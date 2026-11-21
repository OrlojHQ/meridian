import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DesktopUpdateNotice } from "../shell/DesktopUpdateNotice";
import {
  UPDATE_DUE_EVENT,
  UPDATE_LATER_KEY,
  UPDATE_PROGRESS_EVENT,
  applyUpdateProgress,
  automaticCheckDue,
  laterRecord,
  readUpdateProgress,
  updateBusyLabel,
  updateIsSnoozed,
} from "../shell/desktopUpdate";
import type { DesktopUpdateStatus } from "../shell/desktop";

const status = (patch: Partial<DesktopUpdateStatus>): DesktopUpdateStatus => ({
  available: true,
  version: "1.2.3",
  action: "install",
  ...patch,
});

function matchMedia(matches: boolean) {
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: () => ({
      matches,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    }),
  });
}

function visibleFace(button: HTMLElement) {
  const face = button.querySelector(".update-card-action-faces > span:not(.is-offstage)");
  if (!face) throw new Error("missing update label");
  return face;
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  window.localStorage.clear();
  delete window.__MERIDIAN_DESKTOP__;
  matchMedia(false);
});

describe("update snooze", () => {
  it("hides one version until the stored time", () => {
    const now = 1_000;
    const record = laterRecord("1.2.3", now);
    expect(record.until - now).toBe(24 * 60 * 60 * 1000);
    const raw = JSON.stringify(record);
    expect(updateIsSnoozed(raw, "1.2.3", record.until - 1)).toBe(true);
    expect(updateIsSnoozed(raw, "1.2.3", record.until)).toBe(false);
    expect(updateIsSnoozed(raw, "1.2.4", record.until - 1)).toBe(false);
    expect(updateIsSnoozed("not-json", "1.2.3", now)).toBe(false);
  });
});

describe("automatic update checks", () => {
  it("is due on the first check, after the gap, and after a clock change", () => {
    expect(automaticCheckDue(undefined, 0, 1_000)).toBe(true);
    expect(automaticCheckDue(5_000, 5_999, 1_000)).toBe(false);
    expect(automaticCheckDue(5_000, 6_000, 1_000)).toBe(true);
    expect(automaticCheckDue(5_000, 4_000, 1_000)).toBe(true);
  });
});

describe("update progress", () => {
  it("reads a download percent and an install phase", () => {
    expect(readUpdateProgress(null)).toBeNull();
    expect(readUpdateProgress({ phase: "other" })).toBeNull();
    expect(readUpdateProgress({ phase: "download" })).toEqual({ phase: "download" });
    expect(readUpdateProgress({ phase: "download", percent: 42.9 })).toEqual({
      phase: "download",
      percent: 42,
    });
    expect(readUpdateProgress({ phase: "download", percent: 140 })).toEqual({
      phase: "download",
      percent: 100,
    });
    expect(readUpdateProgress({ phase: "download", percent: Number.NaN })).toEqual({
      phase: "download",
    });
    expect(readUpdateProgress({ phase: "install", percent: 10 })).toEqual({ phase: "install" });
    expect(updateBusyLabel(undefined)).toBe("Downloading…");
    expect(updateBusyLabel({ phase: "download", percent: 42 })).toBe("Downloading… 42%");
    expect(updateBusyLabel({ phase: "install" })).toBe("Restarting…");
  });

  it("keeps a known percent when a later event has no size", () => {
    const known = { phase: "download" as const, percent: 42 };
    expect(applyUpdateProgress(known, { phase: "download" })).toEqual(known);
    expect(applyUpdateProgress(known, { phase: "nope" })).toEqual(known);
    expect(applyUpdateProgress(known, { phase: "install" })).toEqual({ phase: "install" });
    expect(applyUpdateProgress({ phase: "install" }, { phase: "download", percent: 7 })).toEqual({
      phase: "download",
      percent: 7,
    });
  });
});

