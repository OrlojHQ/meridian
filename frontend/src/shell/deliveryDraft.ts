const PROTECTED = new Set(["main", "master", "trunk"]);

const activeRunStates = new Set(["Queued", "Starting", "Running", "Cancelling"]);

export function deliveryHasChanges(
  inspection?: {
    dirty?: boolean;
    hasDeliverableChanges?: boolean;
    unpushedCount?: number;
  } | null,
  mode: "create" | "update" = "create",
) {
  if (!inspection) return false;
  if (mode === "update") {
    return inspection.dirty === true || (inspection.unpushedCount ?? 0) > 0;
  }
  if (typeof inspection.hasDeliverableChanges === "boolean") {
    return inspection.hasDeliverableChanges;
  }
  return inspection.dirty === true;
}

// A turn or run that can still write the worktree. Inspection is read again
// while this is true, and once more when it becomes false.
export function agentMayEditWorkspace(input: {
  threads?: { state?: string; turn?: string; currentRunState?: string }[];
  runs?: { state?: string }[];
}) {
  const threadBusy = input.threads?.some((item) => {
    if (item.state === "deleted" || item.state === "archived") return false;
    return item.turn === "working" || activeRunStates.has(item.currentRunState ?? "");
  });
  const runBusy = input.runs?.some((item) => activeRunStates.has(item.state ?? ""));
  return threadBusy === true || runBusy === true;
}

export function suggestedBranch(branch: string | undefined, base: string | undefined, title: string) {
  const current = branch?.trim() ?? "";
  const root = base?.trim() ?? "";
  const refused = new Set([...PROTECTED, root].filter(Boolean));
  if (current && !refused.has(current)) return current;
  const slug = title
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 48)
    .replace(/-+$/g, "");
  if (slug && !refused.has(slug)) return slug;
  return "changes";
}

export function changeSummary(input: {
  files?: number;
  additions?: number;
  deletions?: number;
  dirty: boolean;
  branchAhead: boolean;
  truncated?: boolean;
  pending?: boolean;
  unavailable?: boolean;
}) {
  if (input.pending) return "Reading changes…";
  if (input.unavailable && input.files === undefined) return "Change summary unavailable";
  const files = input.files ?? 0;
  const fileLabel = files === 1 ? "1 file changed" : `${files} files changed`;
  const lines = `+${input.additions ?? 0} −${input.deletions ?? 0}`;
  const commits = input.dirty
    ? "1 commit"
    : input.branchAhead
      ? "commits on this branch"
      : "0 commits";
  return `${fileLabel} · ${lines} · ${commits}${input.truncated ? " · diff truncated" : ""}`;
}
