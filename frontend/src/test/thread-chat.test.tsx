import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Thread, ThreadBlock } from "../api/generated/types.gen";
import * as threadEvents from "../api/threadEvents";
import {
  filterSlashEntries,
  latestThreadCommands,
  slashQuery,
} from "../components/slashCommands";
import { buildThreadChat } from "../components/threadChat";
import { safeLinkTarget, ThreadMarkdown } from "../components/ThreadMarkdown";
import { ThreadDetail } from "../components/Threads";
import { reportedScreenshotTranscript, transcript } from "./threadFixtures";

const thread: Thread = {
  id: "thread-1",
  capsuleId: "capsule-1",
  state: "active",
  harness: "mock",
  currentRunId: "run-1",
  currentRunState: "Running",
  protocol: "meridian.adapter.v1",
  structuredSupported: true,
  encryptedAtRest: true,
  messageCount: 4,
  encryptedBytes: 120,
  createdAt: "2026-11-07T00:00:00Z",
  updatedAt: "2026-11-07T00:01:00Z",
  resourceVersion: 5,
};

let pushBlock: (block: ThreadBlock) => void = () => undefined;

function renderThread(blocks: ThreadBlock[], value: Thread = thread) {
  vi.spyOn(api, "thread").mockResolvedValue(value);
  vi.spyOn(api, "threadBlocks").mockResolvedValue({
    items: blocks,
    nextCursor: blocks.at(-1)?.messageSequence ?? 0,
    more: false,
  });
  vi.spyOn(threadEvents, "followThreadBlocks").mockImplementation(
    async (_id, _cursor, signal, onBlock, onStatus) => {
      pushBlock = onBlock;
      onStatus("live");
      await new Promise<void>((resolve) =>
        signal.addEventListener("abort", () => resolve(), { once: true }),
      );
    },
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/threads/thread-1"]}>
        <Routes>
          <Route path="/ui/threads/:threadId" element={<ThreadDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.spyOn(api, "harnessProfiles").mockResolvedValue({
    items: [{ name: "mock", structured: true, adapterKind: "mock", protocol: "meridian.adapter.v1", pty: false }],
  });
  vi.spyOn(api, "capsule").mockImplementation(() => new Promise(() => {}));
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Thread chat model", () => {
  it("turns the reported transcript into plain chat with a working state", () => {
    const chat = buildThreadChat(reportedScreenshotTranscript(), "Running");
    expect(chat.items.map((item) => item.kind)).toEqual(["assistant", "user"]);
    expect(chat.items[0]).toMatchObject({ text: "Hello World!", streaming: false });
    expect(chat.activity).toBe("working");
    expect(buildThreadChat(reportedScreenshotTranscript().slice(0, 4), "Running").activity).toBe(
      "idle",
    );
  });

  it("derives activity from the session and the latest adapter status", () => {
    const t = transcript();
    t.status("running");
    t.status("idle");
    expect(buildThreadChat(t.blocks, "Starting").activity).toBe("starting");
    expect(buildThreadChat(t.blocks, "Running").activity).toBe("idle");
    t.user("go");
    expect(buildThreadChat(t.blocks, "Running").activity).toBe("working");
    expect(buildThreadChat(t.blocks, "Cancelling").activity).toBe("stopping");
    expect(buildThreadChat(t.blocks, "Succeeded").activity).toBe("idle");
    t.permission("perm-1", "Allow?");
    expect(buildThreadChat(t.blocks, "Running").activity).toBe("waiting");
  });

  it("marks unfinished tools of an ended turn as stopped", () => {
    const t = transcript();
    t.user("go");
    t.status("running");
    t.tool("tool-1", "shell", "sleep 100");
    expect(buildThreadChat(t.blocks, "Running").items[1]).toMatchObject({
      calls: [{ state: "running" }],
    });
    t.status("cancelled");
    const ended = buildThreadChat(t.blocks, "Running");
    expect(ended.items[1]).toMatchObject({ calls: [{ state: "interrupted" }] });
    expect(ended.items.at(-1)).toMatchObject({ kind: "notice", text: "Stopped" });
  });

  it("keeps a restarted session's reply apart when the adapter reuses message IDs", () => {
    const t = transcript();
    t.user("fix the test");
    t.delta("mock-message-1", "fix");
    t.final("mock-message-1", "mock:fix the test");
    t.status("failed");
    t.user("run it again");
    t.status("running");
    t.delta("mock-message-1", "run");
    const streaming = buildThreadChat(t.blocks, "Running").items;
    expect(streaming.filter((item) => item.kind === "assistant")).toMatchObject([
      { text: "mock:fix the test", streaming: false },
      { text: "run", streaming: true },
    ]);
    t.final("mock-message-1", "mock:run it again");
    const items = buildThreadChat(t.blocks, "Running").items;
    expect(items.map((item) => item.kind)).toEqual(["user", "assistant", "error", "user", "assistant"]);
    expect(items[1]).toMatchObject({ text: "mock:fix the test" });
    expect(items[4]).toMatchObject({ text: "mock:run it again", streaming: false });
    expect(new Set(items.map((item) => item.key)).size).toBe(items.length);
  });

  it("matches responses to requests by responseTo, not by order", () => {
    const t = transcript();
    t.permission("perm-1", "First?");
    t.permission("perm-2", "Second?");
    t.permissionResponse("perm-1", "deny");
    const requests = buildThreadChat(t.blocks, "Running").items;
    expect(requests).toMatchObject([
      { kind: "request", actionable: false, request: { id: "perm-1", answer: "deny" } },
      { kind: "request", actionable: true, request: { id: "perm-2" } },
    ]);
  });
});

describe("Thread chat view", () => {
  it("renders the reported transcript without cards, numbers, roles, or statuses", async () => {
    const view = renderThread(reportedScreenshotTranscript());
    const list = await screen.findByRole("list", { name: "Thread transcript" });
    expect(within(list).getByText("Hello World!")).toBeInTheDocument();
    expect(within(list).getByText("prompt").closest(".chat-user")).not.toBeNull();
    expect(list.textContent).not.toMatch(/#\d+\.\d+|Session status|Assistant|idle|running|user ·/);
    expect(within(list).getByRole("status")).toHaveTextContent("Working…");
    expect(view.container.querySelectorAll(".chat-assistant")).toHaveLength(1);
  });

  it("streams assistant deltas into one growing message", async () => {
    const t = transcript();
    t.user("hi");
    t.status("running");
    const view = renderThread([...t.blocks]);
    await screen.findByText("hi");
    act(() => pushBlock(t.delta("assistant-1", "Hel")));
    expect(await screen.findByText("Hel")).toBeInTheDocument();
    act(() => pushBlock(t.delta("assistant-1", "lo **there**")));
    await waitFor(() =>
      expect(view.container.querySelector(".chat-assistant")).toHaveTextContent("Hello there"),
    );
    expect(view.container.querySelector(".chat-assistant strong")).toHaveTextContent("there");
    expect(view.container.querySelectorAll(".chat-assistant")).toHaveLength(1);
    act(() => pushBlock(t.final("assistant-1", "Hello there, done.")));
    act(() => pushBlock(t.status("idle")));
    await waitFor(() =>
      expect(view.container.querySelector(".chat-assistant")).toHaveTextContent("Hello there, done."),
    );
    expect(view.container.querySelectorAll(".chat-assistant")).toHaveLength(1);
    expect(screen.queryByText("Working…")).not.toBeInTheDocument();
  });

  it("groups consecutive tool calls into compact rows that expand to output", async () => {
    const t = transcript();
    t.user("look around");
    t.status("running");
    t.tool("t1", "shell", "ls -la");
    t.result("t1", "README.md");
    t.tool("t2", "read_file", "README.md");
    t.result("t2", "# Demo");
    t.tool("t3", "grep", "TODO");
    t.result("t3", "none");
    t.final("a1", "Done.");
    t.status("idle");
    const user = userEvent.setup();
    renderThread(t.blocks);
    const group = await screen.findByRole("button", { name: /Ran 3 tools/ });
    expect(group).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("ls -la")).not.toBeInTheDocument();
    await user.click(group);
    const row = screen.getByRole("button", { name: /shell\s*ls -la/ });
    expect(screen.queryByText("README.md", { selector: "pre" })).not.toBeInTheDocument();
    await user.click(row);
    expect(row).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("README.md", { selector: "pre" })).toBeInTheDocument();
  });

  it("collapses a permission card once it is answered", async () => {
    const t = transcript();
    t.user("clean up");
    t.status("running");
    t.permission("perm-1", "Run rm -rf .cache?");
    const respond = vi.spyOn(api, "respondThread").mockResolvedValue({ thread });
    const user = userEvent.setup();
    const view = renderThread(t.blocks);
    const card = await screen.findByRole("region", { name: "Permission required" });
    expect(within(card).getByText("Run rm -rf .cache?")).toBeInTheDocument();
    expect(screen.queryByText("Working…")).not.toBeInTheDocument();
    await user.click(within(card).getByRole("button", { name: "Deny" }));
    await waitFor(() =>
      expect(respond).toHaveBeenCalledWith(
        thread.id,
        expect.objectContaining({ responseTo: "perm-1", choice: "deny" }),
      ),
    );
    await waitFor(() =>
      expect(screen.queryByRole("region", { name: "Permission required" })).not.toBeInTheDocument(),
    );
    expect(view.container.querySelector(".chat-request-summary")).toHaveTextContent(
      "DeniedRun rm -rf .cache?",
    );
  });

  it("shows adapter errors as one inline line", async () => {
    const t = transcript();
    t.user("go");
    t.error("adapter_failed", "The adapter\nexited.");
    renderThread(t.blocks);
    const error = await screen.findByRole("alert");
    expect(error).toHaveTextContent("The adapter exited.adapter_failed");
    expect(error.tagName).toBe("LI");
  });

  it("sends on Enter, adds newlines on Shift+Enter, and stops a running turn", async () => {
    const t = transcript();
    t.user("long task");
    t.status("running");
    const send = vi.spyOn(api, "sendThreadMessage").mockResolvedValue({ thread });
    const session = vi.spyOn(api, "threadSession").mockResolvedValue({ thread });
    const user = userEvent.setup();
    renderThread(t.blocks);
    const composer = await screen.findByLabelText("Message");
    await user.type(composer, "one{Shift>}{Enter}{/Shift}two");
    expect(composer).toHaveValue("one\ntwo");
    expect(send).not.toHaveBeenCalled();
    await user.type(composer, "{Enter}");
    await waitFor(() =>
      expect(send).toHaveBeenCalledWith(thread.id, thread.resourceVersion, "one\ntwo"),
    );
    await waitFor(() => expect(composer).toHaveValue(""));
    await user.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() =>
      expect(session).toHaveBeenCalledWith("cancel", thread.id, thread.resourceVersion),
    );
  });

  it("hides Stop while the agent is idle", async () => {
    const t = transcript();
    t.user("hi");
    t.final("a1", "hello");
    t.status("idle");
    renderThread(t.blocks);
    await screen.findByText("hello");
    expect(screen.queryByRole("button", { name: "Stop" })).not.toBeInTheDocument();
    expect(screen.queryByText("Working…")).not.toBeInTheDocument();
  });

  it("keeps raw events behind a toggle that is off by default", async () => {
    const user = userEvent.setup();
    renderThread(reportedScreenshotTranscript());
    const toggle = await screen.findByRole("checkbox", { name: "Raw events" });
    expect(toggle).not.toBeChecked();
    expect(screen.queryByText("#12.1")).not.toBeInTheDocument();
    await user.click(toggle);
    const raw = screen.getByRole("list", { name: "Raw Thread events" });
    expect(within(raw).getByText("#12.1")).toBeInTheDocument();
    expect(within(raw).getAllByText("status").length).toBeGreaterThan(0);
  });
});

