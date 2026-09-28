import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MeridianAPIError, api } from "../api/client";
import type {
  Capsule,
  Thread,
  ThreadBlock,
} from "../api/generated/types.gen";
import * as threadEvents from "../api/threadEvents";
import { CapsuleDetail } from "../App";
import {
  ThreadCreate,
  ThreadDetail,
  ThreadFleet,
  mergeThreadBlocks,
  sanitizeThreadText,
} from "../components/Threads";

const capsule: Capsule = {
  id: "capsule-1",
  projectId: "project-1",
  timelineId: "timeline-1",
  name: "Workspace",
  state: "Ready",
  desiredState: "Ready",
  restoreComplete: true,
  createdAt: "2026-08-23T00:00:00Z",
  updatedAt: "2026-08-23T00:00:00Z",
  resourceVersion: 2,
};

const thread: Thread = {
  id: "thread-1",
  capsuleId: capsule.id,
  state: "active",
  harness: "mock",
  currentRunId: "run-1",
  currentRunState: "Running",
  protocol: "meridian.adapter.v1",
  structuredSupported: true,
  encryptedAtRest: true,
  messageCount: 4,
  encryptedBytes: 120,
  createdAt: "2026-08-23T00:00:00Z",
  updatedAt: "2026-08-23T00:01:00Z",
  resourceVersion: 5,
};

const block = (
  id: string,
  messageId: string,
  messageSequence: number,
  content: string,
  type?: NonNullable<ThreadBlock["event"]>["type"],
): ThreadBlock => ({
  id,
  messageId,
  messageSequence,
  sequence: 1,
  role: "assistant",
  messageKind: "response",
  kind: "text",
  content,
  event: type ? { type, messageId } : undefined,
  createdAt: "2026-08-23T00:01:00Z",
});

function wrapper(node: React.ReactNode, initial = "/ui/threads/thread-1") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[initial]}>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Thread routes and creation", () => {
  it("renders an empty fleet and retained Thread route", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({
      items: [{
        id: "project-1",
        name: "Project",
        createdAt: capsule.createdAt,
        updatedAt: capsule.updatedAt,
        resourceVersion: 1,
      }],
    });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [capsule] });
    vi.spyOn(api, "threads").mockResolvedValue({ items: [] });
    const empty = wrapper(<ThreadFleet />, "/ui/threads");
    expect(await screen.findByText("No Threads yet")).toBeInTheDocument();
    empty.unmount();

    vi.mocked(api.threads).mockResolvedValue({ items: [thread] });
    wrapper(<ThreadFleet />, "/ui/threads");
    expect(await screen.findByRole("link", { name: thread.harness })).toHaveAttribute(
      "href",
      "/ui/threads/thread-1",
    );
  });

  it("creates and atomically starts a first task", async () => {
    vi.spyOn(api, "harnessProfiles").mockResolvedValue({
      items: [
        {
          name: "mock",
          structured: true,
          adapterKind: "mock",
          protocol: "meridian.adapter.v1",
          pty: false,
        },
        { name: "shell", structured: false, pty: true },
      ],
    });
    const create = vi.spyOn(api, "createThread").mockResolvedValue({ thread });
    const created = vi.fn();
    const user = userEvent.setup();
    wrapper(<ThreadCreate capsule={capsule} onCreated={created} />);
    await user.type(await screen.findByLabelText("First task (optional)"), "fix tests");
    await user.click(screen.getByRole("button", { name: "Create Thread and start" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        capsule.id,
        "mock",
        "fix tests",
        true,
      ),
    );
    expect(created).toHaveBeenCalledWith(thread);
  });
});

