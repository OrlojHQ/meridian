import { useQuery } from "@tanstack/react-query";
import { NavLink } from "react-router-dom";

import type { NoteProposalSummary, Project } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { noteProposalPath } from "./ProjectNotes";

// pendingProposalLine names the Project, the note and the proposing Thread
// only. It never quotes proposal or note text, so agent text cannot reach the
// operator before the review page frames it as agent text (ADR 0060
// section 7). The Thread is named by its bounded link name or its role.
export function pendingProposalLine(
  proposal: Pick<NoteProposalSummary, "name" | "source" | "delete">,
  projectName: string | undefined,
) {
  const thread = proposal.source?.linkName ? `subagent ${proposal.source.linkName}` : "a lead Thread";
  return {
    note: `${proposal.name}.md${proposal.delete ? " (delete)" : ""}`,
    detail: `${projectName ?? "A Project"} · from ${thread}`,
  };
}

// NotesToReview is the Inbox's "Notes to review" section. A pending proposal
// raises no attention and no alert; it is only listed here and on the
// Project's Notes tab.
export function NotesToReview({ projects }: { projects: Project[] }) {
  const pending = useQuery(queries.pendingNoteProposals());
  const items = (pending.data?.items ?? []).filter((item) => item.state === "pending");
  if (items.length === 0) return null;
  const names = new Map(projects.map((project) => [project.id, project.name]));
  return (
    <section className="capsule-group notes-to-review" aria-label="Notes to review">
      <h2>
        Notes to review
        <b className="notes-to-review-count" aria-label={`${items.length} to review`}>{items.length}</b>
      </h2>
      <ul>
        {items.map((item) => {
          const line = pendingProposalLine(item, names.get(item.projectId));
          return (
            <li key={item.id}>
              <NavLink to={noteProposalPath(item.id)} className="capsule-row note-review-row">
                <span className="note-review-dot" aria-hidden="true" />
                <span className="capsule-row-body">
                  <span className="capsule-row-top">
                    <strong className="mono">{line.note}</strong>
                  </span>
                  <small>{line.detail}</small>
                </span>
              </NavLink>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
