import { client } from "./generated/client.gen";
import {
  archiveThread,
  cancelRun,
  cancelThread,
  createCapsulePreviewTicket,
  createRunAttachTicket,
  createThread,
  deleteCapsule,
  deleteThread,
  getCapabilities,
  getCapsule,
  getCapsuleGitDiff,
  getCapsuleGitStatus,
  getMoment,
  getRun,
  getThread,
  getTimeline,
  listCapsules,
  listCapsulePreviewPorts,
  listMoments,
  listProjects,
  listRunEvents,
  listRuns,
  listHarnessProfiles,
  listThreadBlocks,
  listThreads,
  pauseCapsule,
  resumeCapsule,
  resumeThread,
  sealCapsule,
  sendThreadMessage,
  startThread,
  respondThread,
} from "./generated/sdk.gen";
import type {
  AttachTicket,
  Capabilities,
  Capsule,
  CapsulePage,
  ErrorEnvelope,
  GitResult,
  HarnessProfilePage,
  Moment,
  MomentPage,
  ProjectPage,
  PreviewPortPage,
  PreviewTicket,
  Run,
  RunEventPage,
  RunPage,
  SealResult,
  TimelineView,
  Thread,
  ThreadBlockPage,
  ThreadMutationResult,
  ThreadPage,
  ThreadResponseRequest,
} from "./generated/types.gen";

const baseUrl =
  typeof window === "undefined" ? "http://127.0.0.1:8080" : window.location.origin;

client.setConfig({ baseUrl });

export class MeridianAPIError extends Error {
  readonly code: string;
  readonly status?: number;

  constructor(message: string, code = "request_failed", status?: number) {
    super(message);
    this.name = "MeridianAPIError";
    this.code = code;
    this.status = status;
  }
}

export function normalizeAPIError(value: unknown): MeridianAPIError {
  if (value instanceof MeridianAPIError) return value;
  if (value instanceof DOMException && value.name === "AbortError") {
    return new MeridianAPIError("Request cancelled", "cancelled");
  }
  if (typeof value === "object" && value !== null && "error" in value) {
    const envelope = value as ErrorEnvelope;
    if (envelope.error?.message) {
      const status =
        envelope.error.code === "transcript_locked"
          ? 423
          : envelope.error.code === "transcript_corrupt" ||
              envelope.error.code === "conflict"
            ? 409
            : envelope.error.code === "unsupported" ||
                envelope.error.code === "illegal_transition"
              ? 422
              : envelope.error.code === "not_found"
                ? 404
                : undefined;
      return new MeridianAPIError(envelope.error.message, envelope.error.code, status);
    }
  }
  if (value instanceof TypeError) {
    return new MeridianAPIError(
      navigator.onLine ? "Unable to reach Meridian" : "You are offline",
      "network_error",
    );
  }
  return new MeridianAPIError("The request could not be completed");
}

async function call<T>(
  operation: Promise<{ data: T; response: Response }>,
): Promise<T> {
  try {
    return (await operation).data;
  } catch (error) {
    throw normalizeAPIError(error);
  }
}

const generatedOptions = (signal?: AbortSignal) =>
  ({ signal, throwOnError: true as const });

const idempotencyHeaders = () => ({
  "Idempotency-Key": crypto.randomUUID(),
});

