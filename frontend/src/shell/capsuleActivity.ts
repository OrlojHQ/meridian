import type { Capsule, ChildRollup, PinnedHarnessSetup, Run, Thread } from "../api/generated/types.gen";
import { phaseLabels } from "../components/startup";
import { endedSessionState, failureText } from "../components/threadChat";
import { describeChecks } from "./checks";

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
  dsh: "DeepSeek Harness",
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

const routineLabels: Record<string, string> = {
  changed: "Routine changed files",
  proposed: "Routine proposed a delivery",
  timed_out: "Routine hit its time limit",
  failed: "Routine failed",
};

// A finished scheduled run that kept its Capsule waits for the operator
// until the Capsule is deleted (ADR 0057). That is also what frees a slot
// in the routine's backlog. A run that is working or asking defers to the
// ordinary labels below.
function describeRoutineRun(capsule: Capsule, threads: Thread[]): string | undefined {
  const note = capsule.routine;
  if (!note || note.state !== "finished" || !note.outcome) return undefined;
  const label = routineLabels[note.outcome];
  if (!label) return undefined;
  if (capsule.state === "Paused" && capsule.desiredState === "Ready") return undefined;
  const live = threads.some((thread) => thread.state === "active" && (thread.awaiting || thread.turn === "working"));
  return live ? undefined : label;
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
      detail: failureText(capsule.failure) || undefined,
    };
  }
  if (capsule.state === "Deleting" || capsule.desiredState === "Deleted") {
    return { group: "working", label: "Deleting", agent, since: capsule.updatedAt };
  }
  if (capsule.state === "Creating") {
    const phase = capsule.startup?.phase;
    return {
      group: "working",
      label: phase ? phaseLabels[phase] : "Starting up",
      agent,
      since: capsule.createdAt,
    };
  }
  if (capsule.state === "Preparing") {
    return {
      group: "working",
      label: "Preparing environment",
      agent,
      since: capsule.updatedAt,
    };
  }
  const scheduled = describeRoutineRun(capsule, threads);
  if (scheduled) {
    return { group: "attention", label: scheduled, agent, since: capsule.updatedAt };
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

  const agentWorking = threads.some((thread) => thread.state === "active" && thread.turn === "working");
  const checks = describeChecks(capsule.checks, agentWorking);
  if (checks) {
    return {
      group: checks.group,
      label: checks.label,
      agent,
      since: capsule.checks?.finishedAt ?? capsule.updatedAt,
    };
  }

  if (!run) {
    return { group: "idle", label: "No agent running", agent, since: capsule.updatedAt };
  }
  const idleSession = threads.some(
    (thread) => thread.currentRunId === run.id && thread.turn === "idle",
  );
  if (run.state === "Running" && idleSession) {
    return { group: "idle", label: `${who} idle`, agent, since: run.startedAt ?? run.createdAt };
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
      detail: failureText(run.failure) || undefined,
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

export const sessionPath = (capsuleId: string, sessionId?: string) =>
  `/ui/capsules/${encodeURIComponent(capsuleId)}${
    sessionId ? `?session=${encodeURIComponent(sessionId)}` : ""
  }`;

export const projectPath = (projectId: string) =>
  `/ui/projects/${encodeURIComponent(projectId)}`;

export function projectSecretNames(project: {
  gitSecretName?: string;
  gitPushSecretName?: string;
  githubAPISecretName?: string;
  harnessSecretNames?: string[];
}) {
  const names = [
    project.gitSecretName,
    project.gitPushSecretName,
    project.githubAPISecretName,
    ...(project.harnessSecretNames ?? []),
  ].filter((name): name is string => Boolean(name));
  return [...new Set(names)];
}

// After the daemon accepts a Project delete, leave that Project for the most
// recently updated other Project. Open its latest Capsule when it has one, so
// the empty Project page is only used when that Project has no Capsules.
// Otherwise open the Inbox.
export function destinationAfterProjectDelete(
  deletedId: string,
  projects: readonly { id: string; createdAt: string; updatedAt: string; state?: string }[],
  capsules: readonly Capsule[],
): string {
  let next: (typeof projects)[number] | undefined;
  for (const candidate of projects) {
    if (candidate.id === deletedId || candidate.state === "deleted") continue;
    if (!next || compareRecency(candidate, next) > 0) next = candidate;
  }
  if (!next) return "/ui/";
  let landing: Capsule | undefined;
  for (const candidate of capsules) {
    if (candidate.projectId !== next.id) continue;
    if (
      candidate.state === "Deleting" ||
      candidate.state === "Deleted" ||
      candidate.desiredState === "Deleted"
    ) {
      continue;
    }
    if (!landing || compareRecency(candidate, landing) > 0) landing = candidate;
  }
  if (landing) return sessionPath(landing.id);
  return projectPath(next.id);
}

const timeValue = (value: string) => {
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? 0 : parsed;
};

// A Capsule that is already leaving is not a place to land. The comparison
// stays inside one Project so a newer Capsule elsewhere cannot win.
function isRemainingCapsule(
  candidate: Capsule,
  deleted: { id: string; projectId: string },
) {
  return (
    candidate.id !== deleted.id &&
    candidate.projectId === deleted.projectId &&
    candidate.state !== "Deleting" &&
    candidate.state !== "Deleted" &&
    candidate.desiredState !== "Deleted"
  );
}

function compareRecency(
  left: { id: string; createdAt: string; updatedAt: string },
  right: { id: string; createdAt: string; updatedAt: string },
) {
  const updated = timeValue(left.updatedAt) - timeValue(right.updatedAt);
  if (updated !== 0) return updated;
  const created = timeValue(left.createdAt) - timeValue(right.createdAt);
  if (created !== 0) return created;
  if (left.id === right.id) return 0;
  return left.id > right.id ? 1 : -1;
}

// After the daemon accepts a delete, leave that Capsule for the most recently
// updated sibling that is still in the Project. Otherwise open the Project's
// empty state, or the Inbox when that Project is not available to show.
export function destinationAfterCapsuleDelete(
  deleted: { id: string; projectId: string },
  siblings: readonly Capsule[],
  projectAvailable: boolean,
): string {
  let next: Capsule | undefined;
  for (const candidate of siblings) {
    if (!isRemainingCapsule(candidate, deleted)) continue;
    if (!next || compareRecency(candidate, next) > 0) next = candidate;
  }
  if (next) return sessionPath(next.id);
  if (projectAvailable) return projectPath(deleted.projectId);
  return "/ui/";
}

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

export function threadSession(thread: Thread): CapsuleSession {
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
  if (thread.currentRunState === "Running" && thread.turn === "idle") {
    return { ...base, group: "idle", label: "Idle" };
  }
  if (thread.currentRunState && activeRunStates.has(thread.currentRunState)) {
    return { ...base, group: "working", label: "Working" };
  }
  if (thread.state === "paused") return { ...base, group: "paused", label: "Paused" };
  if (thread.state === "archived") return { ...base, group: "sealed", label: "Archived" };
  const ended = endedSessionState(thread);
  if (ended) return { ...base, group: "idle", label: ended === "Cancelled" ? "Stopped" : "Ended" };
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

export type HeaderAttention = {
  tone: "working" | "attention";
  label: "Working" | "Needs input" | "Failed";
};

// The session tab already shows idle and finished. The header speaks only
// when the open session is working, waiting, or failed.
export function headerAttention(
  capsuleState: string,
  session?: { group: ActivityGroup; label: string },
): HeaderAttention | undefined {
  if (capsuleState === "Failed" || session?.label === "Failed") {
    return { tone: "attention", label: "Failed" };
  }
  if (
    session?.group === "attention" &&
    (session.label === "Waiting for input" || session.label === "Waiting for permission")
  ) {
    return { tone: "attention", label: "Needs input" };
  }
  if (session?.group === "working") return { tone: "working", label: "Working" };
  return undefined;
}

const revisionMonths = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

export function revisionStamp(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  let hours = date.getHours();
  const suffix = hours >= 12 ? "PM" : "AM";
  hours = hours % 12 || 12;
  const minutes = String(date.getMinutes()).padStart(2, "0");
  return `${revisionMonths[date.getMonth()]} ${date.getDate()}, ${hours}:${minutes} ${suffix}`;
}

// One line under the Capsule title: the saved setup for the harness this
// Capsule runs. The time is local. The Settings sentence appears only when
// that setup's current revision is not the one this Capsule started with.
export function pinnedSetupText(
  pins: PinnedHarnessSetup[] = [],
  harness = "",
  current?: { id: string; revision: string } | null,
) {
  const base = harness.replace(/-structured$/, "");
  const pin = base ? pins.find((item) => item.harness === base) : undefined;
  if (!pin) return "";
  const stamp = revisionStamp(pin.revisedAt);
  const line = stamp ? `${pin.name} · ${stamp}` : pin.name;
  const edited =
    current != null && (current.id !== pin.setupId || current.revision !== pin.revision);
  return edited ? `${line}. Edits in Settings apply to the next Capsule.` : line;
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

// currentSubagents counts a lead's subagents that are still with it: archived
// subagents are folded away and removed ones are not in the rollup at all.
export function currentSubagents(rollup: ChildRollup) {
  return Math.max(0, rollup.total - rollup.archived);
}

export function rollupSummary(rollup: ChildRollup) {
  const count = currentSubagents(rollup);
  return `${count} ${count === 1 ? "subagent" : "subagents"}`;
}
