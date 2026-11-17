const PROTECTED = new Set(["main", "master", "trunk"]);

export function deliveryHasChanges(inspection?: {
  dirty: boolean;
  branch?: string;
  defaultBranch?: string;
} | null) {
  if (!inspection) return false;
  if (inspection.dirty) return true;
  const branch = inspection.branch?.trim() ?? "";
  const base = inspection.defaultBranch?.trim() ?? "";
  return Boolean(branch && base && branch !== base);
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