export const api = {
  baseUrl,
  capabilities: (signal?: AbortSignal) =>
    call<Capabilities>(getCapabilities(generatedOptions(signal))),
  projects: (signal?: AbortSignal) =>
    call<ProjectPage>(
      listProjects({ ...generatedOptions(signal), query: { limit: 100 } }),
    ),
  capsules: (projectId: string, signal?: AbortSignal) =>
    call<CapsulePage>(
      listCapsules({
        ...generatedOptions(signal),
        path: { projectId },
        query: { limit: 100 },
      }),
    ),
  capsule: (capsuleId: string, signal?: AbortSignal) =>
    call<Capsule>(
      getCapsule({ ...generatedOptions(signal), path: { capsuleId } }),
    ),
  runs: (capsuleId: string, signal?: AbortSignal) =>
    call<RunPage>(
      listRuns({
        ...generatedOptions(signal),
        path: { capsuleId },
        query: { limit: 100 },
      }),
    ),
  harnessProfiles: (capsuleId: string, signal?: AbortSignal) =>
    call<HarnessProfilePage>(
      listHarnessProfiles({
        ...generatedOptions(signal),
        path: { capsuleId },
      }),
    ),
  threads: (capsuleId: string, signal?: AbortSignal) =>
    call<ThreadPage>(
      listThreads({
        ...generatedOptions(signal),
        path: { capsuleId },
        query: { limit: 100 },
      }),
    ),
  thread: (threadId: string, signal?: AbortSignal) =>
    call<Thread>(
      getThread({ ...generatedOptions(signal), path: { threadId } }),
    ),
  threadBlocks: async (
    threadId: string,
    initialAfter = 0,
    signal?: AbortSignal,
  ): Promise<ThreadBlockPage> => {
    const maxBlocks = 400;
    const maxPages = 100;
    let after = initialAfter;
    let items: ThreadBlockPage["items"] = [];
    let gap: ThreadBlockPage["gap"];
    for (let pageNumber = 0; pageNumber < maxPages; pageNumber += 1) {
      const page = await call<ThreadBlockPage>(
        listThreadBlocks({
          ...generatedOptions(signal),
          path: { threadId },
          query: { after, limit: 100 },
        }),
      );
      items = [...items, ...page.items].slice(-maxBlocks);
      gap ??= page.gap;
      if (!page.more) {
        return { items, nextCursor: page.nextCursor, more: false, gap };
      }
      if (page.nextCursor <= after) {
        throw new MeridianAPIError(
          "Thread block cursor did not advance",
          "invalid_cursor",
        );
      }
      after = page.nextCursor;
    }
    return { items, nextCursor: after, more: true, gap };
  },
  createThread: (
    capsuleId: string,
    harness: string,
    firstMessage?: string,
    start = false,
    signal?: AbortSignal,
  ) =>
    call<ThreadMutationResult>(
      createThread({
        ...generatedOptions(signal),
        path: { capsuleId },
        headers: idempotencyHeaders(),
        body: {
          harness,
          ...(firstMessage === undefined ? {} : { firstMessage }),
          start,
        },
      }),
    ),
  threadSession: (
    action: "start" | "resume" | "cancel",
    threadId: string,
    expectedResourceVersion: number,
    signal?: AbortSignal,
  ) => {
    const options = {
      ...generatedOptions(signal),
      path: { threadId },
      headers: idempotencyHeaders(),
      body: { expectedResourceVersion },
    };
    const operation =
      action === "start"
        ? startThread(options)
        : action === "resume"
          ? resumeThread(options)
          : cancelThread(options);
    return call<ThreadMutationResult>(operation);
  },
  sendThreadMessage: (
    threadId: string,
    expectedResourceVersion: number,
    content: string,
    signal?: AbortSignal,
  ) =>
    call<ThreadMutationResult>(
      sendThreadMessage({
        ...generatedOptions(signal),
        path: { threadId },
        headers: idempotencyHeaders(),
        body: { expectedResourceVersion, content },
      }),
    ),
  respondThread: (
    threadId: string,
    body: ThreadResponseRequest,
    signal?: AbortSignal,
  ) =>
    call<ThreadMutationResult>(
      respondThread({
        ...generatedOptions(signal),
        path: { threadId },
        headers: idempotencyHeaders(),
        body,
      }),
    ),
  archiveThread: (
    threadId: string,
    expectedResourceVersion: number,
    signal?: AbortSignal,
  ) =>
    call<Thread>(
      archiveThread({
        ...generatedOptions(signal),
        path: { threadId },
        headers: idempotencyHeaders(),
        body: { expectedResourceVersion },
      }),
    ),
  deleteThread: (
    threadId: string,
    expectedResourceVersion: number,
    signal?: AbortSignal,
  ) =>
    call<Thread>(
      deleteThread({
        ...generatedOptions(signal),
        path: { threadId },
        headers: idempotencyHeaders(),
        body: {
          expectedResourceVersion,
          confirmation: "crypto-shred",
        },
      }),
    ),
  run: (runId: string, signal?: AbortSignal) =>
    call<Run>(getRun({ ...generatedOptions(signal), path: { runId } })),
  events: (runId: string, after = 0, signal?: AbortSignal) =>
    call<RunEventPage>(
      listRunEvents({
        ...generatedOptions(signal),
        path: { runId },
        query: { after, limit: 1000 },
      }),
    ),
  gitStatus: (capsuleId: string, signal?: AbortSignal) =>
    call<GitResult>(
      getCapsuleGitStatus({
        ...generatedOptions(signal),
        path: { capsuleId },
      }),
    ),
  gitDiff: (capsuleId: string, signal?: AbortSignal) =>
    call<GitResult>(
      getCapsuleGitDiff({
        ...generatedOptions(signal),
        path: { capsuleId },
      }),
    ),
  moments: (capsuleId: string, signal?: AbortSignal) =>
    call<MomentPage>(
      listMoments({
        ...generatedOptions(signal),
        path: { capsuleId },
        query: { limit: 100 },
      }),
    ),
  moment: (momentId: string, signal?: AbortSignal) =>
    call<Moment>(
      getMoment({ ...generatedOptions(signal), path: { momentId } }),
    ),
  timeline: (timelineId: string, signal?: AbortSignal) =>
    call<TimelineView>(
      getTimeline({ ...generatedOptions(signal), path: { timelineId } }),
    ),
  lifecycle: (
    action: "pause" | "resume" | "delete",
    capsuleId: string,
    expectedResourceVersion: number,
    signal?: AbortSignal,
  ) => {
    const options = {
      ...generatedOptions(signal),
      path: { capsuleId },
      headers: idempotencyHeaders(),
      body: { expectedResourceVersion },
    };
    const operation =
      action === "pause"
        ? pauseCapsule(options)
        : action === "resume"
          ? resumeCapsule(options)
          : deleteCapsule(options);
    return call<Capsule>(operation);
  },
  seal: (
    capsuleId: string,
    expectedResourceVersion: number,
    signal?: AbortSignal,
  ) =>
    call<SealResult>(
      sealCapsule({
        ...generatedOptions(signal),
        path: { capsuleId },
        headers: idempotencyHeaders(),
        body: { expectedResourceVersion },
      }),
    ),
  cancelRun: (
    runId: string,
    expectedResourceVersion: number,
    signal?: AbortSignal,
  ) =>
    call<Run>(
      cancelRun({
        ...generatedOptions(signal),
        path: { runId },
        headers: idempotencyHeaders(),
        body: { expectedResourceVersion },
      }),
    ),
  attachTicket: (runId: string, after: number, signal?: AbortSignal) =>
    call<AttachTicket>(
      createRunAttachTicket({
        ...generatedOptions(signal),
        path: { runId },
        query: { after },
      }),
    ),
  previewPorts: (capsuleId: string, signal?: AbortSignal) =>
    call<PreviewPortPage>(
      listCapsulePreviewPorts({
        ...generatedOptions(signal),
        path: { capsuleId },
      }),
    ),
  previewTicket: (
    capsuleId: string,
    port: number,
    signal?: AbortSignal,
  ) =>
    call<PreviewTicket>(
      createCapsulePreviewTicket({
        ...generatedOptions(signal),
        path: { capsuleId, port },
      }),
    ),
};
