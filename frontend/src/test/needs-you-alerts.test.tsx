import { cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Capsule } from "../api/generated/types.gen";
import {
  NEEDS_YOU_NOTIFICATION_TAG,
  NEEDS_YOU_PHRASE,
} from "../shell/needsYou";
import { useNeedsYouAlerts } from "../shell/useNeedsYouAlerts";
import type { WorkingSetItem } from "../shell/useWorkingSet";

const capsule = (id: string, name: string): Capsule => ({
  id,
  projectId: "project-1",
  timelineId: `timeline-${id}`,
  name,
  harness: "mock",
  state: "Ready",
  desiredState: "Ready",
  restoreComplete: true,
  createdAt: "2026-08-26T00:00:00Z",
  updatedAt: "2026-08-26T00:01:00Z",
  resourceVersion: 1,
});

const attention = (id: string, name: string, since: string): WorkingSetItem => ({
  capsule: capsule(id, name),
  runs: [],
  threads: [],
  activity: {
    group: "attention",
    label: "Waiting for permission",
    agent: "Mock",
    since,
  },
  sessions: [],
});

function Probe({
  items,
  viewed = "",
  ready = true,
}: {
  items: WorkingSetItem[];
  viewed?: string;
  ready?: boolean;
}) {
  useNeedsYouAlerts({ items, viewedCapsuleId: viewed, ready });
  return <p>ready</p>;
}

function mockNotifications(permission: NotificationPermission = "granted") {
  const posted: { title: string; options?: NotificationOptions }[] = [];
  class FakeNotification {
    static permission = permission;
    static requestPermission = vi.fn(async () => {
      FakeNotification.permission = "granted";
      return "granted" as NotificationPermission;
    });
    onclick: (() => void) | null = null;
    constructor(title: string, options?: NotificationOptions) {
      posted.push({ title, options });
    }
    close() {}
  }
  vi.stubGlobal("Notification", FakeNotification);
  return { FakeNotification, posted };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.title = "";
  delete window.__MERIDIAN_DESKTOP__;
  Object.defineProperty(document, "hidden", { configurable: true, value: false });
});

describe("needs-you alerts", () => {
  it("badges the tab with the needs-you count", async () => {
    const view = render(
      <Probe
        items={[attention("capsule-1", "fix-flaky-tests", "2026-08-26T00:01:00Z")]}
      />,
    );
    await waitFor(() => expect(document.title).toBe("(1) Meridian"));
    view.rerender(
      <Probe
        items={[
          attention("capsule-1", "fix-flaky-tests", "2026-08-26T00:01:00Z"),
          attention("capsule-2", "secret prompt in the title", "2026-08-26T00:02:00Z"),
        ]}
      />,
    );
    await waitFor(() => expect(document.title).toBe("(2) Meridian"));
    expect(document.title).not.toContain("fix-flaky-tests");
    expect(document.title).not.toContain("secret prompt");
  });

  it("posts a content-free notification for a hidden-tab arrival and skips the viewed Capsule", async () => {
    const { posted } = mockNotifications("granted");
    Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
    const first = attention("capsule-1", "fix-flaky-tests", "2026-08-26T00:01:00Z");
    const second = attention("capsule-2", "approve bash rm -rf /", "2026-08-26T00:02:00Z");
    const view = render(<Probe items={[first]} viewed="capsule-1" />);
    await waitFor(() => expect(document.title).toBe("(1) Meridian"));
    expect(posted).toHaveLength(0);

    view.rerender(<Probe items={[first, second]} viewed="capsule-1" />);
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toEqual({
      title: NEEDS_YOU_PHRASE,
      options: {
        body: "2 need you",
        tag: NEEDS_YOU_NOTIFICATION_TAG,
      },
    });
    const text = `${posted[0].title}\n${posted[0].options?.body ?? ""}`;
    expect(text).not.toContain("fix-flaky-tests");
    expect(text).not.toContain("approve bash");
    expect(text).not.toContain("rm -rf");
    expect(text).not.toContain("Waiting for permission");

    view.rerender(
      <Probe
        items={[
          first,
          second,
          attention("capsule-3", "thread prompt leak", "2026-08-26T00:03:00Z"),
        ]}
        viewed="capsule-3"
      />,
    );
    await waitFor(() => expect(document.title).toBe("(3) Meridian"));
    expect(posted).toHaveLength(1);
  });

  it("does not notify while the tab is visible or before the working set has settled", async () => {
    const { posted } = mockNotifications("granted");
    Object.defineProperty(document, "hidden", { configurable: true, get: () => false });
    const first = attention("capsule-1", "fix-flaky-tests", "2026-08-26T00:01:00Z");
    const view = render(<Probe items={[]} ready={false} />);
    view.rerender(<Probe items={[first]} ready={false} />);
    await waitFor(() => expect(document.title).toBe("(1) Meridian"));
    expect(posted).toHaveLength(0);
    view.rerender(<Probe items={[first]} ready />);
    expect(posted).toHaveLength(0);
    view.rerender(
      <Probe
        items={[first, attention("capsule-2", "another", "2026-08-26T00:02:00Z")]}
        ready
      />,
    );
    await waitFor(() => expect(document.title).toBe("(2) Meridian"));
    expect(posted).toHaveLength(0);
  });

  it("uses the desktop bridge for a content-free alert and skips the browser notification", async () => {
    const notify = vi.fn();
    const setAttention = vi.fn();
    window.__MERIDIAN_DESKTOP__ = { notify, setAttention };
    const { posted } = mockNotifications("denied");
    Object.defineProperty(document, "hidden", { configurable: true, get: () => true });
    Object.defineProperty(document, "hasFocus", { configurable: true, value: () => false });
    const first = attention("capsule-1", "fix-flaky-tests", "2026-08-26T00:01:00Z");
    const second = attention("capsule-2", "approve bash rm -rf /", "2026-08-26T00:02:00Z");
    const view = render(<Probe items={[first]} />);
    await waitFor(() => expect(setAttention).toHaveBeenCalledWith(1));
    expect(notify).not.toHaveBeenCalled();

    view.rerender(<Probe items={[first, second]} />);
    await waitFor(() => expect(notify).toHaveBeenCalledWith(NEEDS_YOU_PHRASE, "2 need you"));
    expect(posted).toHaveLength(0);
    const text = notify.mock.calls.map((call) => call.join("\n")).join("\n");
    expect(text).not.toContain("fix-flaky-tests");
    expect(text).not.toContain("approve bash");
    expect(text).not.toContain("rm -rf");
  });

  it("does not raise a desktop alert while the window is focused", async () => {
    const notify = vi.fn();
    window.__MERIDIAN_DESKTOP__ = { notify, setAttention: vi.fn() };
    Object.defineProperty(document, "hidden", { configurable: true, get: () => false });
    Object.defineProperty(document, "hasFocus", { configurable: true, value: () => true });
    const first = attention("capsule-1", "fix-flaky-tests", "2026-08-26T00:01:00Z");
    const view = render(<Probe items={[first]} />);
    await waitFor(() => expect(document.title).toBe("(1) Meridian"));
    view.rerender(
      <Probe items={[first, attention("capsule-2", "secret prompt", "2026-08-26T00:02:00Z")]} />,
    );
    await waitFor(() => expect(document.title).toBe("(2) Meridian"));
    expect(notify).not.toHaveBeenCalled();
  });
});
