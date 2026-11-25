import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { CapsuleDetail } from "../App";
import { api } from "../api/client";
import type { Capsule, CoordinationGrant, Thread, ThreadLink } from "../api/generated/types.gen";
import { capsuleSearch, leadThreadIds, openSubagentIds } from "../components/subagentTabs";

const stamp = "2026-11-01T00:00:00Z";

function capsule(id: string, name: string, extra: Partial<Capsule> = {}): Capsule {
  return {
    id,
    projectId: "project-1",
    timelineId: `timeline-${id}`,
    name,
    state: "Ready",
    desiredState: "Ready",
    restoreComplete: true,
    createdAt: stamp,
    updatedAt: stamp,
    resourceVersion: 2,
    ...extra,
  };
}

function thread(id: string, capsuleId: string, extra: Partial<Thread> = {}): Thread {
  return {
    id,
    capsuleId,
    state: "active",
    harness: "mock",
    turn: "idle",
    currentRunState: "Succeeded",
    protocol: "meridian.adapter.v1",
    structuredSupported: true,
    encryptedAtRest: true,
    messageCount: 1,
    encryptedBytes: 40,
    createdAt: stamp,
    updatedAt: stamp,
    resourceVersion: 3,
    ...extra,
  };
}

const leadCapsule = capsule("capsule-lead", "lead work");
const subCapsule = capsule("capsule-sub", "subagent work", { role: "sub", leadThreadId: "thread-lead" });
const otherCapsule = capsule("capsule-other", "unrelated work");

const lead = thread("thread-lead", leadCapsule.id, {
  coordinationGrantId: "grant-1",
  rootThreadId: "thread-lead",
  depth: 0,
});
const sub = thread("thread-sub", subCapsule.id, {
  coordinationGrantId: "grant-1",
  parentThreadId: lead.id,
  rootThreadId: lead.id,
  depth: 1,
});
const other = thread("thread-other", otherCapsule.id);

const grant: CoordinationGrant = {
  id: "grant-1",
  projectId: "project-1",
  rootThreadId: lead.id,
  rootCapsuleId: leadCapsule.id,
  state: "active",
  spawnApproval: "auto",
  maxDepth: 1,
  maxChildren: 4,
  maxDescendants: 8,
  maxLiveCapsules: 4,
  maxMessagesPerLink: 50,
  allowedHarnesses: ["mock"],
  lastOperatorAt: stamp,
  unattendedLimitSeconds: 86400,
  lapsesAt: "2099-11-01T08:00:00Z",
  onDeadline: "cancel_and_pause",
  archiveAfterSeconds: 86400,
  descendantsCreated: 1,
  spawnsInWindow: 1,
  createdAt: stamp,
  updatedAt: stamp,
  resourceVersion: 3,
};

const link: ThreadLink = {
  id: "link-sub",
  grantId: grant.id,
  parentThreadId: lead.id,
  parentCapsuleId: leadCapsule.id,
  childThreadId: sub.id,
  childCapsuleId: subCapsule.id,
  childIntentId: "intent-sub",
  depth: 1,
  name: "docs-writer",
  harness: "mock",
  state: "active",
  phase: "working",
  reportSeq: 0,
  reportDeliveredSeq: 0,
  messagesForwarded: 0,
  operatorMessages: 0,
  createdAt: stamp,
  updatedAt: stamp,
  resourceVersion: 1,
};

const capabilities = {
  providerVersion: "docker/v1",
  isolation: "container" as const,
  attach: false,
  run: true,
  structured: true,
  git: false,
  pause: true,
  snapshot: false,
  clone: false,
  browse: false,
  delivery: false,
  preview: false,
  resourceMetrics: false,
  coordination: { supported: true, maxDepth: 1, maxDescendants: 16 },
};

