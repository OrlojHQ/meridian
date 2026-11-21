import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useParams } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { NoteProposalReview as Review } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { NoteDiff } from "./NoteDiff";
import { NoteEditor, type NoteDraft } from "./NoteEditor";
import { NoteMarkdown } from "./NoteMarkdown";
import { ProposingThread } from "./ProposingThread";
import {
  formatBytes,
  hasHiddenCharacters,
  proposalStateLabels,
  sourceThreadId,
} from "./notesText";
import { formatWhen } from "./routineText";
import { Badge } from "./ui/Badge";
import { Button } from "./ui/Button";
import { Tabs } from "./ui/Tabs";

export function threadPath(threadId: string) {
  return `/ui/threads/${encodeURIComponent(threadId)}`;
}

const stateTone: Record<Review["proposal"]["state"], string> = {
  pending: "paused",
  approved: "ready",
  approved_with_edits: "ready",
  rejected: "failed",
  stale: "paused",
  expired: "paused",
};

// NoteProposalReview reviews one proposal or promotion (ADR 0060 section 7).
// It opens on a source diff against the note's current text, with hidden
// characters escaped and long lines wrapped; the rendered preview is a
// second tab. Approve, Edit and Reject act on this one change only.
export function NoteProposalReview() {
  const { proposalId = "" } = useParams();
  const queryClient = useQueryClient();
  const review = useQuery(queries.noteProposal(proposalId));
  const projects = useQuery(queries.projects());
  const [view, setView] = useState("diff");
  const [editing, setEditing] = useState<"approve" | "merge">();
  const refresh = async (projectId?: string) => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["note-proposal", proposalId] }),
      queryClient.invalidateQueries({ queryKey: ["note-proposals"] }),
      projectId ? queryClient.invalidateQueries({ queryKey: ["project-notes", projectId] }) : undefined,
    ]);
  };
  const approve = useMutation({
    mutationFn: (content?: string) => {
      if (!review.data) throw new Error("The proposal is not loaded");
      const { proposal } = review.data;
      return api.approveNoteProposal(proposal.id, {
        digest: proposal.digest,
        expectedRevision: proposal.currentRevision,
        ...(content !== undefined && content !== review.data.content ? { content } : {}),
      });
    },
    onSuccess: () => setEditing(undefined),
    onSettled: () => refresh(review.data?.proposal.projectId),
  });
  const reject = useMutation({
    mutationFn: () => api.rejectNoteProposal(proposalId),
    onSettled: () => refresh(review.data?.proposal.projectId),
  });

  if (review.isPending) return <p role="status">Loading proposal…</p>;
  if (review.isError) {
    return (
      <section className="note-review">
        <p role="alert" className="inline-error">{normalizeAPIError(review.error).message}</p>
        <Link to="/ui/">Home</Link>
      </section>
    );
  }
  const { proposal, content, currentContent, currentLive, credentialShaped } = review.data;
  const project = projects.data?.items.find((item) => item.id === proposal.projectId);
  const notesHref = `/ui/projects/${encodeURIComponent(proposal.projectId)}?tab=notes`;
  const threadId = sourceThreadId(proposal.source);
  const leadId = proposal.source?.leadThreadId;
  const before = currentLive ? currentContent : "";
  const after = proposal.delete ? "" : content;
  const pending = proposal.state === "pending";
  const stale = proposal.state === "stale";
  const hidden = hasHiddenCharacters(content);
  const busy = approve.isPending || reject.isPending;

  const saveMerged = async (draft: NoteDraft) => {
    await api.putProjectNote(proposal.projectId, proposal.name, {
      content: draft.content,
      expectedRevision: draft.expectedRevision,
    });
    setEditing(undefined);
    await refresh(proposal.projectId);
  };

  return (
    <section className="note-review" aria-labelledby="note-review-title">
      <header className="project-settings-header">
        <div>
          <p className="eyebrow">
            {proposal.origin === "promotion" ? "Promotion to Project notes" : "Proposed Project note change"}
            {project ? ` · ${project.name}` : ""}
          </p>
          <h1 id="note-review-title">
            <span className="mono">{proposal.name}.md</span>{" "}
            <Badge tone={stateTone[proposal.state]}>{proposalStateLabels[proposal.state]}</Badge>
          </h1>
        </div>
        <Link className="button-link" to={notesHref}>Project notes</Link>
      </header>

      <div className="note-review-source notice">
        <p>
          <strong>Agent text.</strong> <ProposingThread source={proposal.source} origin={proposal.origin} link={false} />
          {threadId && (
            <>
              {" · "}
              <Link to={threadPath(threadId)}>Open the proposing Thread</Link>
            </>
          )}
          {leadId && leadId !== threadId && (
            <>
              {" · "}
              <Link to={threadPath(leadId)}>Open its lead</Link>
            </>
          )}
        </p>
        <p className="launch-hint">
          {proposal.delete
            ? "Proposes deleting this note."
            : currentLive
              ? `Written against revision ${proposal.baseRevision}; the note is at revision ${proposal.currentRevision}.`
              : "Proposes a new note."}{" "}
          {formatBytes(proposal.size)} · proposed {formatWhen(proposal.createdAt)}
          {pending ? ` · expires ${formatWhen(proposal.expiresAt)}` : ""}
          {proposal.source?.workingNoteName
            ? ` · from working note ${proposal.source.workingNoteName}${proposal.source.workingNoteRevision ? ` revision ${proposal.source.workingNoteRevision}` : ""}`
            : ""}
        </p>
        <p className="launch-hint">
          Every Thread that receives Project notes reads this as a file. Approving grants no permission,
          but the text can steer what agents do.
        </p>
      </div>

      {credentialShaped && (
        <p className="warning" role="alert">
          This text looks like it contains a credential. Credentials belong in named secrets, not notes.
        </p>
      )}
      {hidden && (
        <p className="warning" role="alert">
          This text contains hidden or direction-changing characters. They are shown as escapes such as \u{"{202E}"}.
        </p>
      )}
      {stale && (
        <p className="notice" role="status">
          The note changed after this was proposed, so it cannot be approved as it is. Open it in the editor
          to merge it by hand into the current note.
        </p>
      )}

      {editing ? (
        <NoteEditor
          heading={editing === "approve" ? `Edit before approving ${proposal.name}.md` : `Merge into ${proposal.name}.md`}
          initialName={proposal.name}
          nameEditable={false}
          initialContent={content}
          revision={proposal.currentRevision}
          saveLabel={editing === "approve" ? "Approve with edits" : "Save note"}
          notice="This starts from the proposed agent text."
          onSave={
            editing === "approve"
              ? async (draft) => {
                  await approve.mutateAsync(draft.content);
                }
              : saveMerged
          }
          onCancel={() => setEditing(undefined)}
          // An approval binds the proposal's own base, so only a hand merge
          // can move to a newer head.
          loadCurrent={
            editing === "merge"
              ? async () => {
                  const head = await api.projectNote(proposal.projectId, proposal.name);
                  return { content: head.content, revision: head.note.revision };
                }
              : undefined
          }
        />
      ) : (
        <>
          <Tabs
            label="Review view"
            value={view}
            onChange={setView}
            tabs={[
              { id: "diff", label: "Source diff" },
              { id: "preview", label: "Preview" },
            ]}
          />
          {view === "diff" ? (
            <NoteDiff
              before={before}
              after={after}
              label={currentLive ? `Revision ${proposal.currentRevision} against the proposal` : "The proposed note"}
            />
          ) : (
            <div className="note-editor-preview">
              {after.trim() === "" ? <p className="muted-copy">Nothing to preview.</p> : <NoteMarkdown text={after} />}
              <p className="launch-hint">The preview loads no images and follows no links.</p>
            </div>
          )}
          <div className="note-review-actions">
            {pending && (
              <>
                <Button variant="primary" disabled={busy} onClick={() => approve.mutate(undefined)}>
                  {proposal.delete ? "Approve deletion" : "Approve"}
                </Button>
                {!proposal.delete && (
                  <Button disabled={busy} onClick={() => setEditing("approve")}>Edit</Button>
                )}
              </>
            )}
            {stale && !proposal.delete && (
              <Button variant="primary" disabled={busy} onClick={() => setEditing("merge")}>Open in editor</Button>
            )}
            {(pending || stale) && (
              <Button variant="danger" disabled={busy} onClick={() => reject.mutate()}>Reject</Button>
            )}
          </div>
          {(pending || stale) && (
            <p className="launch-hint">
              Rejecting sends the Thread no reason. To explain, message the Thread.
            </p>
          )}
        </>
      )}
      {approve.isError && (
        <p role="alert" className="inline-error">
          {normalizeAPIError(approve.error).status === 409
            ? "The note or the proposal changed since this page loaded. It is reloaded; review it again."
            : normalizeAPIError(approve.error).message}
        </p>
      )}
      {reject.isError && <p role="alert" className="inline-error">{normalizeAPIError(reject.error).message}</p>}
      {approve.isSuccess && <p role="status">Approved as revision {approve.data.resolvedRevision ?? ""}.</p>}
      {reject.isSuccess && <p role="status">Rejected.</p>}
    </section>
  );
}
