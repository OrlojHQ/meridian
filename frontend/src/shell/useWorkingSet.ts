import { useQueries, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import type { Capsule, ChildRollup, Run, Thread } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import {
  capsuleSessions,
  describeCapsule,
  type CapsuleActivity,
  type CapsuleSession,
} from "./capsuleActivity";

export type WorkingSetItem = {
  capsule: Capsule;
  runs: Run[];
  threads: Thread[];
  activity: CapsuleActivity;
  sessions: CapsuleSession[];
  rollup?: ChildRollup;
};

export function useWorkingSet() {
  // Projects can be created elsewhere (the CLI, another tab), so the list
  // refreshes on the working set's cadence. Without it, a new Project's
  // Capsules never reach Needs you until the page reloads.
  const projects = useQuery({ ...queries.projects(), refetchInterval: 5_000 });
  const projectItems = useMemo(() => projects.data?.items ?? [], [projects.data]);
  const capsuleResults = useQueries({
    queries: projectItems.map((project) => queries.mainCapsules(project.id)),
  });
  // Subagents stay out of the working set unless they need the operator or
  // their lead is gone. One content-free board read lists those and every
  // lead's rollup, so hidden subagents add no per-Capsule polling.
  const board = useQuery(queries.board("attention"));
  const capsules = useMemo(() => {
    const surfaced = (board.data?.items ?? [])
      .filter((item) => item.capsule.role === "sub")
      .map((item) => item.capsule);
    const seen = new Set<string>();
    return [...capsuleResults.flatMap((result) => result.data?.items ?? []), ...surfaced].filter(
      (capsule) => {
        if (capsule.state === "Deleted" || seen.has(capsule.id)) return false;
        seen.add(capsule.id);
        return true;
      },
    );
  }, [capsuleResults, board.data]);
  const rollups = useMemo(
    () =>
      new Map(
        (board.data?.items ?? []).flatMap((item) =>
          item.childRollup ? [[item.capsule.id, item.childRollup] as const] : [],
        ),
      ),
    [board.data],
  );
  const runResults = useQueries({
    queries: capsules.map((capsule) => ({
      ...queries.runs(capsule.id),
      refetchInterval: 5_000,
    })),
  });
  // Thread summaries carry only a content-free awaiting flag; transcripts
  // are never fetched for the working set.
  const capabilities = useQuery(queries.capabilities());
  const threadResults = useQueries({
    queries: capsules.map((capsule) => ({
      ...queries.threads(capsule.id),
      enabled: capabilities.data?.structured === true,
      refetchInterval: 5_000,
    })),
  });
  const items = useMemo<WorkingSetItem[]>(
    () =>
      capsules.map((capsule, index) => {
        const runs = runResults[index]?.data?.items ?? [];
        const threads = threadResults[index]?.data?.items ?? [];
        return {
          capsule,
          runs,
          threads,
          activity: describeCapsule(capsule, runs, threads),
          sessions: capsuleSessions(capsule, runs, threads),
          rollup: rollups.get(capsule.id),
        };
      }),
    [capsules, runResults, threadResults, rollups],
  );
  const ready =
    !projects.isPending &&
    !board.isPending &&
    !capabilities.isPending &&
    capsuleResults.every((result) => !result.isPending) &&
    runResults.every((result) => !result.isPending) &&
    threadResults.every((result) => !result.isPending);
  return { projects, projectItems, capsuleResults, items, ready };
}

// Subagent Capsules render under their lead so a coordinated tree reads as one
// unit. A subagent whose lead is not visible stays top-level, never hidden.
export function nestSubagents(items: WorkingSetItem[]) {
  const leadCapsule = new Map<string, string>();
  for (const item of items) {
    for (const thread of item.threads) leadCapsule.set(thread.id, item.capsule.id);
  }
  const subagents = new Map<string, WorkingSetItem[]>();
  const roots: WorkingSetItem[] = [];
  for (const item of items) {
    const lead = item.capsule.role === "sub" && item.capsule.leadThreadId
      ? leadCapsule.get(item.capsule.leadThreadId)
      : undefined;
    if (lead && lead !== item.capsule.id) {
      subagents.set(lead, [...(subagents.get(lead) ?? []), item]);
    } else {
      roots.push(item);
    }
  }
  return roots.map((item) => ({ item, subagents: subagents.get(item.capsule.id) ?? [] }));
}
