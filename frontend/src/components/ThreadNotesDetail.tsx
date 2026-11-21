import { useQuery } from "@tanstack/react-query";

import type { ThreadNotes } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { formatBytes } from "./notesText";
import { formatWhen } from "./routineText";

const tierLabels = { project: "Project note", working: "Working note" } as const;

const choiceLabels: Record<ThreadNotes["choice"], string> = {
  "": "Follows the Project setting",
  include: "Included for this Thread",
  exclude: "Left out for this Thread",
};

// ThreadNotesDetail shows, in Thread details, the content-free note set the
// latest session received (ADR 0060 section 4): tier, name and revision.
export function ThreadNotesDetail({ threadId }: { threadId: string }) {
  const notes = useQuery(queries.threadNotes(threadId));
  if (notes.isPending || notes.isError) return null;
  const { delivered, indexed, choice, deliveredAt } = notes.data;
  return (
    <section className="thread-notes-detail" aria-label="Notes this session received">
      <h3>Notes</h3>
      <p className="launch-hint">
        {choiceLabels[choice]}
        {deliveredAt ? ` · delivered ${formatWhen(deliveredAt)}` : ""}
      </p>
      {delivered.length === 0 ? (
        <p className="muted-copy">The latest session received no notes.</p>
      ) : (
        <>
          <ul className="thread-notes-list">
            {delivered.map((note) => (
              <li key={`${note.tier}:${note.name}`}>
                <span>{tierLabels[note.tier]}</span>
                <span className="mono">{note.name}.md</span>
                <span>r{note.revision}</span>
                <span>{formatBytes(note.size)}</span>
              </li>
            ))}
          </ul>
          {!indexed && (
            <p className="notice" role="status">This harness is not told about these files.</p>
          )}
        </>
      )}
    </section>
  );
}
