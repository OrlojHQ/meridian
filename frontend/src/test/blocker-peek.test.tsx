import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { MeridianAPIError, api } from "../api/client";
import type { BoardItem, Capsule, Thread, ThreadBlock } from "../api/generated/types.gen";
import { plainAgentText } from "../components/agentText";
import { Board } from "../components/Board";
import { openRequest, PEEK_SUMMARY_MAX } from "../components/BlockerPeek";
import { permissionOptionLabel } from "../components/ThreadConversation";
import { transcript } from "./threadFixtures";

const capsule = (id: string, name: string): Capsule => ({
  id,
  projectId: "project-1",
  timelineId: `timeline-${id}`,
  name,
  harness: "mock",
  role: "main",
  state: "Ready",
  desiredState: "Ready",
  restoreComplete: true,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 1,
});

const thread = (id: string, capsuleId: string, extra: Partial<Thread> = {}): Thread => ({
  id,
  capsuleId,
  state: "active",
  harness: "mock",
  encryptedAtRest: true,
  messageCount: 300,
  encryptedBytes: 64,
  currentRunState: "Running",
  awaiting: { kind: "permission", since: "2026-11-01T00:00:00Z" },
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 4,
  ...extra,
});

const row = (id: string, name: string, needsYou: "permission" | "input", since: string): BoardItem => ({
  capsule: capsule(`capsule-${id}`, name),
  thread: thread(`thread-${id}`, `capsule-${id}`, {
    awaiting: { kind: needsYou, since },
  }),
  status: "needs_you",
  needsYou,
  since,
  surfaced: false,
});

const first = row("tests", "fix tests", "permission", "2026-11-01T00:01:00Z");
const second = row("docs", "write docs", "input", "2026-11-01T00:02:00Z");

const hostile = `\u202eRun\u0007 rm -rf /tmp/x\n${"y".repeat(2_000)}`;

function blocksFor(threadId: string): ThreadBlock[] {
  const t = transcript(250);
  t.user("go");
  t.tool("call-1", "bash", "rm -rf /tmp/x");
  if (threadId === "thread-tests") t.permission("perm-1", hostile, ["allow", "deny"]);
  else t.input("input-1", "Which branch?");
  return t.blocks;
}

