import { api } from "../api/client";
import type { Capsule, Thread } from "../api/generated/types.gen";
import { currentThread } from "./capsuleActivity";

// Pinned Threads (ADR 0059) are the conversations the operator comes back
// to, usually leads. A pin is presentation only: it grants nothing and only
// changes where the Capsule sits in the sidebar and what the Project's
// composer offers to send to.

type PinSource = { capsule: Pick<Capsule, "role">; threads: Thread[] };

/** The Capsule's current Thread when it can carry a pin: live, in a main Capsule. */
export function pinnableThread({ capsule, threads }: PinSource): Thread | undefined {
  if (capsule.role === "sub") return undefined;
  const thread = currentThread(threads);
  if (!thread || (thread.state !== "active" && thread.state !== "paused")) return undefined;
  return thread.structuredSupported === false ? undefined : thread;
}

/** The Capsule's current Thread when the operator has pinned it. */
export function pinnedThread(source: PinSource): Thread | undefined {
  const thread = pinnableThread(source);
  return thread?.pinnedAt ? thread : undefined;
}

/** Pinned rows are ordered by name, not recency, so they stay put. */
export function byName(
  left: { capsule: Pick<Capsule, "id" | "name"> },
  right: { capsule: Pick<Capsule, "id" | "name"> },
) {
  return (
    left.capsule.name.localeCompare(right.capsule.name, undefined, { sensitivity: "base" }) ||
    left.capsule.id.localeCompare(right.capsule.id)
  );
}

export type PinnedLead = { capsule: Capsule; thread: Thread };

/** A Project's pinned Threads, by name, from the working set already loaded. */
export function pinnedLeads(
  items: (PinSource & { capsule: Capsule })[],
  projectId: string,
): PinnedLead[] {
  return items
    .filter((item) => item.capsule.projectId === projectId)
    .flatMap((item) => {
      const thread = pinnedThread(item);
      return thread ? [{ capsule: item.capsule, thread }] : [];
    })
    .sort(byName);
}

export function setThreadPinned(thread: Pick<Thread, "id" | "resourceVersion">, pinned: boolean) {
  return pinned
    ? api.pinThread(thread.id, thread.resourceVersion)
    : api.unpinThread(thread.id, thread.resourceVersion);
}
