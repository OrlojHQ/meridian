import type { Capsule, CapsuleStartupPhase } from "../api/generated/types.gen";

export const phaseLabels: Record<CapsuleStartupPhase, string> = {
  pulling_image: "Pulling image",
  creating_capsule: "Creating Capsule",
  starting_harness: "Starting harness",
  connecting: "Connecting",
};

export function formatBytes(bytes: number) {
  if (!Number.isFinite(bytes) || bytes < 0) return "0 B";
  const mb = 1_000_000;
  const kb = 1_000;
  if (bytes >= 10 * mb) return `${Math.round(bytes / mb)} MB`;
  if (bytes >= mb) {
    const tenths = Math.round((bytes / mb) * 10);
    return `${Math.floor(tenths / 10)}.${tenths % 10} MB`;
  }
  if (bytes >= kb) return `${Math.round(bytes / kb)} KB`;
  return `${Math.round(bytes)} B`;
}

export function pullSummary(current: number, total?: number) {
  if (total && total > 0) {
    const percent = Math.max(0, Math.min(100, Math.floor((current / total) * 100)));
    return `${formatBytes(current)} / ${formatBytes(total)} · ${percent}%`;
  }
  if (current > 0) return formatBytes(current);
  return "Downloading";
}

export const preparationLabels: Record<NonNullable<Capsule["preparation"]>["stage"], string> = {
  checkout: "Checking out",
  preparing: "Preparing dependencies",
  restoring: "Restoring environment",
  reclone: "Refreshing checkout",
  saving: "Saving preparation",
  ready: "Environment ready",
};

export function isStarting(capsule: { state: Capsule["state"] }) {
  return capsule.state === "Creating" || capsule.state === "Preparing";
}

const repositoryAccessPrefix = "Meridian couldn't access ";

export function isRepositoryAccessFailure(capsule: {
  failure?: string;
  failureCode?: string;
}) {
  return (
    capsule.failureCode === "repository_access" ||
    Boolean(capsule.failure?.startsWith(repositoryAccessPrefix))
  );
}

export function repositoryAccessCopy(failure?: string) {
  if (failure?.startsWith(repositoryAccessPrefix) && !failure.includes("://") && !failure.includes("@")) {
    return failure;
  }
  return "Meridian couldn't access this repository. It may be private.";
}

export function canRetryStartup(capsule: Capsule) {
  if (capsule.state !== "Failed") return false;
  if (capsule.preparation?.stage === "ready") return false;
  if (isRepositoryAccessFailure(capsule)) return false;
  return Boolean(capsule.failureCode || capsule.startup || capsule.preparation);
}
