import type { HarnessProfile } from "../api/generated/types.gen";

export type ComposerDelivery = "steer" | "queue";

// Pi and Codex inject at the next tool boundary from documented user-message
// metadata. Mock has no tool-boundary primitive; steer is immediate inject
// because it does not buffer mid-turn messages. ACP and Claude Code cannot
// steer, so the composer only offers queue.
export function adapterCanSteer(kind: HarnessProfile["adapterKind"] | undefined) {
  return kind === "pi" || kind === "codex" || kind === "mock";
}

export function profileAdapterKind(
  profiles: HarnessProfile[] | undefined,
  harness: string,
) {
  return profiles?.find((profile) => profile.name === harness)?.adapterKind;
}

export function composerBusyHint(
  kind: HarnessProfile["adapterKind"] | undefined,
  canSteer: boolean,
) {
  if (kind === "pi" || kind === "codex") {
    return "Steer is read at the next tool boundary. Queue waits until this turn ends.";
  }
  if (canSteer) {
    return "Steer delivers now. Queue waits until this turn ends.";
  }
  return "This adapter cannot steer. Follow-ups wait until this turn ends.";
}