describe("Thread detail transcript and controls", () => {
  beforeEach(() => {
    vi.spyOn(api, "harnessProfiles").mockResolvedValue({
      items: [{
        name: "mock",
        structured: true,
        adapterKind: "mock",
        protocol: "meridian.adapter.v1",
        pty: false,
      }],
    });
  });

  it("collapses deltas into finals, escapes blocks, sends, and answers permissions", async () => {
    const blocks: ThreadBlock[] = [
      block("delta", "assistant-1", 1, "partial", "assistant_delta"),
      block("final", "assistant-1", 2, "final <script>x</script>", "assistant_message"),
      {
        ...block("tool", "tool-1", 3, "", "tool_start"),
        event: {
          type: "tool_start",
          toolName: "<b>shell</b>",
          summary: "safe summary",
        },
      },
      {
        ...block("permission", "permission-1", 4, "", "permission_request"),
        event: {
          type: "permission_request",
          messageId: "permission-1",
          permission: {
            kind: "filesystem",
            summary: "Allow write?",
            options: ["allow", "deny"],
          },
        },
      },
      {
        ...block("unknown", "future-1", 5, "<img src=x>", undefined),
        event: { type: "future_event" as "status" },
      },
      {
        ...block("status", "status-1", 6, "", "status"),
        event: { type: "status", status: "waiting" },
      },
      {
        ...block("error", "error-1", 7, "", "error"),
        event: {
          type: "error",
          code: "adapter_failed",
          reason: "bad\u001b[31mthing",
        },
      },
    ];
    vi.spyOn(api, "thread").mockResolvedValue(thread);
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "threadBlocks").mockResolvedValue({
      items: blocks,
      nextCursor: 5,
      more: false,
    });
    vi.spyOn(threadEvents, "followThreadBlocks").mockImplementation(
      async (_id, _cursor, signal, _onBlock, onStatus) => {
        onStatus("live");
        await new Promise<void>((resolve) =>
          signal.addEventListener("abort", () => resolve(), { once: true }),
        );
      },
    );
    const send = vi.spyOn(api, "sendThreadMessage").mockResolvedValue({ thread });
    const respond = vi.spyOn(api, "respondThread").mockResolvedValue({ thread });
    const session = vi.spyOn(api, "threadSession").mockResolvedValue({ thread });
    const archive = vi.spyOn(api, "archiveThread").mockResolvedValue({
      ...thread,
      state: "archived",
    });
    const user = userEvent.setup();
    const view = wrapper(
      <Routes>
        <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
      </Routes>,
    );

    expect(await screen.findByText("final <script>x</script>")).toBeInTheDocument();
    expect(screen.queryByText("partial")).not.toBeInTheDocument();
    expect(view.container.querySelector("script")).toBeNull();
    expect(screen.getByText("Tool started · <b>shell</b>")).toBeInTheDocument();
    expect(screen.getByText("Unknown block event · future_event")).toBeInTheDocument();
    expect(screen.getByText("waiting")).toBeInTheDocument();
    expect(screen.getByText("bad[31mthing")).toBeInTheDocument();
    expect(view.container.textContent).not.toMatch(/[\u001b\u009b]/);
    expect(await screen.findByRole("link", { name: capsule.name })).toHaveAttribute(
      "href",
      "/ui/capsules/capsule-1?session=thread-1",
    );
    expect(screen.getByRole("link", { name: "Open terminal" })).toHaveAttribute(
      "href",
      "/ui/capsules/capsule-1#terminal",
    );
    await user.click(screen.getByRole("button", { name: "Cancel session" }));
    expect(session).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Confirm cancel" }));
    await waitFor(() =>
      expect(session).toHaveBeenCalledWith("cancel", thread.id, thread.resourceVersion),
    );
    await user.click(screen.getByRole("button", { name: "Archive" }));
    expect(archive).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Confirm archive" }));
    await waitFor(() =>
      expect(archive).toHaveBeenCalledWith(thread.id, thread.resourceVersion),
    );

    await user.click(screen.getByRole("button", { name: "allow" }));
    await waitFor(() =>
      expect(respond).toHaveBeenCalledWith(
        thread.id,
        expect.objectContaining({
          responseTo: "permission-1",
          choice: "allow",
        }),
      ),
    );
    const composer = screen.getByLabelText("Message");
    await user.type(composer, "next task{Enter}");
    await waitFor(() =>
      expect(send).toHaveBeenCalledWith(thread.id, thread.resourceVersion, "next task"),
    );

    const abort = vi.spyOn(AbortController.prototype, "abort");
    view.unmount();
    expect(abort).toHaveBeenCalled();
  });

  it("names the agent and Capsule in plain words", async () => {
    vi.spyOn(api, "thread").mockResolvedValue({ ...thread, harness: "claude" });
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "threadBlocks").mockResolvedValue({
      items: [],
      nextCursor: 0,
      more: false,
    });
    vi.spyOn(threadEvents, "followThreadBlocks").mockImplementation(
      async (_id, _cursor, signal, _onBlock, onStatus) => {
        onStatus("live");
        await new Promise<void>((resolve) =>
          signal.addEventListener("abort", () => resolve(), { once: true }),
        );
      },
    );
    const view = wrapper(
      <Routes>
        <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
      </Routes>,
    );

    expect(await screen.findByRole("heading", { level: 1, name: "Claude Code" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Workspace" })).toHaveAttribute(
      "href",
      "/ui/capsules/capsule-1?session=thread-1",
    );
    const header = view.container.querySelector("header")!;
    expect(header).toHaveTextContent("The transcript is encrypted at rest.");
    expect(header).not.toHaveTextContent(capsule.id);
    expect(header).not.toHaveTextContent(thread.id);
    expect(view.container).not.toHaveTextContent(/Structured Thread|Retained encrypted/);
    expect(screen.getByText("Live")).toHaveAttribute("title", "Cursor 0");
    expect(screen.getByRole("button", { name: "Crypto-shred" })).toBeInTheDocument();
  });

  it("falls back to a generic Capsule link until the Capsule loads", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(thread);
    vi.spyOn(api, "capsule").mockImplementation(() => new Promise(() => {}));
    vi.spyOn(api, "threadBlocks").mockResolvedValue({
      items: [],
      nextCursor: 0,
      more: false,
    });
    vi.spyOn(threadEvents, "followThreadBlocks").mockResolvedValue();
    wrapper(
      <Routes>
        <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
      </Routes>,
    );
    expect(await screen.findByRole("link", { name: "its Capsule" })).toHaveAttribute(
      "href",
      "/ui/capsules/capsule-1?session=thread-1",
    );
    expect(screen.getByRole("heading", { level: 1, name: "Mock (test)" })).toBeInTheDocument();
  });

  it("resumes a paused retained Thread", async () => {
    const paused = {
      ...thread,
      state: "paused" as const,
      currentRunId: undefined,
      currentRunState: undefined,
    };
    vi.spyOn(api, "thread").mockResolvedValue(paused);
    vi.spyOn(api, "threadBlocks").mockResolvedValue({
      items: [],
      nextCursor: 0,
      more: false,
    });
    vi.spyOn(threadEvents, "followThreadBlocks").mockResolvedValue();
    const session = vi.spyOn(api, "threadSession").mockResolvedValue({ thread: paused });
    const user = userEvent.setup();
    wrapper(
      <Routes>
        <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
      </Routes>,
    );
    await user.click(await screen.findByRole("button", { name: "Resume session" }));
    await waitFor(() =>
      expect(session).toHaveBeenCalledWith("resume", thread.id, thread.resourceVersion),
    );
  });

  it("opens Capsule sessions as tabs with the Thread embedded", async () => {
    const second = {
      ...thread,
      id: "thread-2",
      currentRunState: undefined,
      createdAt: "2026-08-23T00:02:00Z",
    };
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "projects").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "fake/v1",
      attach: false,
      run: false,
      structured: true,
      git: false,
      pause: false,
      snapshot: false,
      clone: false,
      browse: false,
      delivery: false,
      preview: false,
      resourceMetrics: false,
    });
    vi.spyOn(api, "threads").mockResolvedValue({ items: [thread, second] });
    vi.spyOn(api, "thread").mockImplementation(async (id) =>
      id === second.id ? second : thread,
    );
    const blocks = vi.spyOn(api, "threadBlocks").mockResolvedValue({
      items: [],
      nextCursor: 0,
      more: false,
    });
    vi.spyOn(threadEvents, "followThreadBlocks").mockResolvedValue();
    const user = userEvent.setup();
    wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      `/ui/capsules/${capsule.id}`,
    );

    const working = await screen.findByRole("tab", { name: /Mock \(test\)\s*Working/ });
    expect(working).toHaveAttribute("aria-selected", "true");
    await waitFor(() => expect(blocks.mock.calls.map((call) => call[0])).toContain(thread.id));
    expect(await screen.findByRole("heading", { name: "Transcript" })).toBeInTheDocument();
    expect(screen.queryByText("Structured Thread")).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: /Mock \(test\)\s*Idle/ }));
    await waitFor(() => expect(blocks.mock.calls.map((call) => call[0])).toContain(second.id));
    expect(screen.getByRole("tab", { name: /Mock \(test\)\s*Idle/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  it("answers a secret input request without exposing the value", async () => {
    const inputRequest: ThreadBlock = {
      ...block("input", "input-1", 1, "", "input_request"),
      event: {
        type: "input_request",
        messageId: "input-1",
        input: { prompt: "Token", secret: true },
      },
    };
    vi.spyOn(api, "thread").mockResolvedValue(thread);
    vi.spyOn(api, "threadBlocks").mockResolvedValue({
      items: [inputRequest],
      nextCursor: 1,
      more: false,
    });
    vi.spyOn(threadEvents, "followThreadBlocks").mockResolvedValue();
    const respond = vi.spyOn(api, "respondThread").mockResolvedValue({ thread });
    const user = userEvent.setup();
    wrapper(
      <Routes>
        <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
      </Routes>,
    );
    const response = await screen.findByLabelText("Response");
    expect(response).toHaveAttribute("type", "password");
    await user.type(response, "sensitive-value");
    await user.click(screen.getByRole("button", { name: "Send response" }));
    await waitFor(() =>
      expect(respond).toHaveBeenCalledWith(
        thread.id,
        expect.objectContaining({
          responseTo: "input-1",
          input: "sensitive-value",
        }),
      ),
    );
    expect(screen.queryByText("sensitive-value")).not.toBeInTheDocument();
  });

  it("shows locked transcript guidance without sensitive details", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(thread);
    vi.spyOn(api, "threadBlocks").mockRejectedValue(
      new MeridianAPIError("secret key id abc", "transcript_locked", 423),
    );
    wrapper(
      <Routes>
        <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
      </Routes>,
    );
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Encrypted transcript locked");
    expect(alert).not.toHaveTextContent("abc");
  });

  it("shows corrupt transcript recovery without ciphertext details", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(thread);
    vi.spyOn(api, "threadBlocks").mockRejectedValue(
      new MeridianAPIError(
        "ciphertext nonce secret-detail",
        "transcript_corrupt",
        409,
      ),
    );
    wrapper(
      <Routes>
        <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
      </Routes>,
    );
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Encrypted transcript corrupt");
    expect(alert).not.toHaveTextContent("secret-detail");
  });

  it("requires strong crypto-shred confirmation", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(thread);
    vi.spyOn(api, "threadBlocks").mockResolvedValue({
      items: [],
      nextCursor: 0,
      more: false,
    });
    vi.spyOn(threadEvents, "followThreadBlocks").mockResolvedValue();
    const deletion = vi.spyOn(api, "deleteThread").mockResolvedValue({
      ...thread,
      state: "deleted",
    });
    const user = userEvent.setup();
    wrapper(
      <Routes>
        <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
      </Routes>,
    );
    await user.click(await screen.findByRole("button", { name: "Crypto-shred" }));
    const confirm = screen.getByRole("button", { name: "Delete key permanently" });
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText("Confirmation"), "crypto-shred");
    await user.click(confirm);
    await waitFor(() =>
      expect(deletion).toHaveBeenCalledWith(thread.id, thread.resourceVersion),
    );
  });

  it("refreshes authoritative Thread state after a send conflict", async () => {
    vi.spyOn(api, "thread").mockResolvedValue(thread);
    vi.spyOn(api, "threadBlocks").mockResolvedValue({
      items: [],
      nextCursor: 0,
      more: false,
    });
    vi.spyOn(threadEvents, "followThreadBlocks").mockResolvedValue();
    const send = vi
      .spyOn(api, "sendThreadMessage")
      .mockRejectedValue(new MeridianAPIError("stale", "conflict", 409));
    const user = userEvent.setup();
    wrapper(
      <Routes>
        <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
      </Routes>,
    );

    await user.type(await screen.findByLabelText("Message"), "stale task{Enter}");

    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(api.thread).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole("alert")).toHaveTextContent("stale");
  });
});