describe("desktop update notice", () => {
  it("offers update and restart, and Later hides that version", async () => {
    const checkForUpdates = vi.fn().mockResolvedValue(status({}));
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates,
      installUpdate: vi.fn(),
    };
    const user = userEvent.setup();
    const { unmount } = render(<DesktopUpdateNotice />);
    expect(await screen.findByText("Meridian 1.2.3")).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "Update available" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Update & restart" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Remind me later" }));
    expect(screen.queryByText("Meridian 1.2.3")).not.toBeInTheDocument();

    const seen = checkForUpdates.mock.calls.length;
    unmount();
    render(<DesktopUpdateNotice />);
    await waitFor(() => expect(checkForUpdates.mock.calls.length).toBeGreaterThan(seen));
    expect(screen.queryByText("Meridian 1.2.3")).not.toBeInTheDocument();

    window.dispatchEvent(new CustomEvent("meridian-check-updates"));
    expect(await screen.findByText("Meridian 1.2.3")).toBeInTheDocument();
  });

  it("shows the install command instead of a self-update button", async () => {
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates: vi.fn().mockResolvedValue(
        status({
          action: "command",
          command: "brew upgrade --cask meridian-desktop",
        }),
      ),
    };
    render(<DesktopUpdateNotice />);
    expect(
      await screen.findByText(
        (_, element) =>
          element?.tagName === "CODE" && element.textContent === "brew upgrade --cask meridian-desktop",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Update & restart" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Remind me later" })).toBeInTheDocument();
  });

  it("shows the result of a manual check and dismisses it", async () => {
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates: vi
        .fn()
        .mockResolvedValue({ available: false, detail: "Meridian is up to date." }),
    };
    const user = userEvent.setup();
    render(<DesktopUpdateNotice />);
    window.dispatchEvent(new CustomEvent("meridian-check-updates"));
    expect(await screen.findByText("Meridian is up to date.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText("Meridian is up to date.")).not.toBeInTheDocument();
  });

  it("keeps the card when the install fails", async () => {
    const installUpdate = vi.fn().mockRejectedValue(new Error("boom"));
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates: vi.fn().mockResolvedValue(status({})),
      installUpdate,
    };
    const user = userEvent.setup();
    render(<DesktopUpdateNotice />);
    await user.click(await screen.findByRole("button", { name: "Update & restart" }));
    expect(installUpdate).toHaveBeenCalledOnce();
    expect(await screen.findByText("The update could not be installed.")).toBeInTheDocument();
    const button = screen.getByRole("button", { name: "Update & restart" });
    expect(button).toBeEnabled();
    expect(button).not.toHaveAttribute("aria-busy");
    expect(visibleFace(button).querySelector(".spinner")).toBeNull();
  });

  it("shows a spinner while the update is in flight and keeps both labels", async () => {
    let rejectInstall: (reason?: unknown) => void = () => undefined;
    const installUpdate = vi.fn().mockImplementation(
      () =>
        new Promise<void>((_resolve, reject) => {
          rejectInstall = reject;
        }),
    );
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates: vi.fn().mockResolvedValue(status({})),
      installUpdate,
    };
    const user = userEvent.setup();
    render(<DesktopUpdateNotice />);
    const idle = await screen.findByRole("button", { name: "Update & restart" });
    expect(visibleFace(idle).querySelector(".spinner")).toBeNull();
    expect(idle.querySelector('[data-reduced-motion="false"]')).toBeTruthy();
    window.dispatchEvent(
      new CustomEvent(UPDATE_PROGRESS_EVENT, { detail: { phase: "download", percent: 42 } }),
    );
    expect(screen.getByRole("button", { name: "Update & restart" })).toBeEnabled();

    await user.click(idle);
    const busy = await screen.findByRole("button", { name: "Downloading…" });
    expect(busy).toBeDisabled();
    expect(busy).toHaveAttribute("aria-busy", "true");
    const spinner = visibleFace(busy).querySelector(".spinner");
    expect(spinner).toBeInTheDocument();
    expect(spinner).toHaveAttribute("aria-hidden", "true");
    const reserved = [...busy.querySelectorAll(".is-offstage")].map((node) => node.textContent);
    expect(reserved.some((text) => text?.includes("Update & restart"))).toBe(true);
    expect(reserved.some((text) => text?.includes("Downloading… 100%"))).toBe(true);

    window.dispatchEvent(
      new CustomEvent(UPDATE_PROGRESS_EVENT, { detail: { phase: "download", percent: 42 } }),
    );
    const percent = await screen.findByRole("button", { name: "Downloading… 42%" });
    expect(percent).toBeDisabled();
    expect(visibleFace(percent).querySelector(".spinner")).toBeInTheDocument();

    window.dispatchEvent(
      new CustomEvent(UPDATE_PROGRESS_EVENT, { detail: { phase: "install" } }),
    );
    const restarting = await screen.findByRole("button", { name: "Restarting…" });
    expect(restarting).toHaveAttribute("aria-busy", "true");
    expect(visibleFace(restarting).querySelector(".spinner")).toBeInTheDocument();

    rejectInstall(new Error("boom"));
    expect(await screen.findByRole("button", { name: "Update & restart" })).toBeEnabled();
  });

  it("uses a static spinner when motion is reduced", async () => {
    matchMedia(true);
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates: vi.fn().mockResolvedValue(status({})),
      installUpdate: () => new Promise<void>(() => undefined),
    };
    const user = userEvent.setup();
    render(<DesktopUpdateNotice />);
    await user.click(await screen.findByRole("button", { name: "Update & restart" }));
    const spinner = visibleFace(await screen.findByRole("button", { name: "Downloading…" })).querySelector(
      ".spinner",
    );
    expect(spinner).toHaveClass("is-reduced");
    expect(spinner).toHaveAttribute("data-reduced-motion", "true");
  });

  it("does not render in the browser", () => {
    render(<DesktopUpdateNotice />);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});

