import { api } from "../api/client";
import type { Thread } from "../api/generated/types.gen";
import { isActiveRunState } from "../components/threadChat";

const POLL_MS = 500;
const POLL_LIMIT = 40;

const wait = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

// A Capsule holds one active Thread. Starting fresh stops this Thread's live
// session, waits for it to let go of that slot, and starts a new Thread with
// the same harness. The old transcript is kept and shows above the new one.
// Each step is an ordinary operator action, so a failure part way leaves a
// stopped Thread the operator can resume or start fresh from again.
export async function startFresh(thread: Pick<Thread, "id" | "capsuleId" | "harness">) {
  let current = await api.thread(thread.id);
  // A pinned Capsule stays pinned: the pin moves to the new Thread.
  const pinned = Boolean(current.pinnedAt);
  if (current.state === "active" && isActiveRunState(current.currentRunState)) {
    current = (await api.threadSession("cancel", current.id, current.resourceVersion)).thread;
  }
  for (let attempt = 0; current.state === "active" && isActiveRunState(current.currentRunState); attempt++) {
    if (attempt >= POLL_LIMIT) {
      throw new Error("The session is still stopping. Try again in a moment.");
    }
    await wait(POLL_MS);
    current = await api.thread(thread.id);
  }
  // A stopped session normally pauses its Thread, which frees the slot. A
  // Thread that never started a session, or has not paused yet, is archived.
  if (current.state === "active") {
    await wait(POLL_MS);
    current = await api.thread(thread.id);
    if (current.state === "active") {
      current = await api.archiveThread(current.id, current.resourceVersion);
    }
  }
  // Archiving cleared the pin; a paused Thread still holds it.
  if (current.pinnedAt) {
    current = await api.unpinThread(current.id, current.resourceVersion);
  }
  const created = await api.createThread(thread.capsuleId, thread.harness);
  const started = await api.threadSession("start", created.thread.id, created.thread.resourceVersion);
  if (pinned) {
    // Cosmetic: a failure leaves an unpinned Thread the operator can pin.
    await api.pinThread(started.thread.id, started.thread.resourceVersion).catch(() => undefined);
  }
  return created.thread;
}
