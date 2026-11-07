import { useQueries, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import type { Capsule, Run, Thread } from "../api/generated/types.gen";
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
};

export function useWorkingSet() {
  const projects = useQuery(queries.projects());
  const projectItems = useMemo(() => projects.data?.items ?? [], [projects.data]);
  const capsuleResults = useQueries({
    queries: projectItems.map((project) => queries.capsules(project.id)),
  });
  const capsules = useMemo(
    () =>
      capsuleResults
        .flatMap((result) => result.data?.items ?? [])
        .filter((capsule) => capsule.state !== "Deleted"),
    [capsuleResults],
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
        };
      }),
    [capsules, runResults, threadResults],
  );
  return { projects, projectItems, capsuleResults, items };
}

// Helper Capsules render under their lead so a coordinated tree reads as one
// unit. A helper whose lead is not visible stays top-level, never hidden.
export function nestHelpers(items: WorkingSetItem[]) {
  const leadCapsule = new Map<string, string>();
  for (const item of items) {
    for (const thread of item.threads) leadCapsule.set(thread.id, item.capsule.id);
  }
  const helpers = new Map<string, WorkingSetItem[]>();
  const roots: WorkingSetItem[] = [];
  for (const item of items) {
    const lead = item.capsule.role === "sub" && item.capsule.leadThreadId
      ? leadCapsule.get(item.capsule.leadThreadId)
      : undefined;
    if (lead && lead !== item.capsule.id) {
      helpers.set(lead, [...(helpers.get(lead) ?? []), item]);
    } else {
      roots.push(item);
    }
  }
  return roots.map((item) => ({ item, helpers: helpers.get(item.capsule.id) ?? [] }));
}