const reportedCommands = [
  { name: "compact", description: "Summarize the conversation", argumentHint: "<instructions>", kind: "prompt" as const },
  { name: "review", description: "Review a pull request", kind: "prompt" as const },
  { name: "usage", description: "Show plan usage", kind: "prompt" as const, aliases: ["cost", "stats"] },
  { name: "ship", description: "Ship the current branch (project)", argumentHint: "target", kind: "prompt" as const },
];

describe("Slash command model", () => {
  it("uses the latest report and drops entries the daemon would reject", () => {
    const t = transcript();
    expect(latestThreadCommands(t.blocks)).toBeUndefined();
    t.commands([{ name: "old", kind: "prompt" }]);
    t.commands([
      ...reportedCommands,
      { name: "bad name", kind: "prompt" },
      { name: "compact", kind: "action" },
      { name: "-flag", kind: "prompt" },
      { name: "tricky", description: "line\u0007one\ntwo", kind: "action" },
    ]);
    const commands = latestThreadCommands(t.blocks) ?? [];
    expect(commands.map((command) => command.name)).toEqual([
      "compact",
      "review",
      "usage",
      "ship",
      "tricky",
    ]);
    expect(commands.at(-1)).toMatchObject({ description: "lineone two", kind: "action" });
    expect(buildThreadChat(t.blocks, "Running").items).toEqual([]);
  });

  it("filters by name and alias and keeps argument text out of the menu", () => {
    const t = transcript();
    t.commands(reportedCommands);
    const commands = latestThreadCommands(t.blocks);
    expect(commands).toHaveLength(4);
    expect(slashQuery("/")).toBe("");
    expect(slashQuery("/rev")).toBe("rev");
    expect(slashQuery("/review now")).toBeUndefined();
    expect(slashQuery("hello /review")).toBeUndefined();
    const names = (query: string) =>
      filterSlashEntries(query, commands ?? [], [
        { id: "new-session", label: "New session", description: "" },
      ]).map((entry) => (entry.type === "command" ? entry.command.name : entry.control.id));
    expect(names("")).toEqual(["compact", "review", "usage", "ship", "new-session"]);
    expect(names("cost")).toEqual(["usage"]);
    expect(names("s")).toEqual(["usage", "ship", "new-session"]);
    expect(names("session")).toEqual(["new-session"]);
    expect(names("hello")).toEqual([]);
  });
});

