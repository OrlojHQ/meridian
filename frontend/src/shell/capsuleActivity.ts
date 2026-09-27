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
  const agent = harnessName(capsule.harness || run?.harness) || "Thread";

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
        ? `${agent} working`
        : run.state === "Cancelling"
          ? `Stopping ${agent}`
          : `Starting ${agent}`;
    return { group: "working", label, agent, since: run.startedAt ?? run.createdAt };
  }
  const finishedAt = run.finishedAt ?? run.updatedAt;
  if (run.state === "Failed") {
    return {
      group: "attention",
      label: `${agent} failed`,
      agent,
      since: finishedAt,
      detail: run.failure,
    };
  }
  if (run.state === "Cancelled") {
    return { group: "finished", label: `${agent} stopped`, agent, since: finishedAt };
  }
  return { group: "finished", label: `${agent} finished`, agent, since: finishedAt };
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
