import { client } from "./generated/client.gen";
import {
  getProjectEnvironment, updateProjectEnvironment,
  getProjectHarnessSetup, setProjectHarnessSetup,
  listProviderConnections, createProviderConnection, updateProviderConnection, getProjectProviderConnection, grantProjectProviderConnection,
  getHarnessSetupContents, listHarnessSetups, mutateHarnessSetup, listHarnessSetupRevisions, previewHarnessSetup, importHarnessSetup,
  archiveThread,
  cancelRun,
  cancelThread,
  createBrowserSession,
  createCapsule,
  createCapsuleDelivery,
  createCapsulePreviewTicket,
  createProject,
  createProjectThread,
  createRunAttachTicket,
  createThread,
  deleteCapsule,
  deleteThread,
  getCapabilities,
  getCapsule,
  getCapsuleGitDiff,
  getCapsuleGitStatus,
  getProject,
  patchProject,
  inspectCapsuleDelivery,
  getMoment,
  getRun,
  getThread,
  getTimeline,
  listCapsules,
  listCapsulePreviewPorts,
  listCapsuleFiles,
  listMoments,
  listProjects,
  listRunEvents,
  listRuns,
  listHarnessProfiles,
  listThreadBlocks,
  listThreads,
  pauseCapsule,
  resumeCapsule, retryCapsule, getProjectThreadIntent,
  resumeThread,
  sealCapsule,
  sendThreadMessage,
  startRun,
  startThread,
  respondThread,
  readCapsuleFile,
} from "./generated/sdk.gen";
import type { HarnessSetupUpload, HarnessSetupPreview, ImportHarnessSetupRequest, EnvironmentMutation, ProjectEnvironment,
  ProjectHarnessSetup,
  ProviderConnection, ProviderConnectionPage, ProviderConnectionRequestWritable, ProjectProviderConnection,
  HarnessSetup, HarnessSetupPage, HarnessSetupRevisionPage, MutateHarnessSetupRequest,
  AttachTicket,
  Capabilities,
  Capsule,
  CapsulePage,
  CreateProjectRequest,
  HarnessImage,
  ErrorEnvelope,
  GitResult,
  HarnessProfilePage,
  Moment,
  MomentPage,
  Project,
  ProjectPage,
  ProjectThreadIntent,
  PreviewPortPage,
  PreviewTicket,
  Run,
  RunEventPage,
  RunPage,
  SealResult,
  TimelineView,
  CreateDeliveryRequest,
  Delivery,
  DeliveryInspection,
  Thread,
  ThreadBlockPage,
  ThreadMutationResult,
  ThreadPage,
  ThreadResponseRequest,
  WorkspaceFile,
  WorkspaceFilePage,
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
                : envelope.error.code === "unauthorized"
                  ? 401
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
 projectHarnessSetup:(projectId:string,harness:string)=>call<ProjectHarnessSetup>(getProjectHarnessSetup({...generatedOptions(),path:{projectId,harness}})),
 setProjectHarnessSetup:(projectId:string,harness:string,setup:string)=>call<ProjectHarnessSetup>(setProjectHarnessSetup({...generatedOptions(),path:{projectId,harness},body:{setup}})),
 providerConnections:()=>call<ProviderConnectionPage>(listProviderConnections({...generatedOptions()})),
 saveProviderConnection:(body:ProviderConnectionRequestWritable,id?:string)=>call<ProviderConnection>(id ? updateProviderConnection({...generatedOptions(),path:{connectionId:id},body}) : createProviderConnection({...generatedOptions(),body})),
 projectConnection:(projectId:string,harness:string)=>call<ProjectProviderConnection>(getProjectProviderConnection({...generatedOptions(),path:{projectId,harness}})),
 grantConnection:(projectId:string,harness:string,connectionId:string)=>call<ProjectProviderConnection>(grantProjectProviderConnection({...generatedOptions(),path:{projectId,harness},body:{connectionId}})),
 harnessSetups: () => call<HarnessSetupPage>(listHarnessSetups({...generatedOptions()})),
 harnessSetupContents: (setupId: string) => call<ImportHarnessSetupRequest>(getHarnessSetupContents({...generatedOptions(), path:{setupId}})),
 harnessSetupRevisions: (setupId: string) => call<HarnessSetupRevisionPage>(listHarnessSetupRevisions({...generatedOptions(), path:{setupId}})),
 mutateHarnessSetup: (setupId:string, body:MutateHarnessSetupRequest) => call<HarnessSetup>(mutateHarnessSetup({...generatedOptions(),path:{setupId},headers:idempotencyHeaders(),body})),
  baseUrl,
  authenticateBrowser: (token: string, signal?: AbortSignal) =>
    call<void>(
      createBrowserSession({
        ...generatedOptions(signal),
        auth: token,
      }),
    ),
  capabilities: (signal?: AbortSignal) =>
    call<Capabilities>(getCapabilities(generatedOptions(signal))),
  projects: (signal?: AbortSignal) =>
    call<ProjectPage>(
      listProjects({ ...generatedOptions(signal), query: { limit: 100 } }),
    ),
  createProject: (request: CreateProjectRequest, signal?: AbortSignal) =>
    call<Project>(
      createProject({
        ...generatedOptions(signal),
        headers: idempotencyHeaders(),
        body: request,
      }),
    ),
  // Adds one installation-known harness pack to the Project allowlist,
  // replacing a pack of the same name, against the current resource version.
  applyProjectHarness: async (projectId: string, pack: HarnessImage) => {
    const current = await call<Project>(
      getProject({ ...generatedOptions(), path: { projectId } }),
    );
    const images = (current.harnessImages ?? []).filter(
      (item) => item.name !== pack.name,
    );
    return call<Project>(
      patchProject({
        ...generatedOptions(),
        path: { projectId },
        body: {
          expectedResourceVersion: current.resourceVersion,
          harnessImages: [...images, pack],
        },
      }),
    );
  },
  capsules: (projectId: string, signal?: AbortSignal) =>
    call<CapsulePage>(
      listCapsules({
        ...generatedOptions(signal),
        path: { projectId },
        query: { limit: 100 },
      }),
    ),
  createCapsule: (
    projectId: string,
    name: string,
    harness: string,
    signal?: AbortSignal,
    setup?: string,
  ) =>
    call<Capsule>(
      createCapsule({
        ...generatedOptions(signal),
        path: { projectId },
        headers: idempotencyHeaders(),
        body: { name, harness, ...(setup ? {setup} : {}) },
      }),
    ),
  createProjectThread: (
    projectId: string,
    harness: string,
    prompt: string,
    name?: string,
    signal?: AbortSignal,
  ) =>
    call<ProjectThreadIntent>(
      createProjectThread({
        ...generatedOptions(signal),
        path: { projectId },
        headers: idempotencyHeaders(),
        body: { harness, prompt, ...(name ? { name } : {}) },
      }),
    ),
  previewHarnessSetup: (body: HarnessSetupUpload) => call<HarnessSetupPreview>(previewHarnessSetup({...generatedOptions(), body})),
  importHarnessSetup: (body: ImportHarnessSetupRequest, key?: string) => call<HarnessSetup>(importHarnessSetup({...generatedOptions(), headers: key ? {"Idempotency-Key":key} : idempotencyHeaders(), body})),
  projectEnvironment: (projectId: string, signal?: AbortSignal) => call<ProjectEnvironment>(getProjectEnvironment({...generatedOptions(signal), path: {projectId}})),
  updateProjectEnvironment: (projectId: string, body: EnvironmentMutation) => call<ProjectEnvironment>(updateProjectEnvironment({...generatedOptions(), path: {projectId}, headers: idempotencyHeaders(), body})),
  projectThreadIntent: (intentId: string, signal?: AbortSignal) => call<ProjectThreadIntent>(getProjectThreadIntent({...generatedOptions(signal), path: {intentId}})),
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
  workspaceFiles: (capsuleId: string, path = "", signal?: AbortSignal) =>
    call<WorkspaceFilePage>(
      listCapsuleFiles({
        ...generatedOptions(signal),
        path: { capsuleId },
        query: { path, limit: 256 },
      }),
    ),
  workspaceFile: (capsuleId: string, path: string, signal?: AbortSignal) =>
    call<WorkspaceFile>(
      readCapsuleFile({
        ...generatedOptions(signal),
        path: { capsuleId },
        query: { path },
      }),
    ),
  deliveryInspection: (capsuleId: string, signal?: AbortSignal) =>
    call<DeliveryInspection>(
      inspectCapsuleDelivery({
        ...generatedOptions(signal),
        path: { capsuleId },
      }),
    ),
  createDelivery: (
    capsuleId: string,
    body: CreateDeliveryRequest,
    signal?: AbortSignal,
  ) =>
    call<Delivery>(
      createCapsuleDelivery({
        ...generatedOptions(signal),
        path: { capsuleId },
        headers: idempotencyHeaders(),
        body,
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
    action: "pause" | "resume" | "delete" | "retry",
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
          : action === "retry" ? retryCapsule(options) : deleteCapsule(options);
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
  startRun: (
    capsuleId: string,
    harness: string,
    signal?: AbortSignal,
  ) =>
    call<Run>(
      startRun({
        ...generatedOptions(signal),
        path: { capsuleId },
        headers: idempotencyHeaders(),
        body: { harness, prompt: "" },
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
