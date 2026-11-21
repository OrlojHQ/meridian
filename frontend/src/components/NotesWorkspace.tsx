import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";

import { normalizeAPIError } from "../api/client";
import type { NoteList, NoteSummary } from "../api/generated/types.gen";
import { NoteEditor, type NoteDraft } from "./NoteEditor";
import { NoteHistory } from "./NoteHistory";
import type { NoteScope } from "./noteScope";
import { formatBytes, noteWriter } from "./notesText";
import { relativeTime } from "../shell/capsuleActivity";
import { Badge } from "./ui/Badge";
import { Button } from "./ui/Button";
import { ActionsMenu, type ActionsMenuItem } from "./ui/ActionsMenu";

type View =
  | { mode: "list" }
  | { mode: "new" }
  | { mode: "edit"; name: string }
  | { mode: "history"; name: string };

export type NoteAction = { label: string; onSelect: (note: NoteSummary) => void; disabled?: boolean };

function EditExisting({
  scope,
  name,
  onSave,
  onCancel,
}: {
  scope: NoteScope;
  name: string;
  onSave: (draft: NoteDraft) => Promise<unknown>;
  onCancel: () => void;
}) {
  const note = useQuery({
    queryKey: [scope.tier, scope.id, "note", name],
    queryFn: ({ signal }) => scope.read(name, undefined, signal),
    staleTime: Infinity,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
  if (note.isPending) return <p role="status">Loading {name}.md…</p>;
  if (note.isError) {
    return (
      <div>
        <p role="alert" className="inline-error">{normalizeAPIError(note.error).message}</p>
        <Button onClick={onCancel}>Back to notes</Button>
      </div>
    );
  }
  return (
    <NoteEditor
      heading={`Edit ${name}.md`}
      initialName={name}
      nameEditable={false}
      initialContent={note.data.content}
      revision={note.data.note.revision}
      notice={note.data.note.agentProposed ? "The current text is agent text. Read it before you keep it." : undefined}
      onSave={onSave}
      onCancel={onCancel}
      loadCurrent={async (current) => {
        const head = await scope.read(current);
        return { content: head.content, revision: head.note.revision };
      }}
    />
  );
}

// NotesWorkspace lists one tier's notes and switches in place between the
// list, the editor and a note's history. The list is content-free; content
// is read only when a note is opened.
export function NotesWorkspace({
  scope,
  list,
  readOnly,
  readOnlyNote,
  title,
  summary,
  emptyText,
  rowActions = [],
  writerLabel = noteWriter,
  children,
}: {
  scope: NoteScope;
  list: NoteList | undefined;
  readOnly: boolean;
  readOnlyNote?: string;
  title: string;
  summary?: ReactNode;
  emptyText: string;
  rowActions?: NoteAction[];
  writerLabel?: (note: NoteSummary) => string;
  children?: ReactNode;
}) {
  const queryClient = useQueryClient();
  const [view, setView] = useState<View>({ mode: "list" });
  const [confirm, setConfirm] = useState<{ note: NoteSummary; withHistory: boolean }>();
  const [confirmShred, setConfirmShred] = useState(false);
  const [shredWord, setShredWord] = useState("");
  const [copied, setCopied] = useState("");
  const refresh = () => queryClient.invalidateQueries({ queryKey: scope.listKey });
  const save = async (draft: NoteDraft) => {
    await scope.write(draft.name, { content: draft.content, expectedRevision: draft.expectedRevision });
    await refresh();
    setView({ mode: "list" });
  };
  const remove = useMutation({
    mutationFn: ({ note, withHistory }: { note: NoteSummary; withHistory: boolean }) =>
      scope.remove(note.name, { expectedRevision: note.revision, ...(withHistory ? { withHistory: true } : {}) }),
    onSuccess: () => setConfirm(undefined),
    onSettled: refresh,
  });
  const shred = useMutation({
    mutationFn: () => scope.shred(),
    onSuccess: () => {
      setConfirmShred(false);
      setShredWord("");
    },
    onSettled: refresh,
  });
  const copy = useMutation({
    mutationFn: async (note: NoteSummary) => {
      const content = await scope.read(note.name);
      await navigator.clipboard.writeText(content.content);
      return note.name;
    },
    onSuccess: (name) => setCopied(name),
  });

  if (view.mode === "new") {
    return (
      <NoteEditor
        heading="New note"
        nameEditable
        initialContent=""
        revision={0}
        existingNames={(list?.items ?? []).map((item) => item.name)}
        onSave={save}
        onCancel={() => setView({ mode: "list" })}
      />
    );
  }
  if (view.mode === "edit") {
    return <EditExisting scope={scope} name={view.name} onSave={save} onCancel={() => setView({ mode: "list" })} />;
  }
  if (view.mode === "history") {
    return <NoteHistory scope={scope} name={view.name} readOnly={readOnly} onClose={() => setView({ mode: "list" })} />;
  }

  const items = list?.items ?? [];
  const live = items.filter((item) => item.live);
  const deleted = items.filter((item) => !item.live);
  const menuFor = (note: NoteSummary): ActionsMenuItem[] => {
    const menu: ActionsMenuItem[] = [];
    if (note.live) menu.push({ label: "Copy as Markdown", onSelect: () => copy.mutate(note) });
    if (note.live && !readOnly) menu.push({ label: "Delete", danger: true, onSelect: () => setConfirm({ note, withHistory: false }) });
    menu.push({ label: "Delete with history", danger: true, onSelect: () => setConfirm({ note, withHistory: true }) });
    return menu;
  };
  const row = (note: NoteSummary) => (
    <li key={note.name} className={`note-row${note.live ? "" : " deleted"}`}>
      <div className="note-row-main">
        <span className="note-row-name mono">{note.name}.md</span>
        {note.agentProposed && (
          <Badge className="note-agent-label">
            {/* Agents write working notes directly; only Project notes are proposed. */}
            {scope.tier === "working" ? "Written by an agent" : "Agent-proposed"}
          </Badge>
        )}
        {!note.live && <Badge>Deleted</Badge>}
      </div>
      <p className="note-row-meta">
        <span>{formatBytes(note.size)}</span>
        <span title={new Date(note.updatedAt).toLocaleString()}>
          Changed {relativeTime(note.updatedAt) === "now" ? "just now" : `${relativeTime(note.updatedAt)} ago`}
        </span>
        <span>By {writerLabel(note)}</span>
        <span>r{note.revision}</span>
      </p>
      <div className="note-row-actions">
        {note.live && !readOnly && (
          <Button onClick={() => setView({ mode: "edit", name: note.name })} aria-label={`Edit ${note.name}.md`}>
            Edit
          </Button>
        )}
        <Button onClick={() => setView({ mode: "history", name: note.name })} aria-label={`History of ${note.name}.md`}>
          History
        </Button>
        {note.live && rowActions.map((action) => (
          <Button
            key={action.label}
            disabled={action.disabled}
            aria-label={`${action.label}: ${note.name}.md`}
            onClick={() => action.onSelect(note)}
          >
            {action.label}
          </Button>
        ))}
        <ActionsMenu label={`More for ${note.name}.md`} items={menuFor(note)} />
      </div>
      {confirm?.note.name === note.name && (
        <div className="note-confirm" role="alertdialog" aria-label={`Delete ${note.name}.md`}>
          <p>
            {confirm.withHistory
              ? `Delete ${note.name}.md and every revision's content? This cannot be undone.${scope.tier === "project" ? " Its pending proposals are closed." : ""}`
              : `Delete ${note.name}.md? It stops being delivered at the next session start. Its history stays, so you can restore it.`}
          </p>
          <div className="note-confirm-actions">
            <Button variant="danger" disabled={remove.isPending} onClick={() => remove.mutate(confirm)}>
              {confirm.withHistory ? "Delete with history" : "Delete"}
            </Button>
            <Button onClick={() => setConfirm(undefined)}>Keep</Button>
          </div>
        </div>
      )}
    </li>
  );
  return (
    <section className="notes-workspace" aria-label={title}>
      <header className="notes-header">
        <h2>{title}</h2>
        {!readOnly && (
          <Button variant="primary" onClick={() => setView({ mode: "new" })}>New note</Button>
        )}
      </header>
      {readOnly && readOnlyNote && <p className="notice" role="status">{readOnlyNote}</p>}
      {summary}
      {children}
      {remove.isError && <p role="alert" className="inline-error">{normalizeAPIError(remove.error).message}</p>}
      {copy.isError && <p role="alert" className="inline-error">The note could not be copied.</p>}
      {copied && <p role="status" className="launch-hint">Copied {copied}.md as Markdown.</p>}
      {live.length === 0 ? (
        <p className="muted-copy notes-empty">{emptyText}</p>
      ) : (
        <ul className="note-list">{live.map(row)}</ul>
      )}
      {deleted.length > 0 && (
        <details className="notes-deleted">
          <summary>Deleted ({deleted.length})</summary>
          <ul className="note-list">{deleted.map(row)}</ul>
        </details>
      )}
      <details className="notes-shred">
        <summary>Crypto-shred</summary>
        <p>
          Makes every revision{scope.tier === "project" ? " and proposal" : ""} unreadable and empties the
          set. Content-free provenance stays. Copies already delivered to a Capsule are not reached.
        </p>
        {confirmShred ? (
          <div className="note-confirm" role="alertdialog" aria-label="Crypto-shred notes">
            <label>
              Type <strong>crypto-shred</strong> to continue
              <input value={shredWord} onChange={(event) => setShredWord(event.target.value)} />
            </label>
            <div className="note-confirm-actions">
              <Button
                variant="danger"
                disabled={shredWord !== "crypto-shred" || shred.isPending}
                onClick={() => shred.mutate()}
              >
                Shred notes permanently
              </Button>
              <Button onClick={() => setConfirmShred(false)}>Cancel</Button>
            </div>
          </div>
        ) : (
          <Button variant="danger" onClick={() => setConfirmShred(true)}>Crypto-shred notes…</Button>
        )}
        {shred.isError && <p role="alert" className="inline-error">{normalizeAPIError(shred.error).message}</p>}
        {shred.isSuccess && <p role="status">Notes shredded.</p>}
      </details>
    </section>
  );
}
