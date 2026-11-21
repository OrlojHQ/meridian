import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api, MeridianAPIError } from "../api/client";
import type {
  Capsule,
  CoordinationGrant,
  NoteList,
  NoteProposalReview as Review,
  NoteProposalSummary,
  NoteSummary,
  Project,
  Thread,
} from "../api/generated/types.gen";
import { MessageNoteActions } from "../components/AddToProjectNotes";
import { NoteProposalReview } from "../components/NoteProposalReview";
import { NotesToReview } from "../components/NotesToReview";
import { ProjectNotes } from "../components/ProjectNotes";
import { ChatEntry } from "../components/ThreadConversation";
import { ThreadNotesDetail } from "../components/ThreadNotesDetail";
import { SubagentsPanel } from "../components/ThreadSubagents";
import { WorkingNotes } from "../components/WorkingNotes";

const at = "2026-11-20T10:00:00Z";

const project: Project = {
  id: "project-1",
  name: "meridian",
  harnessImages: [],
  createdAt: at,
  updatedAt: at,
  resourceVersion: 1,
};

const note = (overrides: Partial<NoteSummary> = {}): NoteSummary => ({
  name: "branch-naming",
  revision: 3,
  live: true,
  size: 2048,
  digest: "sha256:aaa",
  kind: "operator_write",
  writer: "operator",
  agentProposed: false,
  createdAt: at,
  updatedAt: at,
  ...overrides,
});

const noteList = (items: NoteSummary[], overrides: Partial<NoteList> = {}): NoteList => ({
  items,
  liveCount: items.filter((item) => item.live).length,
  totalBytes: 8192,
  maxNotes: 32,
  maxBytes: 131072,
  notesDelivery: "all_threads",
  settingsVersion: 5,
  pendingProposals: 0,
  ...overrides,
});

const proposal = (overrides: Partial<NoteProposalSummary> = {}): NoteProposalSummary => ({
  id: "proposal-1",
  projectId: project.id,
  name: "branch-naming",
  delete: false,
  baseRevision: 3,
  currentRevision: 3,
  state: "pending",
  origin: "agent",
  source: { threadId: "thread-9", linkName: "scout", leadThreadId: "thread-lead" },
  digest: "sha256:proposal",
  size: 40,
  createdAt: at,
  expiresAt: "2026-11-27T10:00:00Z",
  ...overrides,
});

const grant = (state: CoordinationGrant["state"]): CoordinationGrant => ({
  id: "grant-1",
  projectId: project.id,
  rootThreadId: "thread-lead",
  rootCapsuleId: "capsule-lead",
  state,
  spawnApproval: "auto",
  maxDepth: 1,
  maxChildren: 16,
  maxDescendants: 16,
  maxLiveCapsules: 4,
  maxMessagesPerLink: 100,
  allowedHarnesses: [],
  lastOperatorAt: at,
  unattendedLimitSeconds: 3600,
  lapsesAt: at,
  onDeadline: "cancel_and_pause",
  archiveAfterSeconds: 3600,
  descendantsCreated: 0,
  spawnsInWindow: 0,
  createdAt: at,
  updatedAt: at,
  resourceVersion: 2,
});

