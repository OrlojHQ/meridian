import type { ChecksSummary } from "../api/generated/types.gen";

export type CheckPresentation = {
  label: string;
  group: "attention" | "working";
};

// describeChecks turns the content-free summary into the words the operator
// sees. Intermediate retries stay in the working group. A pass or a used-up
// budget is what asks for the operator.
function describeLocal(
  checks: ChecksSummary | undefined,
  agentWorking: boolean,
): CheckPresentation | undefined {
  if (!checks?.configured || !checks.phase) return undefined;
  const attempt = checks.attempt ?? 1;
  const total = checks.maxAttempts ?? attempt;
  switch (checks.phase) {
    case "queued":
    case "running":
      return { group: "working", label: `Running checks · attempt ${attempt} of ${total}` };
    case "reworking":
      return { group: "working", label: `Reworking · attempt ${Math.min(attempt + 1, total)} of ${total}` };
    case "stale":
      return { group: "working", label: "Checks are out of date" };
    case "passed":
      if (agentWorking) return undefined;
      return { group: "attention", label: "Checks passed" };
    case "failing":
      if (agentWorking) return undefined;
      if (attempt >= total) return { group: "attention", label: `Checks failing (${total}/${total})` };
      return { group: "attention", label: "Checks failed" };
    case "error":
      if (agentWorking) return undefined;
      return { group: "attention", label: "Checks could not finish" };
    default:
      return undefined;
  }
}

function describeRemote(checks: ChecksSummary | undefined, agentWorking: boolean): CheckPresentation | undefined {
  const ci = checks?.remoteCI;
  if (!ci || ci.state === "absent") return undefined;
  const label = ci.label;
  switch (ci.state) {
    case "waiting":
      return { group: "working", label };
    case "failing":
      if (ci.reworkSent && (ci.attempt ?? 1) < (ci.maxAttempts ?? 1)) {
        return { group: "working", label };
      }
      if (agentWorking) return { group: "working", label };
      return { group: "attention", label };
    case "passed":
    case "reconnect":
    case "connect":
    case "unreadable":
      if (agentWorking) return undefined;
      return { group: "attention", label };
    default:
      return undefined;
  }
}

// describeChecks turns the content-free summary into the words the operator
// sees. Remote CI sits beside the local commands. A pass, a used-up budget,
// or a login the station cannot read is what asks for the operator.
export function describeChecks(
  checks: ChecksSummary | undefined,
  agentWorking: boolean,
): CheckPresentation | undefined {
  const local = describeLocal(checks, agentWorking);
  const remote = describeRemote(checks, agentWorking);
  if (local?.group === "working") return local;
  if (remote?.group === "working") return remote;
  if (remote) return remote;
  return local;
}

function localBanner(checks: ChecksSummary | undefined, tree?: string): string | undefined {
  if (!checks?.configured) return undefined;
  if (!checks.phase) return "Checks have not run for this tree";
  if (tree && checks.tree && checks.tree !== tree) return "Checks are out of date for this tree";
  switch (checks.phase) {
    case "passed":
      return "Checks passed on this tree";
    case "failing": {
      const total = checks.maxAttempts ?? checks.attempt ?? 1;
      const attempt = checks.attempt ?? total;
      return attempt >= total ? `Checks failing (${total}/${total} attempts)` : "Checks failed";
    }
    case "reworking": {
      const total = checks.maxAttempts ?? 1;
      const next = Math.min((checks.attempt ?? 1) + 1, total);
      return `Reworking · attempt ${next} of ${total}`;
    }
    case "running":
    case "queued":
      return `Running checks · attempt ${checks.attempt ?? 1} of ${checks.maxAttempts ?? 1}`;
    case "stale":
      return "Checks are out of date for this tree";
    case "error":
      return "Checks could not finish";
    default:
      return undefined;
  }
}

export function checkBanner(checks: ChecksSummary | undefined, tree?: string): string | undefined {
  const local = localBanner(checks, tree);
  const remote = checks?.remoteCI && checks.remoteCI.state !== "absent" ? checks.remoteCI.label : undefined;
  if (local && remote) return `${local} · ${remote}`;
  return remote || local;
}
