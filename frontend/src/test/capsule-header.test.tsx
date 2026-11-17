import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { CapsuleDetail } from "../App";
import { api } from "../api/client";
import type { Capsule, ProfileDiagnostic, Thread } from "../api/generated/types.gen";

const project = {
  id: "project-1",
  name: "Project",
  createdAt: "2026-08-23T00:00:00Z",
  updatedAt: "2026-08-23T00:00:00Z",
  resourceVersion: 1,
};

const capsule: Capsule = {
  id: "capsule-1",
  projectId: project.id,
  timelineId: "timeline-1",
  name: "thread-ad942540f1e7",
  state: "Ready",
  desiredState: "Ready",
  restoreComplete: true,
  createdAt: "2026-08-23T00:00:00Z",
  updatedAt: "2026-08-23T00:00:00Z",
  resourceVersion: 2,
  harness: "opencode",
};

const thread: Thread = {
  id: "thread-1",
  capsuleId: capsule.id,
  state: "active",
  harness: "opencode",
  currentRunState: "Running",
  turn: "working",
  protocol: "meridian.adapter.v1",
  structuredSupported: true,
  encryptedAtRest: true,
  messageCount: 1,
  encryptedBytes: 40,
  createdAt: "2026-08-23T00:00:00Z",
  updatedAt: "2026-08-23T00:01:00Z",
  resourceVersion: 3,
};

const behind: ProfileDiagnostic = {
  capsuleId: capsule.id,
  nothingPinned: false,
  matchesProfile: false,
  harnesses: [],
  commitAuthor: { source: "none", name: "", email: "", profileName: "", profileEmail: "" },
};

const capabilities = {
  providerVersion: "docker/v1",
  isolation: "container" as const,
  attach: false,
  run: true,
  structured: true,
  git: true,
  pause: true,
  snapshot: false,
  clone: false,
  browse: false,
  delivery: true,
  preview: false,
  resourceMetrics: false,
};