describe("Slash command menu", () => {
  const running = () => {
    const t = transcript();
    t.commands(reportedCommands);
    t.user("long task");
    t.status("running");
    return t;
  };

  it("opens on / with the harness-reported commands and inserts the picked one", async () => {
    const send = vi.spyOn(api, "sendThreadMessage").mockResolvedValue({ thread });
    const user = userEvent.setup();
    renderThread(running().blocks);
    const composer = await screen.findByRole("combobox", { name: "Message" });
    await user.type(composer, "/");
    const menu = screen.getByRole("listbox", { name: "Commands" });
    expect(composer).toHaveAttribute("aria-expanded", "true");
    expect(within(menu).getAllByRole("option").map((option) => option.textContent)).toEqual([
      "/compact <instructions>Summarize the conversationPrompt",
      "/reviewReview a pull requestPrompt",
      "/usageShow plan usagePrompt",
      "/ship targetShip the current branch (project)Prompt",
      "StopStop the current turn",
      "New sessionStart a fresh conversation in this Capsule",
    ]);
    await user.type(composer, "sh");
    expect(within(menu).getAllByRole("option")[0]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{Enter}");
    expect(composer).toHaveValue("/ship ");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(send).not.toHaveBeenCalled();
    await user.type(composer, "staging{Enter}");
    await waitFor(() =>
      expect(send).toHaveBeenCalledWith(thread.id, thread.resourceVersion, "/ship staging"),
    );
  });

  it("navigates with arrows, matches aliases, and closes on Escape", async () => {
    const user = userEvent.setup();
    renderThread(running().blocks);
    const composer = await screen.findByRole("combobox", { name: "Message" });
    await user.type(composer, "/");
    await user.keyboard("{ArrowDown}{ArrowDown}");
    const options = screen.getAllByRole("option");
    expect(options[2]).toHaveAttribute("aria-selected", "true");
    expect(composer).toHaveAttribute("aria-activedescendant", options[2].id);
    await user.keyboard("{ArrowUp}{ArrowUp}{ArrowUp}");
    expect(screen.getAllByRole("option").at(-1)).toHaveAttribute("aria-selected", "true");
    await user.type(composer, "cos");
    expect(screen.getByRole("option")).toHaveTextContent("/usage · /costShow plan usage");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    await user.type(composer, "t");
    expect(screen.getByRole("listbox")).toBeInTheDocument();
  });

  it("sends unknown /text as an ordinary prompt", async () => {
    const send = vi.spyOn(api, "sendThreadMessage").mockResolvedValue({ thread });
    const user = userEvent.setup();
    renderThread(running().blocks);
    const composer = await screen.findByRole("combobox", { name: "Message" });
    await user.type(composer, "/deploy-now");
    expect(screen.getByRole("listbox")).toHaveTextContent(
      "No matching commands. Enter sends the text as a message.",
    );
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(send).toHaveBeenCalledWith(thread.id, thread.resourceVersion, "/deploy-now"),
    );
  });

  it("runs Meridian controls instead of sending them", async () => {
    const send = vi.spyOn(api, "sendThreadMessage").mockResolvedValue({ thread });
    const session = vi.spyOn(api, "threadSession").mockResolvedValue({ thread });
    const user = userEvent.setup();
    renderThread(running().blocks);
    const composer = await screen.findByRole("combobox", { name: "Message" });
    await user.type(composer, "/stop{Enter}");
    await waitFor(() =>
      expect(session).toHaveBeenCalledWith("cancel", thread.id, thread.resourceVersion),
    );
    expect(composer).toHaveValue("");
    expect(send).not.toHaveBeenCalled();
  });

  it("offers no Stop while idle and explains a harness that reported nothing", async () => {
    const t = transcript();
    t.user("hi");
    t.final("a1", "hello");
    t.status("idle");
    const user = userEvent.setup();
    renderThread(t.blocks);
    const composer = await screen.findByRole("combobox", { name: "Message" });
    await user.type(composer, "/");
    const menu = screen.getByRole("listbox");
    expect(menu).toHaveTextContent("Mock (test) has not reported commands for this session.");
    expect(within(menu).getAllByRole("option").map((option) => option.textContent)).toEqual([
      "New sessionStart a fresh conversation in this Capsule",
    ]);
  });
});

