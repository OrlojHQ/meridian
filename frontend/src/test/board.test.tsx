import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { App } from "../App";
import { api } from "../api/client";
import type {
  BoardItem,
  Capabilities,
  Capsule,
  Project,
  Thread,
  ThreadLink,
} from "../api/generated/types.gen";
import { Board } from "../components/Board";
import { rollupSummary } from "../shell/capsuleActivity";

const project: Project = {
  id: "project-1",
  name: "Meridian",
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 1,
};

const capabilities: Capabilities = {
  providerVersion: "docker/v1",
  attach: false,
  run: false,
  structured: true,
  git: false,
  pause: false,
  snapshot: false,
  clone: false,
  preview: false,
  browse: false,
  delivery: false,
  resourceMetrics: false,
};

const capsule = (id: string, name: string, role: "main" | "sub"): Capsule => ({
  id,
  projectId: "project-1",
  timelineId: `timeline-${id}`,
  name,
  harness: "mock",
  role,
  state: "Ready",
  desiredState: "Ready",
  restoreComplete: true,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 1,
});

const thread = (id: string, capsuleId: string): Thread => ({
  id,
  capsuleId,
  state: "active",
  harness: "mock",
  encryptedAtRest: true,
  messageCount: 2,
  encryptedBytes: 64,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 2,
});

const link = (name: string, child: string): ThreadLink => ({
  id: `link-${name}`,
  grantId: "grant-1",
  parentThreadId: "thread-lead",
  parentCapsuleId: "capsule-lead",
  childThreadId: `thread-${child}`,
  childCapsuleId: `capsule-${child}`,
  childIntentId: `intent-${child}`,
  depth: 1,
  name,
  harness: "mock",
  state: "active",
  phase: "working",
  reportSeq: 0,
  reportDeliveredSeq: 0,
  messagesForwarded: 0,
  operatorMessages: 0,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 1,
});

const lead: BoardItem = {
  capsule: capsule("capsule-lead", "release lead", "main"),
  thread: thread("thread-lead", "capsule-lead"),
  status: "working",
  since: "2026-11-01T00:00:00Z",
  surfaced: false,
  childRollup: { total: 2, working: 1, idle: 0, done: 0, needsYou: 1, failed: 0, archived: 0 },
};

const surfaced: BoardItem = {
  capsule: capsule("capsule-tests", "tests capsule", "sub"),
  thread: thread("thread-tests", "capsule-tests"),
  status: "needs_you",
  needsYou: "permission",
  since: "2026-11-01T00:01:00Z",
  surfaced: true,
  link: link("tests", "tests"),
};

const quiet: BoardItem = {
  capsule: capsule("capsule-docs", "docs capsule", "sub"),
  thread: thread("thread-docs", "capsule-docs"),
  status: "working",
  since: "2026-11-01T00:02:00Z",
  surfaced: false,
  link: link("docs", "docs"),
};

const shipped: BoardItem = {
  capsule: capsule("capsule-ship", "shipped", "main"),
  status: "pr_open",
  since: "2026-11-01T00:03:00Z",
  surfaced: false,
  latestDelivery: {
    id: "delivery-1",
    state: "succeeded",
    action: "open_pull_request",
    pullRequestNumber: 7,
    pullRequestUrl: "https://github.com/example/repo/pull/7",
    updatedAt: "2026-11-01T00:03:00Z",
  },
};

