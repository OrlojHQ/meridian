import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useMemo } from "react";
import { Link } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { NotesDelivery, Project } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { NotesWorkspace } from "./NotesWorkspace";
import { projectNoteScope } from "./noteScope";
import {
  notesBudget,
  notesDeliveryLabels,
  proposalSourceLabel,
  proposalStateLabels,
} from "./notesText";
import { relativeTime } from "../shell/capsuleActivity";
import { Select } from "./ui/Field";

export function noteProposalPath(proposalId: string) {
  return `/ui/note-proposals/${encodeURIComponent(proposalId)}`;
}

const deliveryChoices: NotesDelivery[] = ["all_threads", "leads_only", "off"];

// ProjectNotes is the Project page's Notes tab (ADR 0060 section 7): the
// Project's notes by name with size, last change and who made it, the total
// against the limit, which Threads receive them, and pending proposals.
// Proposal lines name the note and the Thread only, never proposal text.
export function ProjectNotes({ project }: { project: Project }) {
  const queryClient = useQueryClient();
  const deliveryId = useId();
  const scope = useMemo(() => projectNoteScope(project.id), [project.id]);
  const notes = useQuery(queries.projectNotes(project.id));
  const proposals = useQuery(queries.projectNoteProposals(project.id));
  const delivery = useMutation({
    mutationFn: (choice: NotesDelivery) => {
      if (!notes.data) throw new Error("Notes are not loaded");
      return api.setProjectNotesDelivery(project.id, choice, notes.data.settingsVersion);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["project-notes", project.id] }),
  });
  if (notes.isPending) return <p role="status">Loading notes…</p>;
  if (notes.isError) {
    return <p role="alert" className="inline-error">{normalizeAPIError(notes.error).message}</p>;
  }
  const list = notes.data;
  const pending = (proposals.data?.items ?? []).filter((item) => item.state === "pending" || item.state === "stale");
  const pendingCount = Math.max(list.pendingProposals, pending.filter((item) => item.state === "pending").length);
  return (
    <NotesWorkspace
      scope={scope}
      list={list}
      readOnly={false}
      title="Project notes"
      emptyText="No Project notes yet. Write one, or approve a lead's proposal."
      summary={
        <div className="notes-summary">
          <p className="notes-budget">
            {notesBudget(list.totalBytes, list.maxBytes)} · {list.liveCount} of {list.maxNotes} notes
          </p>
          <label className="notes-delivery" htmlFor={deliveryId}>
            <span>
              <span>Deliver to</span>
              <small>Copied as files into each new session. Routines opt in one by one.</small>
            </span>
            <Select
              id={deliveryId}
              value={list.notesDelivery}
              disabled={delivery.isPending}
              onChange={(event) => delivery.mutate(event.currentTarget.value as NotesDelivery)}
            >
              {deliveryChoices.map((choice) => (
                <option key={choice} value={choice}>{notesDeliveryLabels[choice]}</option>
              ))}
            </Select>
          </label>
          {delivery.isError && (
            <p role="alert" className="inline-error">
              {normalizeAPIError(delivery.error).status === 409
                ? "The setting changed elsewhere. It is reloaded; choose again."
                : normalizeAPIError(delivery.error).message}
            </p>
          )}
        </div>
      }
    >
      <section className="notes-proposals" aria-label="Proposals to review">
        <h3>
          Proposals to review
          {pendingCount > 0 && <b className="notes-count">{pendingCount}</b>}
        </h3>
        {pending.length === 0 ? (
          <p className="muted-copy">
            {pendingCount > 0 ? "Loading proposals…" : "Nothing to review."}
          </p>
        ) : (
          <ul className="note-proposal-list">
            {pending.map((item) => (
              <li key={item.id}>
                <Link to={noteProposalPath(item.id)}>
                  <span className="mono">{item.name}.md</span>
                  {item.delete ? " (delete)" : ""}
                </Link>
                <span className="note-row-meta">
                  <span>{proposalSourceLabel(item.source, item.origin)}</span>
                  <span>{proposalStateLabels[item.state]}</span>
                  <span>{relativeTime(item.createdAt)}</span>
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </NotesWorkspace>
  );
}
