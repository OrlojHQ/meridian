import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Thread, ThreadBlock } from "../api/generated/types.gen";
import * as threadEvents from "../api/threadEvents";
import { ThreadDetail } from "../components/Threads";
import { buildThreadChat } from "../components/threadChat";
import {
  compactTokens,
  contextLabel,
  contextMeterUsage,
  contextUsage,
} from "../components/contextUsage";

const plain: Thread = {
  id: "thread-1",
  capsuleId: "capsule-1",
  state: "active",
  harness: "claude",
  currentRunId: "run-1",
  currentRunState: "Running",
  protocol: "meridian.adapter.v1",
  structuredSupported: true,
  encryptedAtRest: true,
  messageCount: 4,
  encryptedBytes: 120,
  createdAt: "2026-11-20T00:00:00Z",
  updatedAt: "2026-11-20T00:01:00Z",
  resourceVersion: 5,
};

const lead: Thread = { ...plain, coordinationGrantId: "grant-1", rootThreadId: plain.id, depth: 0 };

const compaction: ThreadBlock = {
  id: "block-compaction",
  messageId: "message-compaction",
  messageSequence: 3,
  sequence: 1,
  role: "system",
  messageKind: "status",
  kind: "json",
  event: { type: "context_usage", compacted: true },
  createdAt: "2026-11-20T00:01:00Z",
};

describe("context usage", () => {
  it("formats the window compactly", () => {
    expect([200000, 1000000, 1500000, 128000, 272000, 950].map(compactTokens)).toEqual([
      "200k", "1M", "1.5M", "128k", "272k", "950",
    ]);
    expect(contextLabel({ tokens: 124000, window: 200000 })).toBe("62% of 200k");
    // A harness can count past its window just before compacting.
    expect(contextLabel({ tokens: 210000, window: 200000 })).toBe("100% of 200k");
  });

  it("shows the meter on plain Threads from half full, and never on leads", () => {
    expect(contextMeterUsage({ ...plain, contextUsage: { tokens: 98000, window: 200000 } })).toBeUndefined();
    expect(contextMeterUsage({ ...plain, contextUsage: { tokens: 100000, window: 200000 } })).toBeDefined();
    expect(contextMeterUsage({ ...lead, contextUsage: { tokens: 190000, window: 200000 } })).toBeUndefined();
    // A subagent is not a lead; its operator can act on it too.
    expect(
      contextMeterUsage({ ...plain, coordinationGrantId: "grant-1", parentThreadId: "lead-1",
        contextUsage: { tokens: 150000, window: 200000 } }),
    ).toBeDefined();
    expect(contextMeterUsage(plain)).toBeUndefined();
    expect(contextUsage({ contextUsage: { tokens: 10, window: 0 } })).toBeUndefined();
  });

  it("turns a compaction marker into a divider and ignores anything else", () => {
    const chat = buildThreadChat(
      [compaction, { ...compaction, id: "block-other", event: { type: "context_usage" } }],
      "Running",
    );
    expect(chat.items).toEqual([{ kind: "compaction", key: "block-compaction" }]);
  });
});

describe("context usage in the Thread view", () => {
  beforeEach(() => {
    vi.spyOn(api, "harnessProfiles").mockResolvedValue({
      items: [{ name: "claude", structured: true, adapterKind: "claude",
        protocol: "meridian.adapter.v1", pty: false }],
    });
    vi.spyOn(api, "capsule").mockImplementation(() => new Promise(() => {}));
    vi.spyOn(api, "threadLinks").mockResolvedValue({ items: [] });
    vi.spyOn(threadEvents, "followThreadBlocks").mockResolvedValue();
  });
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  const show = (thread: Thread, blocks: ThreadBlock[] = []) => {
    vi.spyOn(api, "thread").mockResolvedValue(thread);
    vi.spyOn(api, "threadBlocks").mockResolvedValue({ items: blocks, nextCursor: blocks.length, more: false });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/ui/threads/thread-1"]}>
          <Routes>
            <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
  };

  it("puts a meter in a plain Thread's toolbar and the count in its details", async () => {
    show({ ...plain, contextUsage: { tokens: 124000, window: 200000 } });
    const meter = await screen.findByRole("meter", { name: "Context used" });
    expect(meter).toHaveTextContent("62% of 200k");
    expect(meter).toHaveAttribute("aria-valuenow", "62");
    expect(meter.closest(".thread-toolbar")).not.toBeNull();
    expect(meter.getAttribute("title")).toContain("Claude Code");
    await userEvent.click(screen.getByRole("button", { name: "Thread details" }));
    expect(screen.getByText("Context").nextElementSibling).toHaveTextContent(
      "62% of 200k (124,000 of 200,000 tokens)",
    );
  });

  it("hides the meter below half full and when the harness reports nothing", async () => {
    const view = show({ ...plain, contextUsage: { tokens: 40000, window: 200000 } });
    await screen.findByRole("button", { name: "Thread details" });
    expect(screen.queryByRole("meter")).not.toBeInTheDocument();
    view.unmount();
    show(plain);
    await userEvent.click(await screen.findByRole("button", { name: "Thread details" }));
    expect(screen.queryByRole("meter")).not.toBeInTheDocument();
    expect(screen.queryByText("Context")).not.toBeInTheDocument();
  });

  it("keeps a lead's fill in its details only", async () => {
    show({ ...lead, contextUsage: { tokens: 190000, window: 200000 } });
    await userEvent.click(await screen.findByRole("button", { name: "Thread details" }));
    expect(screen.queryByRole("meter")).not.toBeInTheDocument();
    expect(screen.getByText("Context").nextElementSibling).toHaveTextContent("95% of 200k");
  });

  it("marks where the harness compacted the conversation", async () => {
    show(lead, [compaction]);
    const transcript = await screen.findByRole("list", { name: "Thread transcript" });
    expect(
      within(transcript).getByRole("separator", { name: "Conversation compacted" }),
    ).toHaveTextContent("Earlier conversation was summarized by Claude Code");
  });
});