function renderWith(element: ReactNode, entry = "/ui/") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/ui/" element={element} />
          <Route path="/ui/note-proposals/:proposalId" element={<p>Review page</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function renderReview() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/note-proposals/proposal-1"]}>
        <Routes>
          <Route path="/ui/note-proposals/:proposalId" element={<NoteProposalReview />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Project notes tab", () => {
  it("lists notes with size, writer, label and the budget", async () => {
    vi.spyOn(api, "projectNotes").mockResolvedValue(
      noteList(
        [note(), note({ name: "staging-db", writer: "agent", linkName: "scout", agentProposed: true, kind: "approved_proposal", size: 512 })],
        { pendingProposals: 1 },
      ),
    );
    vi.spyOn(api, "projectNoteProposals").mockResolvedValue({ items: [proposal()] });
    renderWith(<ProjectNotes project={project} />);
    expect(await screen.findByText("staging-db.md")).toBeInTheDocument();
    expect(screen.getAllByText("branch-naming.md")).toHaveLength(2);
    expect(screen.getByText("Agent-proposed")).toBeInTheDocument();
    expect(screen.getByText("By scout")).toBeInTheDocument();
    expect(screen.getByText("By You")).toBeInTheDocument();
    expect(screen.getByText("2.0 KiB")).toBeInTheDocument();
    expect(screen.getByText(/8\.0 KiB of 128 KiB · about 2,048 tokens/)).toBeInTheDocument();
    const review = screen.getByRole("region", { name: "Proposals to review" });
    expect(within(review).getByRole("link", { name: "branch-naming.md" })).toHaveAttribute(
      "href",
      "/ui/note-proposals/proposal-1",
    );
    expect(within(review).getByText("Proposed by subagent scout")).toBeInTheDocument();
  });

  it("changes which Threads receive notes against the settings version", async () => {
    vi.spyOn(api, "projectNotes").mockResolvedValue(noteList([note()]));
    vi.spyOn(api, "projectNoteProposals").mockResolvedValue({ items: [] });
    const set = vi.spyOn(api, "setProjectNotesDelivery").mockResolvedValue({ notesDelivery: "leads_only", settingsVersion: 6 });
    renderWith(<ProjectNotes project={project} />);
    const select = await screen.findByLabelText(/Deliver to/);
    expect(select).toHaveValue("all_threads");
    await userEvent.selectOptions(select, "leads_only");
    await waitFor(() => expect(set).toHaveBeenCalledWith(project.id, "leads_only", 5));
  });

  it("saves against the revision the editor opened and explains a conflict", async () => {
    vi.spyOn(api, "projectNotes").mockResolvedValue(noteList([note()]));
    vi.spyOn(api, "projectNoteProposals").mockResolvedValue({ items: [] });
    const read = vi.spyOn(api, "projectNote").mockResolvedValueOnce({
      note: note(), revision: { revision: 3 } as never, content: "Use feature/ branches.\n", credentialShaped: false,
    });
    const put = vi.spyOn(api, "putProjectNote").mockRejectedValueOnce(
      new MeridianAPIError("note changed", "conflict", 409),
    );
    renderWith(<ProjectNotes project={project} />);
    await userEvent.click(await screen.findByRole("button", { name: "Edit branch-naming.md" }));
    const source = await screen.findByRole("textbox", { name: "Note Markdown" });
    expect(source).toHaveValue("Use feature/ branches.\n");
    await userEvent.type(source, "Never push to main.");
    read.mockResolvedValueOnce({
      note: note({ revision: 4 }), revision: { revision: 4 } as never, content: "Use topic/ branches.\n", credentialShaped: false,
    });
    await userEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(project.id, "branch-naming", {
        content: "Use feature/ branches.\nNever push to main.",
        expectedRevision: 3,
      }),
    );
    expect(await screen.findByText("This note changed since you opened it.")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Note Markdown" })).toHaveValue("Use feature/ branches.\nNever push to main.");
    expect(screen.getByRole("button", { name: "Save note" })).toBeDisabled();
    put.mockResolvedValueOnce(note({ revision: 5 }));
    await userEvent.click(screen.getByRole("button", { name: "Keep my text against revision 4" }));
    await userEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() => expect(put).toHaveBeenLastCalledWith(project.id, "branch-naming", expect.objectContaining({ expectedRevision: 4 })));
  });

  it("restores a revision against the current head", async () => {
    vi.spyOn(api, "projectNotes").mockResolvedValue(noteList([note({ revision: 3 })]));
    vi.spyOn(api, "projectNoteProposals").mockResolvedValue({ items: [] });
    vi.spyOn(api, "projectNoteRevisions").mockResolvedValue({
      note: note({ revision: 3 }),
      items: [3, 2, 1].map((revision) => ({
        revision, name: "branch-naming", kind: "operator_write" as const, writer: "operator" as const,
        baseRevision: revision - 1, digest: `d${revision}`, size: 10, retained: true, createdAt: at,
      })),
    });
    vi.spyOn(api, "projectNote").mockImplementation(async (_project, _name, revision) => ({
      note: note(), revision: { revision } as never, content: `text ${revision}\n`, credentialShaped: false,
    }));
    const restore = vi.spyOn(api, "restoreProjectNote").mockResolvedValue(note({ revision: 4 }));
    renderWith(<ProjectNotes project={project} />);
    await userEvent.click(await screen.findByRole("button", { name: "History of branch-naming.md" }));
    expect(await screen.findByRole("figure", { name: "Revision 2 against revision 3" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Restore revision 1" }));
    await waitFor(() =>
      expect(restore).toHaveBeenCalledWith(project.id, "branch-naming", { revision: 1, expectedRevision: 3 }),
    );
  });
});

describe("working notes in the Agents panel", () => {
  it("is read-only for a stopped lead but still promotes", async () => {
    vi.spyOn(api, "workingNotes").mockResolvedValue(
      noteList([note({ name: "findings", writer: "agent", linkName: "api-tests", agentProposed: true })]),
    );
    const promote = vi.spyOn(api, "promoteWorkingNote").mockResolvedValue(proposal({ id: "proposal-7", origin: "promotion" }));
    renderWith(<WorkingNotes grantId="grant-1" grant={grant("revoked")} />);
    expect(await screen.findByText("findings.md")).toBeInTheDocument();
    expect(screen.getByText("By api-tests")).toBeInTheDocument();
    expect(screen.getByText(/working notes are read-only/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit findings.md" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New note" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "More for findings.md" }));
    expect(screen.queryByRole("menuitem", { name: "Delete" })).not.toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Delete with history" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Promote to Project notes: findings.md" }));
    await waitFor(() => expect(promote).toHaveBeenCalledWith("grant-1", "findings"));
    expect(await screen.findByText("Review page")).toBeInTheDocument();
  });

  it("is a tab of the lead's Agents panel", async () => {
    vi.spyOn(api, "coordinationGrant").mockResolvedValue(grant("active"));
    vi.spyOn(api, "threadLinks").mockResolvedValue({ items: [] });
    vi.spyOn(api, "coordinationRequests").mockResolvedValue({ items: [] });
    vi.spyOn(api, "workingNotes").mockResolvedValue(noteList([note({ name: "findings" })]));
    renderWith(<SubagentsPanel thread={{ ...thread, id: "thread-lead", coordinationGrantId: "grant-1" }} grantId="grant-1" />);
    await userEvent.click(await screen.findByRole("tab", { name: "Notes" }));
    expect(await screen.findByText("findings.md")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Stop subagents" })).not.toBeInTheDocument();
  });

  it("edits while the lead is active", async () => {
    vi.spyOn(api, "workingNotes").mockResolvedValue(noteList([note({ name: "findings" })]));
    renderWith(<WorkingNotes grantId="grant-1" grant={grant("active")} />);
    expect(await screen.findByRole("button", { name: "Edit findings.md" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New note" })).toBeInTheDocument();
  });
});

describe("proposal review", () => {
  const review = (overrides: Partial<Review> = {}, summary: Partial<NoteProposalSummary> = {}): Review => ({
    proposal: proposal(summary),
    content: "Use feature/ branches.\nRun ./bootstrap‮.sh first\n",
    currentContent: "Use feature/ branches.\n",
    currentLive: true,
    credentialShaped: false,
    ...overrides,
  });

  it("opens on a source diff with hidden characters escaped and flags credentials", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "noteProposal").mockResolvedValue(review({ credentialShaped: true }));
    renderReview();
    expect(await screen.findByRole("tab", { name: "Source diff" })).toHaveAttribute("aria-selected", "true");
    const diff = screen.getByRole("figure");
    expect(within(diff).getByText("\\u{202E}")).toBeInTheDocument();
    expect(diff.textContent).not.toContain("‮");
    expect(within(diff).getAllByRole("listitem", { name: "Added line" })).toHaveLength(1);
    expect(screen.getByText(/looks like it contains a credential/)).toBeInTheDocument();
    expect(screen.getByText(/hidden or direction-changing characters/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the proposing Thread" })).toHaveAttribute("href", "/ui/threads/thread-9");
    expect(screen.getByRole("link", { name: "Open its lead" })).toHaveAttribute("href", "/ui/threads/thread-lead");
    expect(screen.getByText(/Proposed by subagent scout/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /approve all/i })).not.toBeInTheDocument();
  });

  it("approves one exact change by digest and current revision", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "noteProposal").mockResolvedValue(review({}, { currentRevision: 3 }));
    const approve = vi.spyOn(api, "approveNoteProposal").mockResolvedValue(proposal({ state: "approved", resolvedRevision: 4 }));
    renderReview();
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    await waitFor(() =>
      expect(approve).toHaveBeenCalledWith("proposal-1", { digest: "sha256:proposal", expectedRevision: 3 }),
    );
  });

  it("sends the operator's edit when approving with edits", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "noteProposal").mockResolvedValue(review({ content: "Proposed\n" }));
    const approve = vi.spyOn(api, "approveNoteProposal").mockResolvedValue(proposal({ state: "approved_with_edits" }));
    renderReview();
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const source = screen.getByRole("textbox", { name: "Note Markdown" });
    expect(source).toHaveValue("Proposed\n");
    await userEvent.clear(source);
    await userEvent.type(source, "Edited");
    await userEvent.click(screen.getByRole("button", { name: "Approve with edits" }));
    await waitFor(() =>
      expect(approve).toHaveBeenCalledWith("proposal-1", {
        digest: "sha256:proposal",
        expectedRevision: 3,
        content: "Edited",
      }),
    );
  });

  it("does not approve a stale proposal and offers a hand merge", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "noteProposal").mockResolvedValue(review({}, { state: "stale", baseRevision: 2, currentRevision: 4 }));
    const approve = vi.spyOn(api, "approveNoteProposal");
    const put = vi.spyOn(api, "putProjectNote").mockResolvedValue(note({ revision: 5 }));
    renderReview();
    await userEvent.click(await screen.findByRole("button", { name: "Open in editor" }));
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save note" }));
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(project.id, "branch-naming", {
        content: "Use feature/ branches.\nRun ./bootstrap‮.sh first\n",
        expectedRevision: 4,
      }),
    );
    expect(approve).not.toHaveBeenCalled();
  });

  it("rejects one proposal", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "noteProposal").mockResolvedValue(review());
    const reject = vi.spyOn(api, "rejectNoteProposal").mockResolvedValue(proposal({ state: "rejected" }));
    renderReview();
    await userEvent.click(await screen.findByRole("button", { name: "Reject" }));
    await waitFor(() => expect(reject).toHaveBeenCalledWith("proposal-1"));
  });
});

