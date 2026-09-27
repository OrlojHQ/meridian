import type { Capsule, Run, Thread } from "../api/generated/types.gen";

// Activity is derived only from Capsule and Run resources. Git status,
// workspace files, and previews record Capsule activity on the server, so
// polling them from the working set would keep idle Capsules awake.

export type ActivityGroup =
  | "attention"
  | "working"
  | "finished"
  | "idle"
  | "paused"
  | "sealed";

export type CapsuleActivity = {
  group: ActivityGroup;
  label: string;
  agent: string;
  since?: string;
  detail?: string;
};

export const activityGroups: { group: ActivityGroup; title: string }[] = [
  { group: "attention", title: "Needs attention" },
  { group: "working", title: "Working" },
  { group: "finished", title: "Ready for review" },
  { group: "idle", title: "Idle" },
  { group: "paused", title: "Paused" },
  { group: "sealed", title: "Sealed" },
];

const harnessNames: Record<string, string> = {
  claude: "Claude Code",
  codex: "Codex",
  opencode: "OpenCode",
  pi: "Pi",
  mock: "Mock (test)",
};

export function harnessName(harness?: string) {
  if (!harness) return "";
  return harnessNames[harness] ?? harness;
}

const activeRunStates = new Set<Run["state"]>([
  "Queued",
  "Starting",
  "Running",
  "Cancelling",
]);

export function latestRun(runs: Run[] = []) {
  let latest: Run | undefined;
  for (const run of runs) {
    if (!latest || Date.parse(run.createdAt) >= Date.parse(latest.createdAt)) {
      latest = run;
    }
  }
  return latest;
}

export function describeCapsule(
  capsule: Capsule,
  runs: Run[] = [],
  threads: Thread[] = [],
): CapsuleActivity {
  const run = latestRun(runs);
  const agent = harnessName(capsule.harness || run?.harness || threads[0]?.harness);
  const who = agent || "Agent";

  if (capsule.state === "Sealed") {
    return { group: "sealed", label: "Sealed", agent, since: capsule.updatedAt };
  }
  if (capsule.state === "Failed") {
    return {
      group: "attention",
      label: "Capsule failed",
      agent,
      since: capsule.updatedAt,
      detail: capsule.failure,
    };
  }
  if (capsule.state === "Deleting" || capsule.desiredState === "Deleted") {
    return { group: "working", label: "Deleting", agent, since: capsule.updatedAt };
  }
  if (capsule.state === "Creating") {
    return { group: "working", label: "Starting up", agent, since: capsule.createdAt };
  }
  if (capsule.state === "Preparing") {
    return {
      group: "working",
      label: "Preparing environment",
      agent,
      since: capsule.updatedAt,
    };
  }
  if (capsule.state === "Paused") {
    return {
      group: capsule.desiredState === "Ready" ? "working" : "paused",
      label: capsule.desiredState === "Ready" ? "Resuming" : "Paused",
      agent,
      since: capsule.updatedAt,
    };
  }
  if (capsule.desiredState === "Paused") {
    return { group: "working", label: "Pausing", agent, since: capsule.updatedAt };
  }
  if (capsule.desiredState === "Sealed" || capsule.maintenance === "seal") {
    return { group: "working", label: "Sealing", agent, since: capsule.updatedAt };
  }
  if (capsule.maintenance) {
    return {
      group: "working",
      label: capsule.maintenance === "capture" ? "Capturing workspace" : "Busy",
      agent,
      since: capsule.updatedAt,
    };
  }

  const waiting = threads.find(
    (thread) => thread.state === "active" && thread.awaiting,
  )?.awaiting;
  if (waiting) {
    return {
      group: "attention",
      label: waiting.kind === "permission" ? "Waiting for permission" : "Waiting for input",
      agent,
      since: waiting.since,
    };
  }

  if (!run) {
    return { group: "idle", label: "No agent running", agent, since: capsule.updatedAt };
  }
  if (activeRunStates.has(run.state)) {
    const label =
      run.state === "Running"
        ? `${who} working`
        : run.state === "Cancelling"
          ? `Stopping ${who}`
          : `Starting ${who}`;
    return { group: "working", label, agent, since: run.startedAt ?? run.createdAt };
  }
  const finishedAt = run.finishedAt ?? run.updatedAt;
  if (run.state === "Failed") {
    return {
      group: "attention",
      label: `${who} failed`,
      agent,
      since: finishedAt,
      detail: run.failure,
    };
  }
  if (run.state === "Cancelled") {
    return { group: "finished", label: `${who} stopped`, agent, since: finishedAt };
  }
  return { group: "finished", label: `${who} finished`, agent, since: finishedAt };
}

