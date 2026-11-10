import { describe, expect, it } from "vitest";

import type { Capsule, Run, Thread } from "../api/generated/types.gen";
import { MAX_FAILURE_TEXT } from "../components/threadChat";
import {
  capsuleSessions,
  defaultSession,
  describeCapsule,
  destinationAfterCapsuleDelete,
  latestRun,
  projectPath,
  relativeTime,
  sessionPath,
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

  it("reports a live structured session as idle between turns", () => {
    const session: Thread = {
      id: "thread-1",
      capsuleId: capsule.id,
      state: "active",
      harness: "codex",
      currentRunId: "run-1",
      currentRunState: "Running",
      encryptedAtRest: true,
      messageCount: 4,
      encryptedBytes: 512,
      createdAt: "2026-08-23T01:00:00Z",
      updatedAt: "2026-08-23T01:05:00Z",
      resourceVersion: 3,
    };
    const running = [run({ state: "Running" })];
    expect(describeCapsule(capsule, running, [{ ...session, turn: "idle" }])).toMatchObject({
      group: "idle",
      label: "Codex idle",
    });
    expect(describeCapsule(capsule, running, [{ ...session, turn: "working" }]).label).toBe(
      "Codex working",
    );
    expect(
      describeCapsule(capsule, running, [{ ...session, currentRunId: "other", turn: "idle" }]).label,
    ).toBe("Codex working");
    const [, tab] = capsuleSessions(capsule, running, [{ ...session, turn: "idle" }]);
    expect(tab).toMatchObject({ group: "idle", label: "Idle" });
    expect(capsuleSessions(capsule, running, [{ ...session, turn: "working" }])[1]).toMatchObject({
      group: "working",
      label: "Working",
    });
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

  it("labels ended sessions honestly and bounds failure detail", () => {
    const base: Thread = {
      id: "thread-1",
      capsuleId: capsule.id,
      state: "active",
      harness: "claude",
      currentRunId: "run-1",
      encryptedAtRest: true,
      messageCount: 1,
      encryptedBytes: 64,
      createdAt: "2026-08-23T01:00:00Z",
      updatedAt: "2026-08-23T01:00:00Z",
      resourceVersion: 1,
    };
    const labels = capsuleSessions({ ...capsule, harness: undefined }, [], [
      { ...base, id: "failed", currentRunState: "Failed" },
      { ...base, id: "stopped", currentRunState: "Cancelled" },
      { ...base, id: "exited", currentRunState: "Succeeded" },
      { ...base, id: "paused", state: "paused", currentRunState: "Cancelled" },
    ]).map((session) => [session.id, session.group, session.label]);
    expect(labels).toEqual([
      ["failed", "attention", "Failed"],
      ["stopped", "idle", "Stopped"],
      ["exited", "idle", "Ended"],
      ["paused", "paused", "Paused"],
    ]);

    const hostile = `\u202eboom\u001b[31m ${"x".repeat(2_000)}`;
    const detail = describeCapsule(capsule, [run({ state: "Failed", failure: hostile })]).detail!;
    expect(detail.startsWith("boom[31m x")).toBe(true);
    expect(Array.from(detail)).toHaveLength(MAX_FAILURE_TEXT);
    expect(detail.endsWith("…")).toBe(true);
    expect(describeCapsule(capsule, [run({ state: "Failed", failure: " \n " })]).detail).toBeUndefined();
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

  it("leaves a deleted Capsule for the newest sibling, the Project, or the Inbox", () => {
    const sibling = (overrides: Partial<Capsule>): Capsule => ({
      ...capsule,
      ...overrides,
    });
    const current = sibling({
      id: "current",
      updatedAt: "2026-08-23T03:00:00Z",
      createdAt: "2026-08-23T03:00:00Z",
    });
    const recent = sibling({
      id: "recent",
      name: "Recent work",
      updatedAt: "2026-08-23T05:00:00Z",
      createdAt: "2026-08-23T00:00:00Z",
    });
    const createdLater = sibling({
      id: "created-later",
      updatedAt: "2026-08-23T01:00:00Z",
      createdAt: "2026-08-23T04:00:00Z",
    });
    const otherProject = sibling({
      id: "other-project",
      projectId: "project-2",
      updatedAt: "2026-08-23T09:00:00Z",
    });
    const deleting = sibling({
      id: "deleting",
      state: "Deleting",
      desiredState: "Deleted",
      updatedAt: "2026-08-23T08:00:00Z",
    });
    const desiredDeleted = sibling({
      id: "desired-deleted",
      state: "Ready",
      desiredState: "Deleted",
      updatedAt: "2026-08-23T07:00:00Z",
    });
    const deleted = sibling({
      id: "already-deleted",
      state: "Deleted",
      desiredState: "Deleted",
      updatedAt: "2026-08-23T06:00:00Z",
    });
    const siblings = [
      current,
      createdLater,
      otherProject,
      deleting,
      desiredDeleted,
      deleted,
      recent,
    ];
    expect(destinationAfterCapsuleDelete(current, siblings, true)).toBe(
      sessionPath(recent.id),
    );
    const tied = [
      sibling({ id: "b", updatedAt: "2026-08-23T02:00:00Z", createdAt: "2026-08-23T01:00:00Z" }),
      sibling({ id: "a", updatedAt: "2026-08-23T02:00:00Z", createdAt: "2026-08-23T02:00:00Z" }),
    ];
    expect(destinationAfterCapsuleDelete(current, tied, true)).toBe(sessionPath("a"));
    const sameCreated = [
      sibling({ id: "m", updatedAt: "2026-08-23T02:00:00Z", createdAt: "2026-08-23T02:00:00Z" }),
      sibling({ id: "z", updatedAt: "2026-08-23T02:00:00Z", createdAt: "2026-08-23T02:00:00Z" }),
    ];
    expect(destinationAfterCapsuleDelete(current, sameCreated, true)).toBe(sessionPath("z"));
    expect(destinationAfterCapsuleDelete(current, [current], true)).toBe(
      projectPath(current.projectId),
    );
    expect(destinationAfterCapsuleDelete(current, [current], false)).toBe("/ui/");
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