describe("desktop update notice in a running window", () => {
  const minute = 60 * 1000;
  const start = 1_700_000_000_000;

  function clock() {
    let now = start;
    vi.spyOn(Date, "now").mockImplementation(() => now);
    return (ms: number) => {
      now += ms;
    };
  }

  function due() {
    act(() => {
      window.dispatchEvent(new CustomEvent(UPDATE_DUE_EVENT));
    });
  }

  it("shows a release published after the page loaded, without a reload", async () => {
    const advance = clock();
    const checkForUpdates = vi
      .fn()
      .mockResolvedValueOnce({ available: false })
      .mockResolvedValue(status({}));
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates,
      installUpdate: vi.fn(),
    };
    render(<DesktopUpdateNotice />);
    await waitFor(() => expect(checkForUpdates).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("status")).not.toBeInTheDocument();

    // A request right after the page's own check is not repeated.
    advance(minute);
    due();
    expect(checkForUpdates).toHaveBeenCalledTimes(1);

    advance(4 * 60 * minute);
    due();
    expect(await screen.findByText("Meridian 1.2.3")).toBeInTheDocument();
    expect(checkForUpdates).toHaveBeenCalledTimes(2);
  });

  it("checks when the window is shown again after the interval", async () => {
    const advance = clock();
    const checkForUpdates = vi
      .fn()
      .mockResolvedValueOnce({ available: false })
      .mockResolvedValue(status({}));
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates,
    };
    render(<DesktopUpdateNotice />);
    await waitFor(() => expect(checkForUpdates).toHaveBeenCalledTimes(1));

    advance(30 * minute);
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(checkForUpdates).toHaveBeenCalledTimes(1);

    advance(4 * 60 * minute);
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    expect(await screen.findByText("Meridian 1.2.3")).toBeInTheDocument();
  });

  it("keeps Later through automatic checks, and the tray still shows it", async () => {
    const advance = clock();
    const checkForUpdates = vi.fn().mockResolvedValue(status({}));
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates,
      installUpdate: vi.fn(),
    };
    const user = userEvent.setup();
    render(<DesktopUpdateNotice />);
    await user.click(await screen.findByRole("button", { name: "Remind me later" }));
    expect(screen.queryByText("Meridian 1.2.3")).not.toBeInTheDocument();

    for (let hours = 0; hours < 20; hours += 5) {
      advance(5 * 60 * minute);
      due();
    }
    await waitFor(() => expect(checkForUpdates).toHaveBeenCalledTimes(5));
    expect(screen.queryByText("Meridian 1.2.3")).not.toBeInTheDocument();

    act(() => {
      window.dispatchEvent(new CustomEvent("meridian-check-updates"));
    });
    expect(await screen.findByText("Meridian 1.2.3")).toBeInTheDocument();
  });

  it("listens for the shell even when the bridge arrives after the page", async () => {
    clock();
    render(<DesktopUpdateNotice />);
    const checkForUpdates = vi.fn().mockResolvedValue(status({}));
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates,
    };
    due();
    expect(await screen.findByText("Meridian 1.2.3")).toBeInTheDocument();
  });

  it("does not reset an install in flight", async () => {
    const advance = clock();
    const checkForUpdates = vi.fn().mockResolvedValue(status({}));
    window.__MERIDIAN_DESKTOP__ = {
      notify: () => undefined,
      setAttention: () => undefined,
      checkForUpdates,
      installUpdate: () => new Promise<void>(() => undefined),
    };
    const user = userEvent.setup();
    render(<DesktopUpdateNotice />);
    await user.click(await screen.findByRole("button", { name: "Update & restart" }));
    expect(await screen.findByRole("button", { name: "Downloading…" })).toBeDisabled();

    advance(5 * 60 * minute);
    due();
    await waitFor(() => expect(checkForUpdates).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("button", { name: "Downloading…" })).toBeDisabled();
  });
});
