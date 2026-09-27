import { useQueries, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import type { Capsule, Run, Thread } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { describeCapsule, type CapsuleActivity } from "./capsuleActivity";

export type WorkingSetItem = {
  capsule: Capsule;
  runs: Run[];
  threads: Thread[];
  activity: CapsuleActivity;
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
        };
      }),
    [capsules, runResults, threadResults],
  );
  return { projects, projectItems, capsuleResults, items };
}
