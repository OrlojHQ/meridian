import { QueryClient, queryOptions } from "@tanstack/react-query";

import { MeridianAPIError, api, type BoardExpand } from "./client";

const retry = (failureCount: number, error: Error) =>
  failureCount < 2 &&
  (!(error instanceof MeridianAPIError) ||
    error.status === undefined ||
    error.status >= 500);

// Every request goes to the Meridian gateway, usually on loopback, so the
// browser's internet reachability says nothing about whether it will succeed.
// The default "online" network mode pauses queries and mutations after an
// `offline` event; a paused send leaves the composer disabled and the cached
// page looks live. Always run requests and let a real failure surface instead.
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      networkMode: "always",
      retry,
      staleTime: 2_000,
      refetchOnReconnect: true,
      refetchOnWindowFocus: true,
    },
    mutations: { networkMode: "always", retry: false },
  },
});

export const queries = {
  authenticationMode: () =>
    queryOptions({
      queryKey: ["authentication-mode"],
      queryFn: ({ signal }) => api.authenticationMode(signal),
      staleTime: 30_000,
    }),
  currentMember: () =>
    queryOptions({
      queryKey: ["current-member"],
      queryFn: ({ signal }) => api.currentMember(signal),
    }),
  personalAccessTokens: () =>
    queryOptions({
      queryKey: ["personal-access-tokens"],
      queryFn: ({ signal }) => api.personalAccessTokens(signal),
    }),
  sessions: () =>
    queryOptions({
      queryKey: ["sessions"],
      queryFn: ({ signal }) => api.sessions(signal),
    }),
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
  mainCapsules: (projectId: string) =>
    queryOptions({
      queryKey: ["capsules", projectId, "main"],
      queryFn: ({ signal }) => api.capsules(projectId, signal, "main"),
      enabled: Boolean(projectId),
      refetchInterval: 5_000,
    }),
  capsule: (capsuleId: string) =>
    queryOptions({
      queryKey: ["capsule", capsuleId],
      queryFn: ({ signal }) => api.capsule(capsuleId, signal),
      enabled: Boolean(capsuleId),
      refetchInterval: (query) =>
        query.state.data?.state === "Creating" || query.state.data?.state === "Preparing"
          ? 1_000
          : 3_000,
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
  board: (expand: BoardExpand = "attention") =>
    queryOptions({
      queryKey: ["board", expand],
      queryFn: ({ signal }) => api.board(expand, signal),
      refetchInterval: 3_000,
    }),
  threadLinks: (threadId: string) =>
    queryOptions({
      queryKey: ["thread-links", threadId],
      queryFn: ({ signal }) => api.threadLinks(threadId, signal),
      enabled: Boolean(threadId),
      refetchInterval: 3_000,
    }),
  coordinationGrant: (grantId: string) =>
    queryOptions({
      queryKey: ["coordination-grant", grantId],
      queryFn: ({ signal }) => api.coordinationGrant(grantId, signal),
      enabled: Boolean(grantId),
      refetchInterval: 5_000,
    }),
  coordinationRequests: () =>
    queryOptions({
      queryKey: ["coordination-requests", "pending_approval"],
      queryFn: ({ signal }) => api.coordinationRequests(signal),
      refetchInterval: 3_000,
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
  // Reading the workspace counts as Capsule activity, so listings and file
  // content refresh only when the operator asks, never on focus or reconnect.
  workspaceFiles: (capsuleId: string, path = "") =>
    queryOptions({
      queryKey: ["workspace-files", capsuleId, path],
      queryFn: ({ signal }) => api.workspaceFiles(capsuleId, path, signal),
      enabled: Boolean(capsuleId),
      staleTime: Infinity,
      refetchOnWindowFocus: false,
      refetchOnReconnect: false,
    }),
  workspaceFile: (capsuleId: string, path: string) =>
    queryOptions({
      queryKey: ["workspace-file", capsuleId, path],
      queryFn: ({ signal }) => api.workspaceFile(capsuleId, path, signal),
      enabled: Boolean(capsuleId && path),
      staleTime: Infinity,
      refetchOnWindowFocus: false,
      refetchOnReconnect: false,
    }),
  deliveryInspection: (capsuleId: string) =>
    queryOptions({
      queryKey: ["delivery-inspection", capsuleId],
      queryFn: ({ signal }) => api.deliveryInspection(capsuleId, signal),
      enabled: Boolean(capsuleId),
    }),
  delivery: (deliveryId: string) =>
    queryOptions({
      queryKey: ["delivery", deliveryId],
      queryFn: ({ signal }) => api.delivery(deliveryId, signal),
      enabled: Boolean(deliveryId),
      refetchInterval: 3_000,
    }),
  deliveries: (capsuleId: string) =>
    queryOptions({
      queryKey: ["deliveries", capsuleId],
      queryFn: ({ signal }) => api.deliveries(capsuleId, signal),
      enabled: Boolean(capsuleId),
      refetchInterval: 3_000,
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
