import { QueryClient } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api, MeridianAPIError } from "../api/client";
import type { Capsule } from "../api/generated/types.gen";
import { capsuleSessions, sessionNames } from "../shell/capsuleActivity";
import { nameThreadFromFirstMessage } from "../shell/nameThread";
import { awaitingFirstMessage, capsuleNameFromMessage } from "../shell/threadTitle";

const capsule = (name: string, resourceVersion = 3) =>
  ({ id: "cap-1", projectId: "proj-1", name, resourceVersion }) as Capsule;

describe("Capsule names from the first message", () => {
  afterEach(() => vi.restoreAllMocks());

  it("keeps a name within the API's limits", () => {
    expect(capsuleNameFromMessage("Fix the header so it stays on one line.")).toBe(
      "Fix the header so it stays on one line",
    );
    expect(capsuleNameFromMessage("  \n ")).toBe("");
    expect(capsuleNameFromMessage("Fix­ the⁠ header")).toBe("Fix the header");
    const wide = capsuleNameFromMessage("𝔐".repeat(80));
    expect(new TextEncoder().encode(wide).length).toBeLessThanOrEqual(128);
    expect(wide).not.toBe("");
  });

  it("renames a generated name and refreshes the Capsule lists", async () => {
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    vi.spyOn(api, "capsule").mockResolvedValue(capsule("thread-ad942540f1e7"));
    const rename = vi
      .spyOn(api, "renameCapsule")
      .mockResolvedValue(capsule("Fix the header", 4));

    await expect(
      nameThreadFromFirstMessage(queryClient, "cap-1", "Fix the header. Then stop."),
    ).resolves.toMatchObject({ name: "Fix the header" });
    expect(rename).toHaveBeenCalledWith("cap-1", "Fix the header", 3);
    expect(queryClient.getQueryData(["capsule", "cap-1"])).toMatchObject({ name: "Fix the header" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["capsules"] });
  });

  it("never replaces a name the operator chose", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule("release notes"));
    const rename = vi.spyOn(api, "renameCapsule");
    await expect(
      nameThreadFromFirstMessage(new QueryClient(), "cap-1", "Fix the header"),
    ).resolves.toBeUndefined();
    expect(rename).not.toHaveBeenCalled();
  });

  it("does nothing for a message with no words", async () => {
    const read = vi.spyOn(api, "capsule");
    await nameThreadFromFirstMessage(new QueryClient(), "cap-1", "  ");
    expect(read).not.toHaveBeenCalled();
  });

  it("rereads after a conflict and stops if another tab named it first", async () => {
    vi.spyOn(api, "capsule")
      .mockResolvedValueOnce(capsule("thread-ad942540f1e7", 3))
      .mockResolvedValueOnce(capsule("Named elsewhere", 4));
    const rename = vi
      .spyOn(api, "renameCapsule")
      .mockRejectedValue(new MeridianAPIError("changed", "conflict", 409));
    await expect(
      nameThreadFromFirstMessage(new QueryClient(), "cap-1", "Fix the header"),
    ).resolves.toBeUndefined();
    expect(rename).toHaveBeenCalledTimes(1);
  });
});

describe("awaiting the first message", () => {
  it("is true only when the transcript starts at its beginning with no operator text", () => {
    expect(awaitingFirstMessage([])).toBe(true);
    expect(awaitingFirstMessage([{ role: "system", kind: "text", content: "ready", messageSequence: 1 }])).toBe(true);
    expect(awaitingFirstMessage([{ role: "user", kind: "text", content: "hi", messageSequence: 1 }])).toBe(false);
    // Only the newest blocks are loaded: an older operator message may exist.
    expect(awaitingFirstMessage([{ role: "assistant", kind: "text", content: "…", messageSequence: 812 }])).toBe(false);
  });
});

describe("session names", () => {
  const thread = (id: string, createdAt: string) =>
    ({ id, capsuleId: "cap-1", state: "active", harness: "claude", createdAt }) as never;

  it("names a Terminal and numbers Threads only when there are several", () => {
    const one = capsuleSessions(
      { ...capsule("x"), harness: "claude" } as Capsule,
      [],
      [thread("t1", "2026-11-19T00:00:00Z")],
    );
    expect([...sessionNames(one).values()]).toEqual(["Terminal", "Thread"]);
    const two = capsuleSessions(capsule("x"), [], [
      thread("t2", "2026-11-19T00:02:00Z"),
      thread("t1", "2026-11-19T00:01:00Z"),
    ]);
    expect(sessionNames(two)).toEqual(new Map([["t1", "Thread 1"], ["t2", "Thread 2"]]));
  });
});