function mockDaemon() {
  const capsules = [leadCapsule, subCapsule, otherCapsule];
  const threads = [lead, sub, other];
  vi.spyOn(api, "capsule").mockImplementation(async (id) => {
    const found = capsules.find((item) => item.id === id);
    if (!found) throw new Error("not found");
    return found;
  });
  vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
  vi.spyOn(api, "threads").mockImplementation(async (capsuleId) => ({
    items: threads.filter((item) => item.capsuleId === capsuleId),
  }));
  vi.spyOn(api, "thread").mockImplementation(async (id) => {
    const found = threads.find((item) => item.id === id);
    if (!found) throw new Error("not found");
    return found;
  });
  vi.spyOn(api, "threadBlocks").mockResolvedValue({ items: [], nextCursor: 0, more: false });
  vi.spyOn(api, "projects").mockResolvedValue({ items: [] });
  vi.spyOn(api, "harnessProfiles").mockResolvedValue({
    items: [{ name: "mock-structured", structured: true, pty: false }],
  });
  vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
  vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
  const links = vi.spyOn(api, "threadLinks").mockImplementation(async (threadId) => ({
    items: threadId === lead.id ? [link] : [],
  }));
  vi.spyOn(api, "coordinationGrant").mockResolvedValue(grant);
  vi.spyOn(api, "coordinationRequests").mockResolvedValue({ items: [] });
  return {
    links,
    removeSubagent: vi.spyOn(api, "removeSubagent"),
    threadSession: vi.spyOn(api, "threadSession"),
    archiveThread: vi.spyOn(api, "archiveThread"),
    deleteThread: vi.spyOn(api, "deleteThread"),
    revoke: vi.spyOn(api, "revokeCoordinationGrant"),
  };
}

function Where() {
  const location = useLocation();
  return <output data-testid="where">{`${location.pathname}${location.search}`}</output>;
}

