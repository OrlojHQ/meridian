import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Capabilities } from "../api/generated/types.gen";
import { StationAwareUpdateNotice } from "../shell/DesktopUpdateNotice";
import type { DesktopAppInfo, DesktopBridge, DesktopUpdateStatus } from "../shell/desktop";
import { UPDATE_LATER_KEY, laterRecord, recordUpdateCheck } from "../shell/desktopUpdate";
import { STATION_GAP_LATER_KEY, compareVersions, stationGap } from "../shell/stationVersion";

const NOTICE = "Desktop app is older than this Station";

function capabilities(version: string): Capabilities {
  return {
    providerVersion: "docker/v1",
    isolation: "container",
    attach: false,
    run: false,
    structured: false,
    git: false,
    pause: false,
    snapshot: false,
    clone: false,
    preview: false,
    browse: false,
    delivery: false,
    resourceMetrics: false,
    harnessImages: [],
    build: { version, commit: "abc1234", date: "2026-11-27T00:00:00Z" },
  };
}

function app(version: string, patch: Partial<DesktopAppInfo> = {}): DesktopAppInfo {
  return { version, install: "mac-app", selfUpdate: true, ...patch };
}

function desktop(info: DesktopAppInfo, patch: Partial<DesktopBridge> = {}) {
  const bridge: DesktopBridge = {
    notify: () => undefined,
    setAttention: () => undefined,
    checkForUpdates: vi.fn().mockResolvedValue({ available: false } satisfies DesktopUpdateStatus),
    installUpdate: vi.fn().mockResolvedValue(undefined),
    appInfo: vi.fn().mockResolvedValue(info),
    ...patch,
  };
  window.__MERIDIAN_DESKTOP__ = bridge;
  return bridge;
}

function renderNotice() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <StationAwareUpdateNotice />
    </QueryClientProvider>,
  );
}

