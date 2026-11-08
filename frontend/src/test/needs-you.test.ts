import { describe, expect, it } from "vitest";

import type { Capsule } from "../api/generated/types.gen";
import {
  attentionItems,
  incomingNeedsYou,
  needsYouDocumentTitle,
  needsYouNotification,
  NEEDS_YOU_NOTIFICATION_TAG,
  NEEDS_YOU_PHRASE,
  nextNeedingMe,
  nextNeedingMePath,
  postNeedsYouNotification,
  shouldPostNeedsYouNotification,
} from "../shell/needsYou";
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

const idle = (id: string, name: string): WorkingSetItem => ({
  capsule: capsule(id, name),
  runs: [],
  threads: [],
  activity: { group: "idle", label: "No agent running", agent: "Mock" },
  sessions: [],
});

describe("needs-you routing", () => {
  it("orders attention items by since and cycles from the current Capsule", () => {
    const first = attention("capsule-1", "fix-flaky-tests", "2026-08-26T00:01:00Z");
    const second = attention("capsule-2", "secret prompt in the title", "2026-08-26T00:02:00Z");
    const items = [idle("capsule-3", "Idle work"), second, first];
    expect(attentionItems(items).map((item) => item.capsule.id)).toEqual([
      "capsule-1",
      "capsule-2",
    ]);
    expect(nextNeedingMe(items, "")?.capsule.id).toBe("capsule-1");
    expect(nextNeedingMePath(items, "capsule-1")).toBe("/ui/capsules/capsule-2");
    expect(nextNeedingMePath(items, "capsule-2")).toBe("/ui/capsules/capsule-1");
    expect(nextNeedingMe(items, "capsule-3")?.capsule.id).toBe("capsule-1");
    expect(nextNeedingMe([], "capsule-1")).toBeUndefined();
  });
});

describe("needs-you badge and notifications", () => {
  it("prefixes the document title with the count and keeps the product name", () => {
    expect(needsYouDocumentTitle(0)).toBe("Meridian");
    expect(needsYouDocumentTitle(1)).toBe("(1) Meridian");
    expect(needsYouDocumentTitle(4)).toBe("(4) Meridian");
    expect(needsYouDocumentTitle(4)).not.toContain("fix-flaky-tests");
  });

  it("keeps notification payloads to a count plus Meridian needs you", () => {
    const one = needsYouNotification(1);
    const many = needsYouNotification(4);
    expect(one).toEqual({ title: NEEDS_YOU_PHRASE, body: "1 needs you" });
    expect(many).toEqual({ title: NEEDS_YOU_PHRASE, body: "4 need you" });
    const forbidden = [
      "fix-flaky-tests",
      "secret prompt in the title",
      "Waiting for permission",
      "approve bash",
      "rm -rf",
      "thread-1",
      "helper",
    ];
    for (const payload of [one, many]) {
      const text = `${payload.title}\n${payload.body}`;
      for (const value of forbidden) {
        expect(text.toLowerCase()).not.toContain(value);
      }
    }
  });

  it("posts through the Notification constructor without thread content", () => {
    const posted: { title: string; options?: NotificationOptions }[] = [];
    class FakeNotification {
      onclick: (() => void) | null = null;
      constructor(title: string, options?: NotificationOptions) {
        posted.push({ title, options });
      }
      close() {}
    }
    const payload = postNeedsYouNotification(2, FakeNotification);
    expect(payload).toEqual({ title: NEEDS_YOU_PHRASE, body: "2 need you" });
    expect(posted).toEqual([
      {
        title: NEEDS_YOU_PHRASE,
        options: {
          body: "2 need you",
          tag: NEEDS_YOU_NOTIFICATION_TAG,
        },
      },
    ]);
  });

  it("notifies only for hidden-tab transitions that are not the viewed Capsule", () => {
    const previous = new Set(["capsule-1"]);
    const current = new Set(["capsule-1", "capsule-2", "capsule-3"]);
    expect(incomingNeedsYou(previous, current, "capsule-2")).toEqual(["capsule-3"]);
    expect(incomingNeedsYou(previous, current, "capsule-3")).toEqual(["capsule-2"]);
    expect(
      shouldPostNeedsYouNotification({
        hidden: true,
        permission: "granted",
        incoming: ["capsule-3"],
      }),
    ).toBe(true);
    expect(
      shouldPostNeedsYouNotification({
        hidden: false,
        permission: "granted",
        incoming: ["capsule-3"],
      }),
    ).toBe(false);
    expect(
      shouldPostNeedsYouNotification({
        hidden: true,
        permission: "default",
        incoming: ["capsule-3"],
      }),
    ).toBe(false);
    expect(
      shouldPostNeedsYouNotification({
        hidden: true,
        permission: "granted",
        incoming: [],
      }),
    ).toBe(false);
  });
});