function show(search: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/ui/capsules/${leadCapsule.id}${search}`]}>
        <Routes>
          <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
        </Routes>
        <Where />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function where() {
  const url = new URL(screen.getByTestId("where").textContent ?? "", "http://meridian.test");
  return { pathname: url.pathname, session: url.searchParams.get("session"), open: url.searchParams.getAll("open") };
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("subagent tab URL", () => {
  it("reads unique open ids and writes them back in order", () => {
    expect(openSubagentIds(new URLSearchParams("open=a&open=&open=b&open=a"))).toEqual(["a", "b"]);
    expect(capsuleSearch("thread-1", ["a", "b"])).toBe("?session=thread-1&open=a&open=b");
    expect(capsuleSearch("", [])).toBe("");
  });

  it("treats only root Threads with a grant as leads", () => {
    expect(leadThreadIds([lead, sub, other, { ...lead, id: "gone", state: "deleted" }])).toEqual([lead.id]);
  });
});

describe("Subagent tabs on the lead Capsule's page", () => {
  it("opens a subagent from the Subagents panel as a tab on the lead's page", async () => {
    mockDaemon();
    show(`?session=${lead.id}`);
    await userEvent.click(await screen.findByRole("button", { name: "1 agent, 1 working" }));
    const panel = await screen.findByRole("region", { name: "Subagents" });
    expect(await within(panel).findByRole("link", { name: "Open docs-writer's Capsule page" })).toHaveAttribute(
      "href",
      `/ui/capsules/${subCapsule.id}?session=${sub.id}`,
    );
    await userEvent.click(within(panel).getByRole("link", { name: "docs-writer" }));

    const tablist = screen.getByRole("tablist", { name: "Sessions" });
    const tab = await within(tablist).findByRole("tab", { name: "docs-writer, subagent" });
    expect(tab).toHaveAttribute("aria-selected", "true");
    expect(tab).toHaveTextContent("Subagent");
    expect(where()).toEqual({ pathname: `/ui/capsules/${leadCapsule.id}`, session: sub.id, open: [sub.id] });
    // The page stays the lead's; the subagent's Thread fills the session panel
    // and says whose header and tools are in view.
    const notice = await screen.findByText(/It runs in its own Capsule/);
    expect(notice).toHaveTextContent("pull request controls are the lead Capsule's");
    expect(within(notice).getByRole("link", { name: "Open its Capsule page" })).toHaveAttribute(
      "href",
      `/ui/capsules/${subCapsule.id}?session=${sub.id}`,
    );
    expect(screen.getByRole("link", { name: "Open the lead" })).toHaveAttribute(
      "href",
      `/ui/capsules/${leadCapsule.id}?session=${lead.id}&open=${sub.id}`,
    );
  });

  it("restores opened tabs from the URL and keeps them while switching sessions", async () => {
    mockDaemon();
    const first = show(`?session=${sub.id}&open=${sub.id}`);
    const tab = await screen.findByRole("tab", { name: "docs-writer, subagent" });
    expect(tab).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByText(/It runs in its own Capsule/)).toBeInTheDocument();

    const leadTab = screen.getAllByRole("tab").find((item) => item.id === `session-tab-${lead.id}`);
    if (!leadTab) throw new Error("lead tab missing");
    await userEvent.click(leadTab);
    expect(leadTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tab", { name: "docs-writer, subagent" })).toHaveAttribute("aria-selected", "false");
    expect(where()).toEqual({ pathname: `/ui/capsules/${leadCapsule.id}`, session: lead.id, open: [sub.id] });

    // A reload starts again from the same URL and finds the tab.
    first.unmount();
    show(`?session=${lead.id}&open=${sub.id}`);
    expect(await screen.findByRole("tab", { name: "docs-writer, subagent" })).toHaveAttribute(
      "aria-selected",
      "false",
    );
  });

  it("closes a tab without stopping, archiving, or removing the subagent", async () => {
    const daemon = mockDaemon();
    show(`?session=${sub.id}&open=${sub.id}`);
    const close = await screen.findByRole("button", {
      name: "Close docs-writer tab; the subagent keeps running",
    });
    expect(close).toHaveAttribute("title", "Close docs-writer tab; the subagent keeps running");
    await userEvent.click(close);

    await waitFor(() => expect(screen.queryByRole("tab", { name: "docs-writer, subagent" })).not.toBeInTheDocument());
    const leadTab = screen.getAllByRole("tab").find((item) => item.id === `session-tab-${lead.id}`);
    expect(leadTab).toHaveAttribute("aria-selected", "true");
    expect(where()).toEqual({ pathname: `/ui/capsules/${leadCapsule.id}`, session: lead.id, open: [] });
    await waitFor(() => expect(leadTab).toHaveFocus());
    expect(daemon.removeSubagent).not.toHaveBeenCalled();
    expect(daemon.threadSession).not.toHaveBeenCalled();
    expect(daemon.archiveThread).not.toHaveBeenCalled();
    expect(daemon.deleteThread).not.toHaveBeenCalled();
    expect(daemon.revoke).not.toHaveBeenCalled();
  });

  it("closes the focused subagent tab with Delete", async () => {
    mockDaemon();
    show(`?session=${lead.id}&open=${sub.id}`);
    const tab = await screen.findByRole("tab", { name: "docs-writer, subagent" });
    tab.focus();
    await userEvent.keyboard("{Delete}");
    await waitFor(() => expect(screen.queryByRole("tab", { name: "docs-writer, subagent" })).not.toBeInTheDocument());
    expect(where().open).toEqual([]);
  });

  it("ignores ids in the URL that no lead Thread here linked", async () => {
    const daemon = mockDaemon();
    show(`?session=${other.id}&open=${other.id}&open=thread-made-up`);
    const leadTab = await waitFor(() => {
      const found = screen.getAllByRole("tab").find((item) => item.id === `session-tab-${lead.id}`);
      if (!found) throw new Error("lead tab missing");
      return found;
    });
    await waitFor(() => expect(daemon.links).toHaveBeenCalledWith(lead.id, expect.anything()));
    await waitFor(() => expect(leadTab).toHaveAttribute("aria-selected", "true"));
    expect(screen.queryByRole("tab", { name: /subagent/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /keeps running/ })).not.toBeInTheDocument();
    expect(daemon.links).not.toHaveBeenCalledWith(other.id, expect.anything());

    // Selecting a session writes back only the tabs that were really linked.
    await userEvent.click(leadTab);
    expect(where()).toEqual({ pathname: `/ui/capsules/${leadCapsule.id}`, session: lead.id, open: [] });
  });
});
