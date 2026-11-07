import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { BoardItem, Capsule, Thread, ThreadLink } from "../api/generated/types.gen";
import { Board, rollupSummary } from "../components/Board";

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
  childRollup: { total: 2, working: 1, idle: 0, done: 0, needsYou: 1, failed: 0 },
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
  it("groups rows, surfaces a helper that needs you and folds the rest", async () => {
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
    await userEvent.click(await within(working).findByRole("button", { name: "Helpers (2)" }));
    const fold = within(working).getByRole("list", { name: "Helpers of release lead" });
    expect(within(fold).getByRole("link", { name: "docs" })).toBeTruthy();
    expect(within(fold).getByRole("link", { name: "tests" })).toBeTruthy();

    const pr = within(screen.getByRole("region", { name: /PR open/ })).getByRole("link", { name: "PR #7" });
    expect(pr.getAttribute("href")).toBe("https://github.com/example/repo/pull/7");
    expect(pr.getAttribute("rel")).toContain("noopener");

    await userEvent.click(screen.getByRole("checkbox", { name: "Show all helpers" }));
    expect(board).toHaveBeenCalledWith("all", expect.anything());
    expect(await within(screen.getByRole("region", { name: /Working/ })).findByText("helper of")).toBeTruthy();
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

  it("summarizes a rollup", () => {
    expect(rollupSummary({ total: 5, working: 1, idle: 0, done: 3, needsYou: 0, failed: 1 })).toBe(
      "3/5 done, 1 working, 1 failed",
    );
  });
});
