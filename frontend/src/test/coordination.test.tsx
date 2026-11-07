import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type {
  Capabilities,
  Capsule,
  CoordinationGrant,
  Project,
  Thread,
  ThreadLink,
} from "../api/generated/types.gen";
import { StructuredLauncher } from "../components/StructuredLauncher";
import { ThreadHelpers } from "../components/ThreadHelpers";
import { buildThreadChat } from "../components/threadChat";
import { nestHelpers, type WorkingSetItem } from "../shell/useWorkingSet";

const project: Project = {
  id: "project-1",
  name: "Project",
  harnessImages: [{ name: "mock", imageReference: "ghcr.io/orlojhq/meridian-capsule-mock:v1" }],
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 1,
};

const capabilities = (coordination: boolean): Capabilities => ({
  providerVersion: "docker/v2",
  attach: true,
  run: true,
  structured: true,
  git: false,
  pause: true,
  snapshot: false,
  clone: false,
  preview: false,
  browse: false,
  delivery: false,
  resourceMetrics: false,
  coordination: { supported: coordination, maxDepth: 1, maxDescendants: 32 },
});

const leadThread: Thread = {
  id: "thread-lead",
  capsuleId: "capsule-lead",
  state: "active",
  harness: "mock",
  encryptedAtRest: true,
  messageCount: 3,
  encryptedBytes: 256,
  coordinationGrantId: "grant-1",
  rootThreadId: "thread-lead",
  depth: 0,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 4,
};

const grant: CoordinationGrant = {
  id: "grant-1",
  projectId: project.id,
  rootThreadId: leadThread.id,
  rootCapsuleId: leadThread.capsuleId,
  state: "active",
  spawnApproval: "auto",
  maxDepth: 1,
  maxChildren: 4,
  maxDescendants: 8,
  maxLiveCapsules: 4,
  maxMessagesPerLink: 50,
  allowedHarnesses: ["mock"],
  deadlineAt: "2026-11-01T08:00:00Z",
  onDeadline: "cancel_and_pause",
  descendantsCreated: 2,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 3,
};

const link = (name: string, phase: ThreadLink["phase"], reportSeq: number): ThreadLink => ({
  id: `link-${name}`,
  grantId: grant.id,
  parentThreadId: leadThread.id,
  parentCapsuleId: leadThread.capsuleId,
  childThreadId: `thread-${name}`,
  childCapsuleId: `capsule-${name}`,
  childIntentId: `intent-${name}`,
  depth: 1,
  name,
  harness: "mock",
  state: "active",
  phase,
  reportSeq,
  reportDeliveredSeq: 0,
  messagesForwarded: 0,
  operatorMessages: 0,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 1,
});

function wrapper(node: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/"]}>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Lead launcher", () => {
  const intent = {
    id: "intent-1",
    projectId: project.id,
    capsuleId: "capsule-lead",
    capsuleName: "lead",
    threadId: leadThread.id,
    runId: "run-1",
    messageId: "message-1",
    harness: "mock",
    state: "provisioning" as const,
    createdAt: "2026-11-01T00:00:00Z",
    updatedAt: "2026-11-01T00:00:00Z",
    resourceVersion: 1,
  };

  it("sends a bounded coordination policy only when the operator opts in", async () => {
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities(true));
    const spawn = vi.spyOn(api, "createProjectThread").mockResolvedValue(intent);
    wrapper(<StructuredLauncher project={project} harness="mock" />);
    await userEvent.click(await screen.findByLabelText("Lead with helpers"));
    const helpers = screen.getByLabelText("Helpers");
    await userEvent.clear(helpers);
    await userEvent.type(helpers, "2");
    await userEvent.type(screen.getByLabelText("Task"), "split the work");
    await userEvent.click(screen.getByRole("button", { name: "Start task" }));
    await waitFor(() =>
      expect(spawn).toHaveBeenCalledWith(project.id, "mock", "split the work", undefined, {
        maxChildren: 2,
        deadlineSeconds: 8 * 3600,
      }),
    );
  });

  it("hides the lead option when the installation cannot coordinate", async () => {
    const loaded = vi.spyOn(api, "capabilities").mockResolvedValue(capabilities(false));
    wrapper(<StructuredLauncher project={project} harness="mock" />);
    await waitFor(() => expect(loaded).toHaveBeenCalled());
    expect(screen.queryByLabelText("Lead with helpers")).not.toBeInTheDocument();
  });
});