function show(session = "thread-1") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/ui/capsules/capsule-1?session=${session}`]}>
        <Routes>
          <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function ready(options?: {
  name?: string;
  turn?: Thread["turn"];
  awaiting?: Thread["awaiting"];
  runState?: Thread["currentRunState"];
  delivery?: boolean;
  dirty?: boolean;
  branch?: string;
  pullRequest?: boolean;
  matches?: boolean;
}) {
  vi.spyOn(api, "capsule").mockResolvedValue({ ...capsule, name: options?.name ?? capsule.name });
  vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
  vi.spyOn(api, "threads").mockResolvedValue({
    items: [{
      ...thread,
      turn: options?.turn ?? "idle",
      awaiting: options?.awaiting,
      currentRunState: options?.runState ?? (options?.turn === "working" ? "Running" : "Succeeded"),
    }],
  });
  vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
  vi.spyOn(api, "harnessProfiles").mockResolvedValue({
    items: [{ name: "opencode-structured", structured: true, pty: false }],
  });
  vi.spyOn(api, "capabilities").mockResolvedValue({
    ...capabilities,
    delivery: options?.delivery ?? true,
    git: options?.delivery ?? true,
  });
  vi.spyOn(api, "threadBlocks").mockResolvedValue({
    items: [{
      id: "block-1",
      messageId: "message-1",
      messageSequence: 1,
      sequence: 1,
      role: "user",
      messageKind: "prompt",
      kind: "text",
      content: "Fix the header so the title can be read.",
      createdAt: capsule.createdAt,
    }],
    nextCursor: 1,
    more: false,
  });
  vi.spyOn(api, "deliveryInspection").mockResolvedValue({
    capsuleResourceVersion: capsule.resourceVersion,
    head: "a".repeat(40),
    tree: "b".repeat(40),
    branch: options?.branch ?? "feature/header",
    defaultBranch: "main",
    dirty: options?.dirty ?? true,
  });
  vi.spyOn(api, "gitDiff").mockResolvedValue({ content: "", truncated: false });
  vi.spyOn(api, "deliveries").mockResolvedValue({
    items: options?.pullRequest
      ? [{
          id: "delivery-1",
          capsuleId: capsule.id,
          projectId: project.id,
          state: "succeeded",
          action: "open_pull_request",
          proposedBy: "operator",
          approved: true,
          remoteBranch: "feature/header",
          destinationRef: "refs/heads/feature/header",
          resultPullRequestUrl: "https://github.com/OrlojHQ/meridian/pull/18",
          resultPullRequestNumber: 18,
          createdAt: capsule.createdAt,
          updatedAt: capsule.updatedAt,
          resourceVersion: 4,
        }]
      : [],
  });
  vi.spyOn(api, "capsuleProfileDiagnostic").mockResolvedValue({
    ...behind,
    matchesProfile: options?.matches ?? false,
  });
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Capsule header", () => {
  it("titles the thread from the first message and shows attention without an idle pill", async () => {
    ready({ turn: "working", runState: "Running" });
    show();
    expect(await screen.findByRole("heading", { name: "Fix the header so the title can be read" })).toBeInTheDocument();
    expect(screen.queryByText("thread-ad942540f1e7")).not.toBeInTheDocument();
    expect(document.querySelector(".header-status")).toHaveTextContent("Working");
    expect(screen.getByText("feature/header")).toBeInTheDocument();
    expect(screen.queryByText(/idle/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Profile…" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Ship…" })).not.toBeInTheDocument();
  });

  it("saves an edited title as the Capsule name", async () => {
    ready({ turn: "idle" });
    const rename = vi.spyOn(api, "renameCapsule").mockResolvedValue({ ...capsule, name: "Header layout", resourceVersion: 3 });
    show();
    await userEvent.click(await screen.findByRole("button", { name: "Fix the header so the title can be read" }));
    const input = screen.getByLabelText("Thread title");
    await userEvent.clear(input);
    await userEvent.type(input, "Header layout");
    input.blur();
    await waitFor(() => expect(rename).toHaveBeenCalledWith(capsule.id, "Header layout", capsule.resourceVersion));
  });

  it("offers to update only when the profile differs, from the menu and the note", async () => {
    ready({ matches: false, delivery: false });
    const update = vi.spyOn(api, "updateCapsuleProfile").mockResolvedValue({ ...behind, matchesProfile: true });
    show();
    expect(await screen.findByRole("button", { name: "Update?" })).toBeInTheDocument();
    expect(screen.getByText("Your setup changed.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Capsule actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Setup details" }));
    expect(await screen.findByRole("dialog", { name: "Setup details" })).toBeInTheDocument();
    expect(screen.getByText("This Capsule differs from your current profile:")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Update?" }));
    await waitFor(() => expect(update).toHaveBeenCalledWith(capsule.id));
  });

  it("hides the update note when the Capsule matches the profile", async () => {
    ready({ matches: true, delivery: false });
    show();
    await screen.findByRole("heading", { name: "Fix the header so the title can be read" });
    expect(screen.queryByText("Your setup changed.")).not.toBeInTheDocument();
  });

  it("hides Create PR when delivery is unsupported or there is nothing to send", async () => {
    ready({ delivery: false, dirty: false, branch: "main" });
    show();
    await screen.findByRole("heading", { name: "Fix the header so the title can be read" });
    expect(screen.queryByRole("button", { name: "Create PR" })).not.toBeInTheDocument();
  });

  it("opens a pull request sheet and becomes a link once a pull request exists", async () => {
    ready({ dirty: true, pullRequest: false });
    const { unmount } = show();
    await userEvent.click(await screen.findByRole("button", { name: "Create PR" }));
    const dialog = await screen.findByRole("dialog", { name: "Create pull request" });
    expect(dialog).toHaveTextContent("Fix the header so the title can be read");
    expect(dialog.querySelector("details")).not.toHaveAttribute("open");
    unmount();
    cleanup();
    vi.restoreAllMocks();
    ready({ dirty: true, pullRequest: true });
    show();
    const link = await screen.findByRole("link", { name: "PR #18" });
    expect(link).toHaveAttribute("href", "https://github.com/OrlojHQ/meridian/pull/18");
    expect(screen.getByRole("button", { name: "Push update" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create PR" })).not.toBeInTheDocument();
  });

  it("labels needs input and failed without an always-on status", async () => {
    ready({ awaiting: { kind: "input", since: capsule.updatedAt }, runState: "Running", turn: "idle", delivery: false });
    show();
    expect(await screen.findByText("Needs input")).toBeInTheDocument();
    expect(document.querySelector(".header-status")).toHaveTextContent("Needs input");
    cleanup();
    vi.restoreAllMocks();
    ready({ runState: "Failed", turn: "idle", delivery: false });
    show();
    await screen.findByRole("heading", { name: "Fix the header so the title can be read" });
    expect(document.querySelector(".header-status")).toHaveTextContent("Failed");
  });
});