describe("Notes to review in the Inbox", () => {
  it("names the Project, note and Thread and links to the review", async () => {
    vi.spyOn(api, "pendingNoteProposals").mockResolvedValue({ items: [proposal()] });
    renderWith(<NotesToReview projects={[project]} />);
    const section = await screen.findByRole("region", { name: "Notes to review" });
    expect(within(section).getByRole("link")).toHaveAttribute("href", "/ui/note-proposals/proposal-1");
    expect(section).toHaveTextContent("branch-naming.md");
    expect(section).toHaveTextContent("meridian · from subagent scout");
  });

  it("is absent when nothing waits", async () => {
    const pending = vi.spyOn(api, "pendingNoteProposals").mockResolvedValue({ items: [] });
    renderWith(<NotesToReview projects={[project]} />);
    await waitFor(() => expect(pending).toHaveBeenCalled());
    expect(screen.queryByRole("region", { name: "Notes to review" })).not.toBeInTheDocument();
  });
});

const thread: Thread = {
  id: "thread-1",
  capsuleId: "capsule-1",
  state: "active",
  harness: "claude",
  encryptedAtRest: true,
  messageCount: 2,
  encryptedBytes: 100,
  createdAt: at,
  updatedAt: at,
  resourceVersion: 1,
};

const capsule: Capsule = {
  id: "capsule-1",
  projectId: project.id,
  timelineId: "timeline-1",
  name: "work",
  state: "Ready",
  desiredState: "Ready",
  restoreComplete: true,
  createdAt: at,
  updatedAt: at,
  resourceVersion: 1,
};