describe("Helpers panel", () => {
  it("lists helpers with their phase and stops the tree on confirmation", async () => {
    vi.spyOn(api, "coordinationGrant").mockResolvedValue(grant);
    vi.spyOn(api, "threadLinks").mockResolvedValue({
      items: [link("helper-1", "idle", 1), link("helper-2", "awaiting_operator", 0)],
    });
    const revoke = vi
      .spyOn(api, "revokeCoordinationGrant")
      .mockResolvedValue({ ...grant, state: "revoked", revokeReason: "operator" });
    wrapper(<ThreadHelpers thread={leadThread} />);
    const panel = await screen.findByRole("region", { name: "Helpers" });
    const first = await within(panel).findByRole("link", { name: "helper-1" });
    expect(first).toHaveAttribute("href", "/ui/capsules/capsule-helper-1?session=thread-helper-1");
    expect(within(panel).getByText("Reported")).toBeInTheDocument();
    expect(within(panel).getByText("Needs you")).toBeInTheDocument();
    expect(within(panel).getByText("2 of 8 started", { exact: false })).toBeInTheDocument();
    await userEvent.click(within(panel).getByRole("button", { name: "Stop helpers" }));
    await userEvent.click(within(panel).getByRole("button", { name: "Confirm stop" }));
    await waitFor(() => expect(revoke).toHaveBeenCalledWith(grant));
  });

  it("names the lead on a helper Thread and renders nothing for plain Threads", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(leadThread);
    const helper: Thread = {
      ...leadThread,
      id: "thread-helper-1",
      capsuleId: "capsule-helper-1",
      parentThreadId: leadThread.id,
      linkId: "link-helper-1",
      depth: 1,
    };
    const { unmount } = wrapper(<ThreadHelpers thread={helper} />);
    expect(await screen.findByRole("link", { name: "Open the lead" })).toHaveAttribute(
      "href",
      "/ui/capsules/capsule-lead?session=thread-lead",
    );
    unmount();
    const plain = wrapper(
      <ThreadHelpers thread={{ ...leadThread, coordinationGrantId: undefined }} />,
    );
    expect(plain.container).toBeEmptyDOMElement();
  });
});

describe("Coordination transcript origin", () => {
  it("marks Meridian-written prompts instead of presenting them as the operator's", () => {
    const { items } = buildThreadChat([
      {
        id: "block-1",
        messageId: "message-1",
        messageSequence: 1,
        sequence: 1,
        role: "user",
        messageKind: "prompt",
        kind: "text",
        content: "[Meridian coordination] helper-1 reported",
        origin: { source: "coordination", kind: "from_helpers", linkIds: ["link-helper-1"] },
        createdAt: "2026-11-01T00:00:00Z",
      },
    ]);
    expect(items[0]).toMatchObject({ kind: "user", origin: "from_helpers" });
  });
});

describe("Sidebar nesting", () => {
  const item = (capsule: Partial<Capsule> & { id: string }, threads: Thread[] = []): WorkingSetItem => ({
    capsule: {
      projectId: project.id,
      timelineId: `timeline-${capsule.id}`,
      name: capsule.id,
      state: "Ready",
      desiredState: "Ready",
      restoreComplete: true,
      createdAt: "2026-11-01T00:00:00Z",
      updatedAt: "2026-11-01T00:00:00Z",
      resourceVersion: 1,
      ...capsule,
    },
    runs: [],
    threads,
    activity: { group: "working", label: "Working", agent: "mock" },
    sessions: [],
  });

  it("nests helpers under their lead and keeps orphans visible", () => {
    const lead = item({ id: "capsule-lead", role: "main" }, [leadThread]);
    const helper = item({ id: "capsule-helper-1", role: "sub", leadThreadId: leadThread.id });
    const orphan = item({ id: "capsule-orphan", role: "sub", leadThreadId: "thread-gone" });
    const plain = item({ id: "capsule-plain" });
    const nested = nestHelpers([helper, lead, orphan, plain]);
    expect(nested.map((entry) => entry.item.capsule.id)).toEqual([
      "capsule-lead",
      "capsule-orphan",
      "capsule-plain",
    ]);
    expect(nested[0].helpers.map((entry) => entry.capsule.id)).toEqual(["capsule-helper-1"]);
  });
});
