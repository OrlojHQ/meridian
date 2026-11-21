import { useMutation, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { useNavigate } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { CoordinationGrant } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { NotesWorkspace } from "./NotesWorkspace";
import { workingNoteScope } from "./noteScope";
import { notesBudget } from "./notesText";
import { noteProposalPath } from "./ProjectNotes";

// Edits need a live tree; the daemon refuses them once the grant is
// revoked, ended or paused, and still allows delete with history, shred and
// promotion (ADR 0060 section 1).
export function workingNotesWritable(grant: Pick<CoordinationGrant, "state"> | undefined) {
  return grant?.state === "active";
}

// WorkingNotes is the Notes tab of a lead's Agents panel: the tree's shared
// notes, which the lead and its subagents write without review. They never
// leave the tree until the operator promotes one to Project notes, which
// opens the same review page as an agent's proposal.
export function WorkingNotes({ grantId, grant }: { grantId: string; grant?: CoordinationGrant }) {
  const navigate = useNavigate();
  const scope = useMemo(() => workingNoteScope(grantId), [grantId]);
  const notes = useQuery(queries.workingNotes(grantId));
  const promote = useMutation({
    mutationFn: (name: string) => api.promoteWorkingNote(grantId, name),
    onSuccess: (proposal) => navigate(noteProposalPath(proposal.id)),
  });
  const writable = workingNotesWritable(grant);
  if (notes.isPending) return <p role="status">Loading working notes…</p>;
  if (notes.isError) {
    return <p role="alert" className="inline-error">{normalizeAPIError(notes.error).message}</p>;
  }
  const readOnlyNote =
    grant?.state === "lapsed"
      ? "The lead is paused, so its working notes are read-only until you message it. You can still promote them."
      : "This lead has stopped, so its working notes are read-only. You can still read and promote them.";
  return (
    <NotesWorkspace
      scope={scope}
      list={notes.data}
      readOnly={!writable}
      readOnlyNote={readOnlyNote}
      title="Working notes"
      emptyText="The lead and its subagents have not written any working notes yet."
      writerLabel={(note) => (note.writer === "operator" ? "You" : note.linkName ? note.linkName : "Lead")}
      rowActions={[
        {
          label: "Promote to Project notes",
          disabled: promote.isPending,
          onSelect: (note) => promote.mutate(note.name),
        },
      ]}
      summary={
        <p className="notes-budget">
          {notesBudget(notes.data.totalBytes, notes.data.maxBytes)} · {notes.data.liveCount} of {notes.data.maxNotes} notes.
          Only this lead and its subagents read them.
        </p>
      }
    >
      {promote.isError && (
        <p role="alert" className="inline-error">{normalizeAPIError(promote.error).message}</p>
      )}
    </NotesWorkspace>
  );
}