it("deduplicates and bounds the React Query transcript representation", () => {
  const first = block("same", "message", 1, "a", "assistant_delta");
  const many = Array.from({ length: 450 }, (_, index) =>
    block(`block-${index}`, `message-${index}`, index + 2, "x", "status"),
  );
  const merged = mergeThreadBlocks([first], [first, ...many]);
  expect(merged).toHaveLength(400);
  expect(new Set(merged.map((item) => item.id)).size).toBe(400);
});

it("removes terminal control characters while preserving safe text layout", () => {
  expect(sanitizeThreadText("one\u001b[31m\ntwo\u009b0m\tthree")).toBe(
    "one[31m\ntwo0m\tthree",
  );
});

it("does not persist or log Thread content", async () => {
  vi.spyOn(api, "harnessProfiles").mockResolvedValue({
    items: [{
      name: "mock",
      structured: true,
      adapterKind: "mock",
      protocol: "meridian.adapter.v1",
      pty: false,
    }],
  });
  const local = vi.spyOn(Storage.prototype, "setItem");
  const historyWrite = vi.spyOn(window.history, "pushState");
  const log = vi.spyOn(console, "log").mockImplementation(() => undefined);
  wrapper(<ThreadCreate capsule={capsule} />);
  const input = await screen.findByLabelText("First task (optional)");
  await userEvent.setup().type(input, "content-never-persisted");
  expect(local).not.toHaveBeenCalled();
  expect(historyWrite).not.toHaveBeenCalled();
  expect(window.location.href).not.toContain("content-never-persisted");
  expect(log).not.toHaveBeenCalled();
});
