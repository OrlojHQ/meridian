import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { normalizeAPIError } from "../api/client";
import type { NoteRevision } from "../api/generated/types.gen";
import { formatWhen } from "./routineText";
import { NoteDiff } from "./NoteDiff";
import type { NoteScope } from "./noteScope";
import { formatBytes, revisionProvenance } from "./notesText";
import { Button } from "./ui/Button";
import { Select } from "./ui/Field";

function useRevisionContent(scope: NoteScope, name: string, revision: NoteRevision | undefined) {
  return useQuery({
    queryKey: [scope.tier, scope.id, "note-revision", name, revision?.revision],
    queryFn: ({ signal }) => scope.read(name, revision?.revision, signal),
    // A delete revision has no content of its own.
    enabled: Boolean(revision?.retained && revision.kind !== "delete"),
    staleTime: Infinity,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
}

// NoteHistory lists a note's revisions with their content-free provenance
// and diffs any two retained revisions. Restore writes a new revision with
// an older revision's content against the current head (ADR 0060 section 7).
export function NoteHistory({
  scope,
  name,
  readOnly,
  onClose,
}: {
  scope: NoteScope;
  name: string;
  readOnly: boolean;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const history = useQuery({
    queryKey: [scope.tier, scope.id, "note-history", name],
    queryFn: ({ signal }) => scope.revisions(name, signal),
  });
  const items = history.data?.items ?? [];
  const comparable = items.filter((item) => item.retained && item.kind !== "delete");
  const [fromRevision, setFrom] = useState<number>();
  const [toRevision, setTo] = useState<number>();
  const to = comparable.find((item) => item.revision === toRevision) ?? comparable[0];
  const from = comparable.find((item) => item.revision === fromRevision) ?? comparable[1];
  const fromContent = useRevisionContent(scope, name, from);
  const toContent = useRevisionContent(scope, name, to);
  const restore = useMutation({
    mutationFn: (revision: number) => {
      if (!history.data) throw new Error("History is not loaded");
      return scope.restore(name, { revision, expectedRevision: history.data.note.revision });
    },
    onSettled: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: [scope.tier, scope.id, "note-history", name] }),
        queryClient.invalidateQueries({ queryKey: scope.listKey }),
      ]);
    },
  });
  const head = history.data?.note.revision;
  return (
    <section className="note-history" aria-label={`History of ${name}.md`}>
      <header className="note-editor-header">
        <h3>History of <span className="mono">{name}.md</span></h3>
        <Button onClick={onClose}>Back to notes</Button>
      </header>
      {history.isPending && <p role="status">Loading history…</p>}
      {history.isError && <p role="alert" className="inline-error">{normalizeAPIError(history.error).message}</p>}
      {history.data && (
        <>
          <ol className="note-revisions">
            {items.map((item) => (
              <li key={item.revision} className={item.revision === head ? "head" : undefined}>
                <span className="note-revision-number mono">r{item.revision}</span>
                <span className="note-revision-what">{revisionProvenance(item)}</span>
                <span className="note-revision-meta">
                  {formatWhen(item.createdAt)} · {formatBytes(item.size)}
                  {!item.retained && " · content no longer kept"}
                  {item.revision === head && " · current"}
                </span>
                {!readOnly && item.retained && item.kind !== "delete" && item.revision !== head && (
                  <Button
                    disabled={restore.isPending}
                    onClick={() => restore.mutate(item.revision)}
                    aria-label={`Restore revision ${item.revision}`}
                  >
                    Restore
                  </Button>
                )}
              </li>
            ))}
          </ol>
          {restore.isError && (
            <p role="alert" className="inline-error">
              {normalizeAPIError(restore.error).status === 409
                ? "The note changed while you were looking. The history is reloaded; try again."
                : normalizeAPIError(restore.error).message}
            </p>
          )}
          {restore.isSuccess && <p role="status">Restored as revision {restore.data.revision}.</p>}
          {comparable.length >= 2 && from && to ? (
            <div className="note-history-compare">
              <div className="note-history-pickers">
                <label>
                  From
                  <Select value={from.revision} onChange={(event) => setFrom(Number(event.target.value))}>
                    {comparable.map((item) => (
                      <option key={item.revision} value={item.revision}>Revision {item.revision}</option>
                    ))}
                  </Select>
                </label>
                <label>
                  To
                  <Select value={to.revision} onChange={(event) => setTo(Number(event.target.value))}>
                    {comparable.map((item) => (
                      <option key={item.revision} value={item.revision}>Revision {item.revision}</option>
                    ))}
                  </Select>
                </label>
              </div>
              {fromContent.data && toContent.data ? (
                <NoteDiff
                  before={fromContent.data.content}
                  after={toContent.data.content}
                  label={`Revision ${from.revision} against revision ${to.revision}`}
                />
              ) : fromContent.isError || toContent.isError ? (
                <p role="alert" className="inline-error">
                  {normalizeAPIError(fromContent.error ?? toContent.error).message}
                </p>
              ) : (
                <p role="status">Loading revisions…</p>
              )}
            </div>
          ) : (
            <p className="muted-copy">Two retained revisions are needed to compare.</p>
          )}
        </>
      )}
    </section>
  );
}
