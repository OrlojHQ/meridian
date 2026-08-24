import { QueryClient, queryOptions } from "@tanstack/react-query";

import { MeridianAPIError, api } from "./client";

const retry = (failureCount: number, error: Error) =>
  failureCount < 2 &&
  (!(error instanceof MeridianAPIError) ||
    error.status === undefined ||
    error.status >= 500);

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry,
      staleTime: 2_000,
      refetchOnReconnect: true,
      refetchOnWindowFocus: true,
    },
    mutations: { retry: false },
  },
});

export const queries = {
  capabilities: () =>
    queryOptions({
      queryKey: ["capabilities"],
      queryFn: ({ signal }) => api.capabilities(signal),
      staleTime: 30_000,
    }),
  previewPorts: (capsuleId: string) =>
    queryOptions({
      queryKey: ["preview-ports", capsuleId],
      queryFn: ({ signal }) => api.previewPorts(capsuleId, signal),
      enabled: Boolean(capsuleId),
      refetchInterval: 5_000,
    }),
  projects: () =>
    queryOptions({
      queryKey: ["projects"],
      queryFn: ({ signal }) => api.projects(signal),
    }),
  capsules: (projectId: string) =>
    queryOptions({
      queryKey: ["capsules", projectId],
      queryFn: ({ signal }) => api.capsules(projectId, signal),
      enabled: Boolean(projectId),
      refetchInterval: 5_000,
    }),
  capsule: (capsuleId: string) =>
    queryOptions({
      queryKey: ["capsule", capsuleId],
      queryFn: ({ signal }) => api.capsule(capsuleId, signal),
      enabled: Boolean(capsuleId),
      refetchInterval: 3_000,
    }),
  runs: (capsuleId: string) =>
    queryOptions({
      queryKey: ["runs", capsuleId],
      queryFn: ({ signal }) => api.runs(capsuleId, signal),
      enabled: Boolean(capsuleId),
      refetchInterval: 3_000,
    }),
  run: (runId: string) =>
    queryOptions({
      queryKey: ["run", runId],
      queryFn: ({ signal }) => api.run(runId, signal),
      enabled: Boolean(runId),
      refetchInterval: 2_000,
    }),
  harnessProfiles: (capsuleId: string) =>
    queryOptions({
      queryKey: ["harness-profiles", capsuleId],
      queryFn: ({ signal }) => api.harnessProfiles(capsuleId, signal),
      enabled: Boolean(capsuleId),
      staleTime: 10_000,
    }),
  threads: (capsuleId: string) =>
    queryOptions({
      queryKey: ["threads", capsuleId],
      queryFn: ({ signal }) => api.threads(capsuleId, signal),
      enabled: Boolean(capsuleId),
      refetchInterval: 3_000,
    }),
  thread: (threadId: string) =>
    queryOptions({
      queryKey: ["thread", threadId],
      queryFn: ({ signal }) => api.thread(threadId, signal),
      enabled: Boolean(threadId),
      refetchInterval: 2_000,
    }),
  threadBlocks: (threadId: string) =>
    queryOptions({
      queryKey: ["thread-blocks", threadId],
      queryFn: ({ signal }) => api.threadBlocks(threadId, 0, signal),
      enabled: Boolean(threadId),
      refetchInterval: false,
    }),
  events: (runId: string) =>
    queryOptions({
      queryKey: ["run-events", runId],
      queryFn: ({ signal }) => api.events(runId, 0, signal),
      enabled: Boolean(runId),
      refetchInterval: 2_000,
    }),
  gitStatus: (capsuleId: string) =>
    queryOptions({
      queryKey: ["git-status", capsuleId],
      queryFn: ({ signal }) => api.gitStatus(capsuleId, signal),
      enabled: Boolean(capsuleId),
    }),
  gitDiff: (capsuleId: string) =>
    queryOptions({
      queryKey: ["git-diff", capsuleId],
      queryFn: ({ signal }) => api.gitDiff(capsuleId, signal),
      enabled: Boolean(capsuleId),
    }),
  workspaceFiles: (capsuleId: string, path = "") =>
    queryOptions({
      queryKey: ["workspace-files", capsuleId, path],
      queryFn: ({ signal }) => api.workspaceFiles(capsuleId, path, signal),
      enabled: Boolean(capsuleId),
    }),
  deliveryInspection: (capsuleId: string) =>
    queryOptions({
      queryKey: ["delivery-inspection", capsuleId],
      queryFn: ({ signal }) => api.deliveryInspection(capsuleId, signal),
      enabled: Boolean(capsuleId),
    }),
  moments: (capsuleId: string) =>
    queryOptions({
      queryKey: ["moments", capsuleId],
      queryFn: ({ signal }) => api.moments(capsuleId, signal),
      enabled: Boolean(capsuleId),
    }),
  moment: (momentId: string) =>
    queryOptions({
      queryKey: ["moment", momentId],
      queryFn: ({ signal }) => api.moment(momentId, signal),
      enabled: Boolean(momentId),
      staleTime: Infinity,
    }),
  timeline: (timelineId: string) =>
    queryOptions({
      queryKey: ["timeline", timelineId],
      queryFn: ({ signal }) => api.timeline(timelineId, signal),
      enabled: Boolean(timelineId),
      staleTime: 10_000,
    }),
};