// Waits until the page has both versions, so a missing notice is a real "no".
async function settled(bridge: DesktopBridge, station: ReturnType<typeof vi.spyOn>) {
  await waitFor(() => {
    expect(bridge.appInfo).toHaveBeenCalled();
    expect(station).toHaveBeenCalled();
    expect(bridge.checkForUpdates).toHaveBeenCalled();
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.localStorage.clear();
  delete window.__MERIDIAN_DESKTOP__;
  recordUpdateCheck(null);
});

describe("version comparison", () => {
  it("orders releases and prereleases by Semantic Versioning", () => {
    expect(compareVersions("0.18.0", "0.18.1")).toBe(-1);
    expect(compareVersions("0.9.0", "0.10.0")).toBe(-1);
    expect(compareVersions("1.0.0", "0.99.99")).toBe(1);
    expect(compareVersions("0.18.1", "0.18.1")).toBe(0);
    expect(compareVersions("1.0.0-rc.1", "1.0.0")).toBe(-1);
    expect(compareVersions("1.0.0-rc.2", "1.0.0-rc.10")).toBe(-1);
    expect(compareVersions("1.0.0-rc.1", "1.0.0-rc.1.1")).toBe(-1);
    expect(compareVersions("1.0.0-1", "1.0.0-alpha")).toBe(-1);
    expect(compareVersions("1.0.0+build.1", "1.0.0")).toBe(0);
    expect(compareVersions("dev", "1.0.0")).toBeNull();
  });

  it("reports a gap only for an older release app", () => {
    expect(stationGap("0.18.0", "0.18.1")).toEqual({ app: "0.18.0", station: "0.18.1" });
    expect(stationGap("0.18.1", "0.18.1")).toBeNull();
    expect(stationGap("0.19.0", "0.18.1")).toBeNull();
    expect(stationGap("0.0.0", "0.18.1")).toBeNull();
    expect(stationGap("0.18.0", "dev")).toBeNull();
    expect(stationGap(undefined, "0.18.1")).toBeNull();
  });
});

describe("Station version notice", () => {
  it("tells a member on an older app that the Station is newer", async () => {
    const bridge = desktop(app("0.18.0"));
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities("0.18.1"));
    const user = userEvent.setup();
    renderNotice();

    const notice = await screen.findByRole("status", { name: NOTICE });
    expect(notice).toHaveTextContent("Desktop 0.18.0 · Station 0.18.1");
    expect(notice).toHaveTextContent("Update the app to get its fixes.");

    // Nothing found by the automatic check: the card offers a manual one.
    (bridge.checkForUpdates as ReturnType<typeof vi.fn>).mockResolvedValue({
      available: true,
      version: "0.18.1",
      action: "install",
    } satisfies DesktopUpdateStatus);
    await user.click(screen.getByRole("button", { name: "Check for updates" }));
    expect(await screen.findByText("Meridian 0.18.1")).toBeInTheDocument();
    expect(screen.getByText("This app (0.18.0) is older than this Station (0.18.1).")).toBeInTheDocument();
    expect(screen.queryByRole("status", { name: NOTICE })).not.toBeInTheDocument();
  });

  it("adds the Station to an update offer, and Later hides both", async () => {
    desktop(app("0.18.0"), {
      checkForUpdates: vi.fn().mockResolvedValue({
        available: true,
        version: "0.18.1",
        action: "install",
      } satisfies DesktopUpdateStatus),
    });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities("0.18.1"));
    const user = userEvent.setup();
    renderNotice();

    expect(
      await screen.findByText("This app (0.18.0) is older than this Station (0.18.1)."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("status", { name: NOTICE })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Remind me later" }));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("installs from the card when an update was snoozed before the Station moved", async () => {
    window.localStorage.setItem(UPDATE_LATER_KEY, JSON.stringify(laterRecord("0.18.1", Date.now())));
    const bridge = desktop(app("0.18.0"), {
      checkForUpdates: vi.fn().mockResolvedValue({
        available: true,
        version: "0.18.1",
        action: "install",
      } satisfies DesktopUpdateStatus),
    });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities("0.18.1"));
    const user = userEvent.setup();
    renderNotice();

    await screen.findByRole("status", { name: NOTICE });
    await user.click(await screen.findByRole("button", { name: "Update & restart" }));
    expect(bridge.installUpdate).toHaveBeenCalledOnce();
  });

  it("shows the Homebrew command for a copy that does not update itself", async () => {
    window.localStorage.setItem(UPDATE_LATER_KEY, JSON.stringify(laterRecord("0.18.1", Date.now())));
    desktop(app("0.18.0", { install: "homebrew", selfUpdate: false }), {
      checkForUpdates: vi.fn().mockResolvedValue({
        available: true,
        version: "0.18.1",
        action: "command",
        command: "brew upgrade --cask meridian-desktop",
      } satisfies DesktopUpdateStatus),
    });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities("0.18.1"));
    renderNotice();

    const notice = await screen.findByRole("status", { name: NOTICE });
    await waitFor(() =>
      expect(notice.querySelector("code")).toHaveTextContent("brew upgrade --cask meridian-desktop"),
    );
    expect(screen.queryByRole("button", { name: "Update & restart" })).not.toBeInTheDocument();
  });

  it("stays dismissed for this pair of versions and returns for a newer Station", async () => {
    desktop(app("0.18.0"));
    const station = vi.spyOn(api, "capabilities").mockResolvedValue(capabilities("0.18.1"));
    const user = userEvent.setup();
    const { unmount } = renderNotice();
    await user.click(
      await screen.findByRole("button", { name: "Dismiss" }),
    );
    expect(screen.queryByRole("status", { name: NOTICE })).not.toBeInTheDocument();
    expect(window.localStorage.getItem(STATION_GAP_LATER_KEY)).toContain("0.18.0 < 0.18.1");

    unmount();
    station.mockClear();
    const again = renderNotice();
    await settled(window.__MERIDIAN_DESKTOP__!, station);
    expect(screen.queryByRole("status", { name: NOTICE })).not.toBeInTheDocument();

    again.unmount();
    station.mockResolvedValue(capabilities("0.19.0"));
    renderNotice();
    expect(await screen.findByRole("status", { name: NOTICE })).toHaveTextContent("Station 0.19.0");
  });

  it.each([
    ["the same version", "0.18.1", "0.18.1"],
    ["a newer app", "0.19.0", "0.18.1"],
    ["a development app", "0.0.0", "0.18.1"],
    ["a development Station", "0.18.0", "dev"],
  ])("shows nothing for %s", async (_, appVersion, stationVersion) => {
    const bridge = desktop(app(appVersion));
    const station = vi.spyOn(api, "capabilities").mockResolvedValue(capabilities(stationVersion));
    renderNotice();
    await settled(bridge, station);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("shows nothing in a browser and does not ask for the Station version", async () => {
    const station = vi.spyOn(api, "capabilities").mockResolvedValue(capabilities("0.18.1"));
    renderNotice();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(station).not.toHaveBeenCalled();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