// A session is one agent conversation inside a Capsule: the native terminal
// of a launcher Capsule, or a retained structured Thread. Like Capsule
// activity, sessions are derived only from content-free summaries.
export type CapsuleSession = {
  id: string;
  kind: "terminal" | "thread";
  harness: string;
  group: ActivityGroup;
  label: string;
  since?: string;
  archived?: boolean;
};

export const TERMINAL_SESSION = "terminal";

function terminalSession(harness: string, runs: Run[]): CapsuleSession {
  const run = latestRun(runs);
  const base = { id: TERMINAL_SESSION, kind: "terminal" as const, harness };
  if (!run) return { ...base, group: "idle", label: "Not started" };
  if (activeRunStates.has(run.state)) {
    return {
      ...base,
      group: "working",
      label: run.state === "Running" ? "Working" : run.state === "Cancelling" ? "Stopping" : "Starting",
      since: run.startedAt ?? run.createdAt,
    };
  }
  const since = run.finishedAt ?? run.updatedAt;
  if (run.state === "Failed") return { ...base, group: "attention", label: "Failed", since };
  return { ...base, group: "finished", label: run.state === "Cancelled" ? "Stopped" : "Finished", since };
}

function threadSession(thread: Thread): CapsuleSession {
  const base = {
    id: thread.id,
    kind: "thread" as const,
    harness: thread.harness,
    since: thread.updatedAt,
    archived: thread.state === "archived",
  };
  if (thread.state === "active" && thread.awaiting) {
    return {
      ...base,
      group: "attention",
      label: thread.awaiting.kind === "permission" ? "Waiting for permission" : "Waiting for input",
      since: thread.awaiting.since,
    };
  }
  if (thread.currentRunState === "Failed") return { ...base, group: "attention", label: "Failed" };
  if (thread.currentRunState && activeRunStates.has(thread.currentRunState)) {
    return { ...base, group: "working", label: "Working" };
  }
  if (thread.state === "paused") return { ...base, group: "paused", label: "Paused" };
  if (thread.state === "archived") return { ...base, group: "sealed", label: "Archived" };
  return { ...base, group: "idle", label: "Idle" };
}

export function capsuleSessions(
  capsule: Capsule,
  runs: Run[] = [],
  threads: Thread[] = [],
): CapsuleSession[] {
  const sessions = threads
    .filter((thread) => thread.state !== "deleted")
    .sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))
    .map(threadSession);
  return capsule.harness ? [terminalSession(capsule.harness, runs), ...sessions] : sessions;
}

export function relativeTime(value?: string, now = Date.now()) {
  if (!value) return "";
  const seconds = Math.max(0, Math.floor((now - Date.parse(value)) / 1_000));
  if (Number.isNaN(seconds)) return "";
  if (seconds < 60) return "now";
  if (seconds < 3_600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86_400) return `${Math.floor(seconds / 3_600)}h`;
  return `${Math.floor(seconds / 86_400)}d`;
}

// The session a Capsule opens on when none is named: whatever needs the
// operator, then whatever is working, then the native terminal, then the
// most recently updated conversation.
export function defaultSession(sessions: CapsuleSession[]) {
  const live = sessions.filter((session) => !session.archived);
  const recent = [...live].sort(
    (a, b) => Date.parse(b.since ?? "") - Date.parse(a.since ?? ""),
  );
  return (
    live.find((session) => session.group === "attention") ??
    live.find((session) => session.group === "working") ??
    live.find((session) => session.kind === "terminal") ??
    recent[0] ??
    sessions[0]
  );
}