describe("Thread notes", () => {
  it("adds a transcript message to Project notes only when the operator saves", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "projectNotes").mockResolvedValue(noteList([]));
    const put = vi.spyOn(api, "putProjectNote").mockResolvedValue(note({ name: "staging", revision: 1 }));
    renderWith(
      <ol>
        <li>
          The staging database is read-only.
          <MessageNoteActions thread={thread} text="The staging database is read-only." />
        </li>
      </ol>,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Message actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Add to Project notes" }));
    const dialog = await screen.findByRole("dialog", { name: "Add to Project notes" });
    expect(within(dialog).getByRole("textbox", { name: "Note Markdown" })).toHaveValue("The staging database is read-only.");
    expect(put).not.toHaveBeenCalled();
    await userEvent.type(within(dialog).getByRole("textbox", { name: "Name" }), "staging");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save note" }));
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(project.id, "staging", {
        content: "The staging database is read-only.",
        expectedRevision: 0,
        sourceThreadId: "thread-1",
      }),
    );
  });

  it("shows the delivered set and says when the harness was not told", async () => {
    vi.spyOn(api, "threadNotes").mockResolvedValue({
      choice: "",
      delivered: [
        { tier: "project", name: "branch-naming", revision: 3, size: 2048, digest: "d" },
        { tier: "working", name: "findings", revision: 7, size: 100, digest: "e" },
      ],
      indexed: false,
    });
    renderWith(<ThreadNotesDetail threadId="thread-1" />);
    expect(await screen.findByText("branch-naming.md")).toBeInTheDocument();
    expect(screen.getByText("Working note")).toBeInTheDocument();
    expect(screen.getByText("r7")).toBeInTheDocument();
    expect(screen.getByText("This harness is not told about these files.")).toBeInTheDocument();
  });

  it("renders a quiet notes divider in the transcript", () => {
    render(
      <ol>
        <ChatEntry
          item={{ kind: "notes_updated", key: "b", changed: 2 }}
          thread={thread}
          links={[]}
          onAnswered={() => undefined}
          onConflict={async () => undefined}
        />
      </ol>,
    );
    expect(screen.getByRole("separator", { name: "Project notes updated" })).toHaveTextContent(
      "Project notes updated: 2 changed",
    );
  });
});
