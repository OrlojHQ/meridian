import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { BoardItem, Capsule, Thread, ThreadLink } from "../api/generated/types.gen";
import { Board } from "../components/Board";
import { rollupSummary } from "../shell/capsuleActivity";

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

function renderBoard() {
  vi.spyOn(api, "coordinationRequests").mockResolvedValue({ items: [] });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/board"]}>
        <Board />
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

    expect(await screen.findByRole("heading", { name: "1 needs you" })).toBeTruthy();
    const needsYou = screen.getByRole("region", { name: /Needs you/ });
    expect(within(needsYou).getByRole("link", { name: "tests" })).toBeTruthy();
    expect(within(needsYou).getByText("Waiting for permission")).toBeTruthy();
    expect(within(needsYou).getByRole("link", { name: "release lead" }).getAttribute("href")).toBe(
      "/ui/capsules/capsule-lead?session=thread-lead",
    );
    expect(within(needsYou).getByRole("link", { name: "Respond" })).toBeTruthy();

    const working = screen.getByRole("region", { name: /Working/ });
    expect(within(working).getByText("0/2 done, 1 needs you, 1 working")).toBeTruthy();
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

  it("summarizes a rollup", () => {
    expect(
      rollupSummary({ total: 5, working: 1, idle: 0, done: 3, needsYou: 0, failed: 1, archived: 2 }),
    ).toBe("3/5 done, 1 working, 1 failed, 2 archived");
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
    expect(within(working).getByText("1/2 done, 1 needs you, 1 archived")).toBeTruthy();
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
});
