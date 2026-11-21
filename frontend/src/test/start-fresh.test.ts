import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Thread } from "../api/generated/types.gen";
import { startFresh } from "../shell/startFresh";

const base = {
  id: "old",
  capsuleId: "cap-1",
  harness: "claude",
  encryptedAtRest: true,
  messageCount: 3,
  encryptedBytes: 64,
  createdAt: "2026-11-19T00:00:00Z",
  updatedAt: "2026-11-19T00:00:00Z",
  resourceVersion: 4,
} satisfies Partial<Thread>;
const thread = (overrides: Partial<Thread>) => ({ ...base, state: "active", ...overrides }) as Thread;
const fresh = thread({ id: "new", resourceVersion: 1 });

function stubCreate() {
  const create = vi.spyOn(api, "createThread").mockResolvedValue({ thread: fresh } as never);
  return create;
}

describe("start fresh", () => {
  afterEach(() => vi.restoreAllMocks());

  it("stops a live session, waits for it to pause, then starts a new Thread", async () => {
    vi.spyOn(api, "thread")
      .mockResolvedValueOnce(thread({ currentRunState: "Running" }))
      .mockResolvedValueOnce(thread({ state: "paused", currentRunState: "Cancelled", resourceVersion: 6 }));
    const session = vi.spyOn(api, "threadSession").mockImplementation(async (action) =>
      action === "cancel"
        ? ({ thread: thread({ currentRunState: "Cancelling", resourceVersion: 5 }) } as never)
        : ({ thread: fresh } as never),
    );
    const archive = vi.spyOn(api, "archiveThread");
    const create = stubCreate();

    await expect(startFresh(base)).resolves.toBe(fresh);
    expect(session).toHaveBeenNthCalledWith(1, "cancel", "old", 4);
    expect(archive).not.toHaveBeenCalled();
    expect(create).toHaveBeenCalledWith("cap-1", "claude");
    expect(session).toHaveBeenNthCalledWith(2, "start", "new", 1);
  });

  it("starts right away after a session that already ended", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(thread({ state: "paused", currentRunState: "Failed" }));
    const session = vi.spyOn(api, "threadSession").mockResolvedValue({ thread: fresh } as never);
    const create = stubCreate();

    await startFresh(base);
    expect(session).toHaveBeenCalledTimes(1);
    expect(session).toHaveBeenCalledWith("start", "new", 1);
    expect(create).toHaveBeenCalledTimes(1);
  });

  it("archives a Thread that never started, since nothing else frees its slot", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(thread({}));
    const archive = vi
      .spyOn(api, "archiveThread")
      .mockResolvedValue(thread({ state: "archived", resourceVersion: 5 }));
    vi.spyOn(api, "threadSession").mockResolvedValue({ thread: fresh } as never);
    stubCreate();

    await startFresh(base);
    expect(archive).toHaveBeenCalledWith("old", 4);
  });

  it("starts nothing when the new Thread cannot be created", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(thread({ state: "paused" }));
    vi.spyOn(api, "createThread").mockRejectedValue(new Error("Capsule already has an active Thread"));
    const session = vi.spyOn(api, "threadSession");

    await expect(startFresh(base)).rejects.toThrow("active Thread");
    expect(session).not.toHaveBeenCalled();
  });

  it("moves a pin to the new Thread so the Capsule stays pinned", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(
      thread({ state: "paused", currentRunState: "Cancelled", pinnedAt: "2026-11-19T00:00:00Z" }),
    );
    const unpin = vi.spyOn(api, "unpinThread").mockResolvedValue(thread({ state: "paused", resourceVersion: 5 }));
    vi.spyOn(api, "threadSession").mockResolvedValue({ thread: thread({ id: "new", resourceVersion: 2 }) } as never);
    const pin = vi.spyOn(api, "pinThread").mockResolvedValue(thread({ id: "new", resourceVersion: 3 }));
    stubCreate();

    await startFresh(base);
    expect(unpin).toHaveBeenCalledWith("old", 4);
    expect(pin).toHaveBeenCalledWith("new", 2);
  });

  it("leaves an unpinned Thread unpinned", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(thread({ state: "paused", currentRunState: "Failed" }));
    vi.spyOn(api, "threadSession").mockResolvedValue({ thread: fresh } as never);
    const pin = vi.spyOn(api, "pinThread");
    const unpin = vi.spyOn(api, "unpinThread");
    stubCreate();

    await startFresh(base);
    expect(pin).not.toHaveBeenCalled();
    expect(unpin).not.toHaveBeenCalled();
  });
});