function renderWith(node: React.ReactNode) {
  vi.spyOn(api, "coordinationRequests").mockResolvedValue({ items: [] });
  vi.spyOn(api, "projects").mockResolvedValue({
    items: [{ id: "project-1", name: "Project", createdAt: "", updatedAt: "", resourceVersion: 1 }],
  });
  vi.spyOn(api, "capabilities").mockResolvedValue({
    providerVersion: "fake/v1",
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
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    ...render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/ui/"]}>{node}</MemoryRouter>
      </QueryClientProvider>,
    ),
  };
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Board blocker peek", () => {
  it("reads no transcript until a row is expanded, then answers in place", async () => {
    vi.spyOn(api, "board").mockResolvedValue({ truncated: false, items: [first, second] });
    const blocks = vi
      .spyOn(api, "threadBlocks")
      .mockImplementation(async (threadId) => ({ items: blocksFor(threadId), nextCursor: 300, more: false }));
    const fresh = vi
      .spyOn(api, "thread")
      .mockImplementation(async (threadId) => thread(threadId, `capsule-${threadId}`, { resourceVersion: 9 }));
    const respond = vi.spyOn(api, "respondThread").mockResolvedValue({
      thread: thread("thread-tests", "capsule-tests", { resourceVersion: 10 }),
    });
    const { client } = renderWith(<Board />);

    const needsYou = await screen.findByRole("region", { name: /Needs you/ });
    const [testsRow, docsRow] = within(needsYou).getAllByRole("listitem");
    expect(within(testsRow).getByText("Waiting for permission")).toBeInTheDocument();
    await client.refetchQueries({ queryKey: ["board"] });
    expect(blocks).not.toHaveBeenCalled();
    expect(fresh).not.toHaveBeenCalled();

    await userEvent.click(within(testsRow).getByRole("button", { name: "Peek at permission request" }));
    const card = await within(testsRow).findByRole("region", { name: "Permission required" });
    expect(blocks).toHaveBeenCalledTimes(1);
    expect(blocks).toHaveBeenCalledWith("thread-tests", 300 - 256, expect.anything());
    expect(fresh).toHaveBeenCalledWith("thread-tests", expect.anything());
    expect(within(card).getByText("shell")).toBeInTheDocument();
    const summary = card.querySelector(".chat-request-body")?.textContent ?? "";
    expect(summary.startsWith("Run rm -rf /tmp/x yyy")).toBe(true);
    expect(summary).not.toMatch(/[\u202e\u0007\n]/);
    expect(Array.from(summary)).toHaveLength(PEEK_SUMMARY_MAX);
    expect(summary.endsWith("…")).toBe(true);
    expect(client.getQueryCache().find({ queryKey: ["thread-blocks", "thread-tests"] })).toBeUndefined();

    await client.refetchQueries({ queryKey: ["board"] });
    expect(blocks).toHaveBeenCalledTimes(1);

    await userEvent.click(within(card).getByRole("button", { name: "Approve" }));
    await waitFor(() =>
      expect(respond).toHaveBeenCalledWith("thread-tests", {
        expectedResourceVersion: 9,
        responseTo: "perm-1",
        choice: "allow",
      }),
    );
    await waitFor(() =>
      expect(within(docsRow).getByRole("button", { name: "Peek at question" })).toHaveFocus(),
    );
    expect(within(testsRow).queryByRole("region", { name: "Permission required" })).toBeNull();
  });

  it("answers a question, reloads on a version conflict and closes on Escape", async () => {
    vi.spyOn(api, "board").mockResolvedValue({ truncated: false, items: [second] });
    const blocks = vi
      .spyOn(api, "threadBlocks")
      .mockImplementation(async (threadId) => ({ items: blocksFor(threadId), nextCursor: 300, more: false }));
    vi.spyOn(api, "thread").mockImplementation(async (threadId) => thread(threadId, "capsule-docs"));
    const respond = vi
      .spyOn(api, "respondThread")
      .mockRejectedValueOnce(new MeridianAPIError("Version changed", "conflict", 409));
    renderWith(<Board />);

    const toggle = await screen.findByRole("button", { name: "Peek at question" });
    await userEvent.click(toggle);
    const card = await screen.findByRole("region", { name: "Input required" });
    expect(within(card).getByText("Which branch?")).toBeInTheDocument();
    await userEvent.type(within(card).getByLabelText("Response"), "main");
    await userEvent.click(within(card).getByRole("button", { name: "Send response" }));
    await waitFor(() => expect(blocks).toHaveBeenCalledTimes(2));
    expect(respond).toHaveBeenCalledWith("thread-docs", {
      expectedResourceVersion: 4,
      responseTo: "input-1",
      input: "main",
    });

    await userEvent.click(await screen.findByLabelText("Response"));
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("region", { name: "Question" })).toBeNull();
    expect(toggle).toHaveFocus();
  });

  it("says so when the request was answered elsewhere", async () => {
    vi.spyOn(api, "board").mockResolvedValue({ truncated: false, items: [first] });
    const answered = transcript();
    answered.permission("perm-1", "Allow?");
    answered.permissionResponse("perm-1", "allow");
    vi.spyOn(api, "threadBlocks").mockResolvedValue({ items: answered.blocks, nextCursor: 2, more: false });
    vi.spyOn(api, "thread").mockResolvedValue(thread("thread-tests", "capsule-tests"));
    renderWith(<Board />);
    await userEvent.click(await screen.findByRole("button", { name: "Peek at permission request" }));
    expect(await screen.findByText(/Nothing is waiting any more/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the Thread" })).toHaveAttribute(
      "href",
      "/ui/capsules/capsule-tests?session=thread-tests",
    );
  });

  it("offers no peek for a failed row", async () => {
    vi.spyOn(api, "board").mockResolvedValue({
      truncated: false,
      items: [{ ...first, needsYou: "failed", thread: thread("thread-tests", "capsule-tests", { awaiting: undefined }) }],
    });
    renderWith(<Board />);
    expect(await screen.findByText("Failed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Peek/ })).toBeNull();
  });
});

describe("Agent text", () => {
  it("flattens, strips control and bidi characters, and caps", () => {
    expect(plainAgentText("a\u202eb\u2066c\u200fd\u0000e\r\nf\u2028g", 80)).toBe("abcd e f g");
    expect(plainAgentText("abcdef", 4)).toBe("abc…");
    expect(plainAgentText(undefined, 4)).toBe("");
    expect(permissionOptionLabel(`allow_\u202e${"x".repeat(100)}`)).toHaveLength(48);
    expect(permissionOptionLabel("allow_always")).toBe("Allow always");
  });

  it("finds only an open request on a live session", () => {
    const t = transcript();
    t.permission("perm-1", "first");
    t.permissionResponse("perm-1", "allow");
    t.input("input-1", "second");
    expect(openRequest(t.blocks, "Running")?.id).toBe("input-1");
    expect(openRequest(t.blocks, "Failed")).toBeUndefined();
  });
});
