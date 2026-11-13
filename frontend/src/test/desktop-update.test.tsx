import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { DesktopUpdateNotice } from "../shell/DesktopUpdateNotice";
import { UPDATE_LATER_KEY, laterRecord, updateIsSnoozed } from "../shell/desktopUpdate";
import type { DesktopUpdateStatus } from "../shell/desktop";

const status = (patch: Partial<DesktopUpdateStatus>): DesktopUpdateStatus => ({
  available: true,
  version: "1.2.3",
  action: "install",
  ...patch,
});

afterEach(() => {
  cleanup();
  window.localStorage.clear();
  delete window.__MERIDIAN_DESKTOP__;
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
    expect(screen.getByRole("button", { name: "Update & restart" })).toBeEnabled();
  });

  it("does not render in the browser", () => {
    render(<DesktopUpdateNotice />);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});
