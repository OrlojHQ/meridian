import { describe, expect, it } from "vitest";

import type { Capsule, Run, Thread } from "../api/generated/types.gen";
import {
  capsuleSessions,
  defaultSession,
  describeCapsule,
  latestRun,
  relativeTime,
} from "../shell/capsuleActivity";

const capsule: Capsule = {
  id: "capsule-1",
  projectId: "project-1",
  timelineId: "timeline-1",
  name: "Workspace",
  harness: "codex",
  state: "Ready",
  desiredState: "Ready",
  restoreComplete: true,
  createdAt: "2026-08-23T00:00:00Z",
  updatedAt: "2026-08-23T00:05:00Z",
  resourceVersion: 2,
};

const run = (overrides: Partial<Run>): Run => ({
  id: "run-1",
  capsuleId: capsule.id,
  harness: "codex",
  state: "Succeeded",
  createdAt: "2026-08-23T01:00:00Z",
  updatedAt: "2026-08-23T01:00:00Z",
  resourceVersion: 1,
  ...overrides,
});

describe("Capsule activity", () => {
  it("picks the newest Run regardless of API order", () => {
    const older = run({ id: "older", createdAt: "2026-08-23T01:00:00Z" });
    const newer = run({ id: "newer", createdAt: "2026-08-23T02:00:00Z" });
    expect(latestRun([older, newer])?.id).toBe("newer");
    expect(latestRun([newer, older])?.id).toBe("newer");
    expect(latestRun([])).toBeUndefined();
  });

  it.each<[string, Partial<Capsule>, Run[], string, string]>([
    ["idle", {}, [], "idle", "No agent running"],
    ["provisioning", { state: "Creating" }, [], "working", "Starting up"],
    ["preparing", { state: "Preparing" }, [], "working", "Preparing environment"],
    ["running", {}, [run({ state: "Running", startedAt: "2026-08-23T01:00:02Z" })], "working", "Codex working"],
    ["queued", {}, [run({ state: "Queued" })], "working", "Starting Codex"],
    ["finished", {}, [run({ state: "Succeeded" })], "finished", "Codex finished"],
    ["stopped", {}, [run({ state: "Cancelled" })], "finished", "Codex stopped"],
    ["run failed", {}, [run({ state: "Failed", failure: "exit 1" })], "attention", "Codex failed"],
    ["capsule failed", { state: "Failed", failure: "clone failed" }, [], "attention", "Capsule failed"],
    ["pausing", { desiredState: "Paused" }, [run({ state: "Running" })], "working", "Pausing"],
    ["paused", { state: "Paused", desiredState: "Paused" }, [], "paused", "Paused"],
    ["resuming", { state: "Paused", desiredState: "Ready" }, [], "working", "Resuming"],
    ["capturing", { maintenance: "capture" }, [run({ state: "Running" })], "working", "Capturing workspace"],
    ["sealing", { maintenance: "seal" }, [], "working", "Sealing"],
    ["sealed", { state: "Sealed", desiredState: "Sealed" }, [run({ state: "Failed" })], "sealed", "Sealed"],
    ["deleting", { state: "Deleting", desiredState: "Deleted" }, [], "working", "Deleting"],
  ])("describes a %s Capsule", (_, overrides, runs, group, label) => {
    const activity = describeCapsule({ ...capsule, ...overrides }, runs);
    expect(activity.group).toBe(group);
    expect(activity.label).toBe(label);
  });

  it("puts a Thread waiting on the operator ahead of Run state", () => {
    const thread: Thread = {
      id: "thread-1",
      capsuleId: capsule.id,
      state: "active",
      harness: "opencode",
      encryptedAtRest: true,
      messageCount: 4,
      encryptedBytes: 512,
      createdAt: "2026-08-23T01:00:00Z",
      updatedAt: "2026-08-23T01:05:00Z",
      resourceVersion: 3,
    };
    const running = [run({ state: "Running" })];
    const permission = describeCapsule(capsule, running, [
      { ...thread, awaiting: { kind: "permission", since: "2026-08-23T01:04:00Z" } },
    ]);
    expect(permission).toMatchObject({
      group: "attention",
      label: "Waiting for permission",
      since: "2026-08-23T01:04:00Z",
    });
    expect(
      describeCapsule(capsule, running, [
        { ...thread, awaiting: { kind: "input", since: "2026-08-23T01:04:00Z" } },
      ]).label,
    ).toBe("Waiting for input");
    expect(describeCapsule(capsule, running, [thread]).label).toBe("Codex working");
    expect(
      describeCapsule(capsule, running, [
        {
          ...thread,
          state: "archived",
          awaiting: { kind: "permission", since: "2026-08-23T01:04:00Z" },
        },
      ]).label,
    ).toBe("Codex working");
  });

  it("names structured Capsules by their Run harness and surfaces failures", () => {
    const activity = describeCapsule(
      { ...capsule, harness: undefined },
      [run({ harness: "opencode", state: "Failed", failure: "adapter crashed" })],
    );
    expect(activity.agent).toBe("OpenCode");
    expect(activity.detail).toBe("adapter crashed");
    const idle = describeCapsule({ ...capsule, harness: undefined });
    expect(idle.agent).toBe("");
    expect(idle.label).toBe("No agent running");
    expect(
      describeCapsule({ ...capsule, harness: undefined }, [
        run({ harness: "", state: "Running" }),
      ]).label,
    ).toBe("Agent working");
  });

  it("derives sessions and opens on the one that needs the operator", () => {
    const thread = (overrides: Partial<Thread>): Thread => ({
      id: "thread-1",
      capsuleId: capsule.id,
      state: "active",
      harness: "claude",
      encryptedAtRest: true,
      messageCount: 1,
      encryptedBytes: 64,
      createdAt: "2026-08-23T01:00:00Z",
      updatedAt: "2026-08-23T01:00:00Z",
      resourceVersion: 1,
      ...overrides,
    });
    const sessions = capsuleSessions(
      capsule,
      [run({ state: "Running" })],
      [
        thread({ id: "deleted", state: "deleted" }),
        thread({ id: "archived", state: "archived" }),
        thread({
          id: "waiting",
          awaiting: { kind: "input", since: "2026-08-23T02:00:00Z" },
        }),
      ],
    );
    expect(sessions.map((session) => [session.id, session.group, session.label])).toEqual([
      ["terminal", "working", "Working"],
      ["archived", "sealed", "Archived"],
      ["waiting", "attention", "Waiting for input"],
    ]);
    expect(defaultSession(sessions)?.id).toBe("waiting");
    expect(defaultSession(sessions.slice(0, 2))?.id).toBe("terminal");
    expect(capsuleSessions({ ...capsule, harness: undefined })).toEqual([]);
  });

  it("measures working time from Run start and finished time from Run end", () => {
    expect(
      describeCapsule(capsule, [
        run({ state: "Running", startedAt: "2026-08-23T01:00:02Z" }),
      ]).since,
    ).toBe("2026-08-23T01:00:02Z");
    expect(
      describeCapsule(capsule, [
        run({ state: "Succeeded", finishedAt: "2026-08-23T01:30:00Z" }),
      ]).since,
    ).toBe("2026-08-23T01:30:00Z");
  });

  it("formats compact relative times", () => {
    const now = Date.parse("2026-08-23T12:00:00Z");
    expect(relativeTime("2026-08-23T11:59:30Z", now)).toBe("now");
    expect(relativeTime("2026-08-23T11:48:00Z", now)).toBe("12m");
    expect(relativeTime("2026-08-23T09:00:00Z", now)).toBe("3h");
    expect(relativeTime("2026-08-21T12:00:00Z", now)).toBe("2d");
    expect(relativeTime(undefined, now)).toBe("");
  });
});