describe("Thread markdown", () => {
  it("renders no raw HTML, no remote images, and only safe links", () => {
    const view = render(
      <ThreadMarkdown
        text={[
          "<img src=x onerror=alert(1)> <script>alert(1)</script>",
          "[web](https://example.com/a) [js](javascript:alert(1)) [data](data:text/html,x)",
          "[local](/threads/thread-1/delete) ![pixel](https://evil.example/leak?secret=1)",
          "",
          "```sh",
          "make test",
          "```",
        ].join("\n")}
      />,
    );
    expect(view.container.querySelector("script, img, iframe")).toBeNull();
    expect(view.container.textContent).toContain("<script>alert(1)</script>");
    const links = Array.from(view.container.querySelectorAll("a"));
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "https://example.com/a",
      "https://evil.example/leak?secret=1",
    ]);
    for (const link of links) {
      expect(link).toHaveAttribute("target", "_blank");
      expect(link.getAttribute("rel")).toContain("noopener");
      expect(link.getAttribute("rel")).toContain("noreferrer");
    }
    expect(screen.getByText("Image: pixel").tagName).toBe("A");
    expect(screen.getByText("js").tagName).toBe("SPAN");
    expect(screen.getByText("local").tagName).toBe("SPAN");
    expect(view.container.querySelector("pre code")).toHaveTextContent("make test");
  });

  it("allows only absolute http, https, and mailto targets", () => {
    expect(safeLinkTarget("https://example.com")).toBe("https://example.com/");
    expect(safeLinkTarget("mailto:a@example.com")).toBe("mailto:a@example.com");
    expect(safeLinkTarget(" JAVASCRIPT:alert(1)")).toBeUndefined();
    expect(safeLinkTarget("vbscript:x")).toBeUndefined();
    expect(safeLinkTarget("/ui/threads")).toBeUndefined();
    expect(safeLinkTarget("#fn-1")).toBeUndefined();
    expect(safeLinkTarget(undefined)).toBeUndefined();
  });
});