function renderBoard(
  outlet?: { openNewProject: () => void; openLauncher: (value: Project) => void },
  extra: { git?: boolean; projects?: Project[] } = {},
) {
  vi.spyOn(api, "coordinationRequests").mockResolvedValue({ items: [] });
  vi.spyOn(api, "projects").mockResolvedValue({ items: extra.projects ?? [project] });
  vi.spyOn(api, "capabilities").mockResolvedValue({ ...capabilities, git: extra.git === true });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/"]}>
        <Routes>
          <Route path="/ui" element={<Outlet context={outlet} />}>
            <Route index element={<Board />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Board", () => {
  it("groups rows, surfaces a subagent that needs you and folds the rest", async () => {
    const board = vi.spyOn(api, "board").mockImplementation(async (expand) => ({
      truncated: false,
      items: expand === "all" ? [surfaced, lead, quiet, shipped] : [surfaced, lead, shipped],
    }));
    renderBoard();

    expect(await screen.findByRole("heading", { name: "1 needs attention" })).toBeTruthy();
    expect(screen.getByText("Inbox")).toBeTruthy();
    expect(screen.getByText("3 Capsules")).toBeTruthy();
    expect(within(screen.getByRole("region", { name: /Needs you/ })).getByText("Meridian")).toBeTruthy();
    const needsYou = screen.getByRole("region", { name: /Needs you/ });
    expect(within(needsYou).getByRole("link", { name: "tests" })).toBeTruthy();
    expect(within(needsYou).getByText("Waiting for permission")).toBeTruthy();
    expect(within(needsYou).getByRole("link", { name: "release lead" }).getAttribute("href")).toBe(
      "/ui/capsules/capsule-lead?session=thread-lead",
    );
    expect(within(needsYou).getByRole("link", { name: "Respond" })).toBeTruthy();

    const working = screen.getByRole("region", { name: /Working/ });
    expect(within(working).getByText("2 subagents")).toBeTruthy();
    expect(within(working).queryByRole("link", { name: "docs" })).toBeNull();
    await userEvent.click(await within(working).findByRole("button", { name: "Subagents (2)" }));
    const fold = within(working).getByRole("list", { name: "Subagents of release lead" });
    expect(within(fold).getByRole("link", { name: "docs" })).toBeTruthy();
    expect(within(fold).getByRole("link", { name: "tests" })).toBeTruthy();

    const pr = within(screen.getByRole("region", { name: /PR open/ })).getByRole("link", { name: "PR #7" });
    expect(pr.getAttribute("href")).toBe("https://github.com/example/repo/pull/7");
    expect(pr.getAttribute("rel")).toContain("noopener");

    await userEvent.click(screen.getByRole("checkbox", { name: "Show all subagents" }));
    expect(board).toHaveBeenCalledWith("all", expect.anything());
    expect(await within(screen.getByRole("region", { name: /Working/ })).findByText("subagent of")).toBeTruthy();
  });

  it("does not link a delivery URL that is not https", async () => {
    vi.spyOn(api, "board").mockResolvedValue({
      truncated: true,
      items: [{
        ...shipped,
        latestDelivery: { ...shipped.latestDelivery!, pullRequestUrl: "javascript:alert(1)" },
      }],
    });
    renderBoard();
    expect(await screen.findByText("Only the first 500 Capsules are shown.")).toBeTruthy();
    expect(screen.queryByRole("link", { name: /PR #/ })).toBeNull();
    expect(screen.getByText("Delivery succeeded")).toBeTruthy();
  });

  it("says why a failed row needs you, as bounded text", async () => {
    const failedRun = {
      id: "run-failed",
      capsuleId: "capsule-failed",
      harness: "mock",
      state: "Failed" as const,
      failure: `<img src=x onerror=alert(1)> structured adapter timed out ${"y".repeat(1_000)}`,
      createdAt: "2026-11-01T00:00:00Z",
      finishedAt: "2026-11-01T00:02:00Z",
      updatedAt: "2026-11-01T00:02:00Z",
      resourceVersion: 3,
    };
    vi.spyOn(api, "board").mockResolvedValue({
      truncated: false,
      items: [{
        capsule: capsule("capsule-failed", "flaky", "main"),
        thread: { ...thread("thread-failed", "capsule-failed"), currentRunState: "Failed" },
        currentRun: failedRun,
        status: "needs_you",
        needsYou: "failed",
        surfaced: false,
        since: "2026-11-01T00:02:00Z",
      }],
    });
    const view = renderBoard();
    const row = (await screen.findByRole("link", { name: "flaky" })).closest("li")!;
    expect(within(row).getByText("Failed")).toBeTruthy();
    const reason = within(row).getByText(/structured adapter timed out/);
    expect(reason.textContent!.startsWith("<img src=x onerror=alert(1)>")).toBe(true);
    expect(reason.textContent!.length).toBeLessThanOrEqual(280);
    expect(view.container.querySelector("img")).toBeNull();
    expect(within(row).getByRole("link", { name: "Open" })).toBeTruthy();
  });

  it("shows a proposed Delivery for operator approval and treats the text as hostile", async () => {
    const hostileTitle = `\u202eCRASH\u202c\u0007${"x".repeat(80)}`;
    const hostileBody = `<img src=x onerror=alert(1)>\n${"y".repeat(80)}`;
    vi.spyOn(api, "board").mockResolvedValue({
      truncated: false,
      items: [{
        capsule: capsule("capsule-lead", "release lead", "main"),
        thread: thread("thread-lead", "capsule-lead"),
        status: "needs_you",
        needsYou: "proposed_delivery",
        since: "2026-11-01T00:04:00Z",
        surfaced: false,
        latestDelivery: {
          id: "delivery-proposed",
          state: "proposed",
          action: "open_pull_request",
          updatedAt: "2026-11-01T00:04:00Z",
        },
      }],
    });
    vi.spyOn(api, "delivery").mockResolvedValue({
      id: "delivery-proposed",
      capsuleId: "capsule-lead",
      projectId: "project-1",
      state: "proposed",
      action: "open_pull_request",
      proposedBy: "agent",
      approved: false,
      remoteBranch: "feature/parser",
      destinationRef: "refs/heads/feature/parser",
      pullRequestTitle: hostileTitle,
      pullRequestBody: hostileBody,
      createdAt: "2026-11-01T00:04:00Z",
      updatedAt: "2026-11-01T00:04:00Z",
      resourceVersion: 1,
    });
    vi.spyOn(api, "deliveryInspection").mockResolvedValue({
      capsuleResourceVersion: 1,
      head: "a".repeat(40),
      tree: "b".repeat(40),
      branch: "feature/local",
      defaultBranch: "main",
      dirty: true,
    });
    const approve = vi.spyOn(api, "approveDelivery").mockResolvedValue({
      id: "delivery-proposed",
      capsuleId: "capsule-lead",
      projectId: "project-1",
      state: "queued",
      action: "open_pull_request",
      proposedBy: "agent",
      approved: true,
      remoteBranch: "feature/parser",
      destinationRef: "refs/heads/feature/parser",
      createdAt: "2026-11-01T00:04:00Z",
      updatedAt: "2026-11-01T00:04:00Z",
      resourceVersion: 2,
    });
    const view = renderBoard();
    expect(await screen.findByText("Waiting for Delivery approval")).toBeTruthy();
    const row = (await screen.findByRole("link", { name: "release lead" })).closest("li")!;
    expect(within(row).queryByRole("link", { name: "Respond" })).toBeNull();
    expect(view.container.querySelector("img")).toBeNull();
    expect(within(row).getByText(/CRASH/)).toBeTruthy();
    const approveButton = within(row).getByRole("button", { name: "Approve Delivery" });
    expect(approveButton).toBeDisabled();
    await userEvent.type(within(row).getByLabelText("Approve confirmation"), "approve");
    expect(approveButton).toBeEnabled();
    await userEvent.click(approveButton);
    await waitFor(() => expect(approve).toHaveBeenCalled());
  });

  it("summarizes a rollup as the lead's current subagents", () => {
    expect(
      rollupSummary({ total: 5, working: 1, idle: 0, done: 3, needsYou: 0, failed: 1, archived: 2 }),
    ).toBe("3 subagents");
    expect(rollupSummary({ total: 2, working: 1, idle: 0, done: 1, needsYou: 0, failed: 0, archived: 1 })).toBe(
      "1 subagent",
    );
    expect(rollupSummary({ total: 0, working: 0, idle: 0, done: 0, needsYou: 0, failed: 0, archived: 0 })).toBe(
      "0 subagents",
    );
  });

  it("folds archived subagents and offers promotion on a surfaced subagent", async () => {
    const archived: BoardItem = {
      ...quiet,
      status: "paused",
      link: { ...link("docs", "docs"), phase: "idle", archivedAt: "2026-11-02T00:00:00Z" },
    };
    vi.spyOn(api, "board").mockImplementation(async (expand) => ({
      truncated: false,
      items: expand === "all"
        ? [surfaced, { ...lead, childRollup: { ...lead.childRollup!, done: 1, working: 0, archived: 1 } }, archived]
        : [surfaced, { ...lead, childRollup: { ...lead.childRollup!, done: 1, working: 0, archived: 1 } }],
    }));
    const promote = vi.spyOn(api, "promoteCapsule").mockResolvedValue({ ...surfaced.capsule, role: "main" });
    renderBoard();

    const working = await screen.findByRole("region", { name: /Working/ });
    expect(within(working).getByText("1 subagent")).toBeTruthy();
    await userEvent.click(await within(working).findByRole("button", { name: "Subagents (2)" }));
    const fold = within(working).getByRole("list", { name: "Subagents of release lead" });
    expect(within(fold).getByText("Archived (1)")).toBeTruthy();
    const archivedLink = within(fold).getByRole("link", { name: "docs" });
    expect(archivedLink.closest("details")).not.toBeNull();
    expect(within(fold).getByRole("link", { name: "tests" }).closest("details")).toBeNull();

    const needsYou = screen.getByRole("region", { name: /Needs you/ });
    await userEvent.click(within(needsYou).getByRole("button", { name: "Promote to main…" }));
    const dialog = within(needsYou).getByRole("alertdialog", { name: "Promote to main" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Promote" }));
    expect(promote).toHaveBeenCalledWith(surfaced.capsule, false);
    expect(within(working).queryByRole("button", { name: /Promote/ })).toBeNull();
  });

  it("marks a promoted subagent in its own row and in its lead's fold", async () => {
    const promoted: BoardItem = {
      ...quiet,
      capsule: { ...quiet.capsule, role: "main", promotedAt: "2026-11-02T00:00:00Z" },
      status: "idle",
      link: { ...link("docs", "docs"), phase: "idle", promotedAt: "2026-11-02T00:00:00Z" },
    };
    vi.spyOn(api, "board").mockResolvedValue({ truncated: false, items: [lead, promoted] });
    renderBoard();

    const idle = await screen.findByRole("region", { name: /Idle/ });
    expect(within(idle).getByText("promoted from")).toBeTruthy();
    expect(within(idle).queryByText("subagent of")).toBeNull();
    expect(within(idle).queryByRole("button", { name: /Promote/ })).toBeNull();

    const working = screen.getByRole("region", { name: /Working/ });
    await userEvent.click(await within(working).findByRole("button", { name: "Subagents (1)" }));
    const fold = within(working).getByRole("list", { name: "Subagents of release lead" });
    expect(within(fold).getByRole("link", { name: "docs" })).toBeTruthy();
    expect(within(fold).getByText("Promoted")).toBeTruthy();
  });

  it("says when everything is caught up and still offers new work", async () => {
    vi.spyOn(api, "board").mockResolvedValue({ truncated: false, items: [lead] });
    const gitStatus = vi.spyOn(api, "gitStatus");
    const gitDiff = vi.spyOn(api, "gitDiff");
    const files = vi.spyOn(api, "workspaceFiles");
    const moments = vi.spyOn(api, "moments");
    const blocks = vi.spyOn(api, "threadBlocks");
    const openLauncher = vi.fn();
    const openNewProject = vi.fn();
    renderBoard({ openLauncher, openNewProject });

    expect(await screen.findByRole("heading", { name: "All caught up" })).toBeTruthy();
    expect(screen.getByText(/1 agent is still working/)).toBeTruthy();
    expect(screen.queryByRole("region", { name: /Needs you/ })).toBeNull();
    expect(screen.getByRole("region", { name: /Working/ })).toHaveTextContent("release lead");
    await userEvent.click(screen.getByRole("button", { name: "Start work in Meridian" }));
    expect(openLauncher).toHaveBeenCalledWith(project);
    await userEvent.click(screen.getByRole("button", { name: "New project" }));
    expect(openNewProject).toHaveBeenCalled();
    expect(gitStatus).not.toHaveBeenCalled();
    expect(gitDiff).not.toHaveBeenCalled();
    expect(files).not.toHaveBeenCalled();
    expect(moments).not.toHaveBeenCalled();
    expect(blocks).not.toHaveBeenCalled();
  });

  it("offers the diff of finished work only when the provider supports Git review", async () => {
    const review: BoardItem = {
      capsule: capsule("capsule-done", "Done", "main"),
      status: "ready_for_review",
      since: "2026-11-01T00:05:00Z",
      surfaced: false,
    };
    vi.spyOn(api, "board").mockResolvedValue({ truncated: false, items: [review] });
    renderBoard(undefined, { git: true });

    expect(await screen.findByRole("heading", { name: "All caught up" })).toBeTruthy();
    const lane = screen.getByRole("region", { name: /Ready for review/ });
    expect(within(lane).getByRole("link", { name: "Open diff" })).toHaveAttribute(
      "href",
      "/ui/capsules/capsule-done/diff",
    );
  });

  it("does not offer a diff when Git review is unavailable", async () => {
    vi.spyOn(api, "board").mockResolvedValue({
      truncated: false,
      items: [{
        capsule: capsule("capsule-done", "Done", "main"),
        status: "ready_for_review",
        since: "2026-11-01T00:05:00Z",
        surfaced: false,
      }],
    });
    renderBoard();
    expect(await screen.findByRole("region", { name: /Ready for review/ })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Open diff" })).toBeNull();
  });

  it("asks for a Project when the installation has none", async () => {
    vi.spyOn(api, "board").mockResolvedValue({ truncated: false, items: [] });
    const openNewProject = vi.fn();
    renderBoard({ openLauncher: vi.fn(), openNewProject }, { projects: [] });
    expect(await screen.findByRole("heading", { name: "No projects yet" })).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "New project" }));
    expect(openNewProject).toHaveBeenCalled();
  });

  it("is the home view and redirects /ui/board", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "threads").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
    vi.spyOn(api, "board").mockResolvedValue({ truncated: false, items: [surfaced, lead] });
    vi.spyOn(api, "coordinationRequests").mockResolvedValue({ items: [] });
    function Where() {
      const location = useLocation();
      return <p data-testid="location">{location.pathname}</p>;
    }
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/ui/board"]}>
          <App />
          <Where />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByTestId("location")).toHaveTextContent("/ui/");
    expect(await screen.findByRole("heading", { name: "Start something new" })).toBeTruthy();
    const views = screen.getByRole("navigation", { name: "Views" });
    expect(within(views).getAllByRole("link").map((link) => link.getAttribute("href"))).toEqual([
      "/ui/settings/harnesses",
    ]);
    expect(within(views).getByRole("link", { name: /Harness settings/ })).toBeTruthy();
    expect(within(views).queryByRole("link", { name: /^Board$/ })).toBeNull();
    expect(within(views).queryByRole("link", { name: /^Activity$/ })).toBeNull();
  });
});
