import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { api, normalizeAPIError } from "../api/client";
import type { Thread } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { NoteEditor, type NoteDraft } from "./NoteEditor";
import { ActionsMenu } from "./ui/ActionsMenu";
import { Dialog } from "./ui/Dialog";
import { Select } from "./ui/Field";

// selectionWithin returns the text selected inside one element, or "" when
// the selection is empty or reaches outside it.
export function selectionWithin(element: Element | null) {
  const selection = typeof window === "undefined" ? null : window.getSelection();
  if (!element || !selection || selection.isCollapsed || selection.rangeCount === 0) return "";
  if (!selection.anchorNode || !selection.focusNode) return "";
  if (!element.contains(selection.anchorNode) || !element.contains(selection.focusNode)) return "";
  return selection.toString();
}

// appendToNote adds a transcript excerpt after a note's current text.
export function appendToNote(current: string, excerpt: string) {
  const head = current.replace(/\s+$/, "");
  const tail = excerpt.replace(/^\s+|\s+$/g, "");
  return head ? `${head}\n\n${tail}\n` : `${tail}\n`;
}

function ExistingNoteEditor({
  projectId,
  name,
  excerpt,
  onSave,
  onCancel,
}: {
  projectId: string;
  name: string;
  excerpt: string;
  onSave: (draft: NoteDraft) => Promise<unknown>;
  onCancel: () => void;
}) {
  const note = useQuery({
    queryKey: ["project", projectId, "note", name],
    queryFn: ({ signal }) => api.projectNote(projectId, name, undefined, signal),
    staleTime: Infinity,
    gcTime: 0,
    refetchOnWindowFocus: false,
  });
  if (note.isPending) return <p role="status">Loading {name}.md…</p>;
  if (note.isError) return <p role="alert" className="inline-error">{normalizeAPIError(note.error).message}</p>;
  return (
    <NoteEditor
      heading={`Add to ${name}.md`}
      initialName={name}
      nameEditable={false}
      initialContent={appendToNote(note.data.content, excerpt)}
      revision={note.data.note.revision}
      notice="The excerpt is added at the end. Nothing is saved until you save."
      onSave={onSave}
      onCancel={onCancel}
      loadCurrent={async (current) => {
        const head = await api.projectNote(projectId, current);
        return { content: head.content, revision: head.note.revision };
      }}
    />
  );
}

// AddToProjectNotesDialog opens the note editor with a transcript excerpt
// (ADR 0060 section 7). Nothing is saved until the operator saves, and the
// revision records the Thread the excerpt came from.
export function AddToProjectNotesDialog({
  projectId,
  thread,
  excerpt,
  onClose,
}: {
  projectId: string;
  thread: Pick<Thread, "id">;
  excerpt: string;
  onClose: () => void;
}) {
  const titleId = useId();
  const targetId = useId();
  const queryClient = useQueryClient();
  const notes = useQuery(queries.projectNotes(projectId));
  const [target, setTarget] = useState("");
  const [saved, setSaved] = useState("");
  const live = (notes.data?.items ?? []).filter((item) => item.live);
  const save = async (draft: NoteDraft) => {
    await api.putProjectNote(projectId, draft.name, {
      content: draft.content,
      expectedRevision: draft.expectedRevision,
      sourceThreadId: thread.id,
    });
    await queryClient.invalidateQueries({ queryKey: ["project-notes", projectId] });
    setSaved(draft.name);
  };
  return (
    <Dialog
      labelledBy={titleId}
      eyebrow="From this Thread"
      title="Add to Project notes"
      closeLabel="Close without saving"
      onClose={onClose}
      className="add-to-notes-dialog"
    >
      {saved ? (
        <div>
          <p role="status">Saved {saved}.md. New sessions in this Project receive it.</p>
          <div className="dialog-actions">
            <button type="button" className="primary-button" onClick={onClose}>Done</button>
          </div>
        </div>
      ) : (
        <>
          <label className="note-target" htmlFor={targetId}>
            Add to
            <Select id={targetId} value={target} onChange={(event) => setTarget(event.target.value)}>
              <option value="">A new note</option>
              {live.map((item) => (
                <option key={item.name} value={item.name}>{item.name}.md</option>
              ))}
            </Select>
          </label>
          {target === "" ? (
            <NoteEditor
              key="new"
              heading="New Project note"
              nameEditable
              initialContent={excerpt}
              revision={0}
              existingNames={(notes.data?.items ?? []).map((item) => item.name)}
              notice="Nothing is saved until you save. Every Thread that receives Project notes will read it."
              onSave={save}
              onCancel={onClose}
            />
          ) : (
            <ExistingNoteEditor
              key={target}
              projectId={projectId}
              name={target}
              excerpt={excerpt}
              onSave={save}
              onCancel={onClose}
            />
          )}
        </>
      )}
    </Dialog>
  );
}

// MessageNoteActions is a transcript message's menu. "Add to Project notes"
// takes the text selected inside the message, or the whole message.
export function MessageNoteActions({ thread, text }: { thread: Thread; text: string }) {
  const root = useRef<HTMLSpanElement>(null);
  const selected = useRef("");
  const [excerpt, setExcerpt] = useState<string>();
  const capsule = useQuery(queries.capsule(thread.capsuleId));
  const projectId = capsule.data?.projectId;
  if (!projectId) return null;
  const message = () => root.current?.closest("li") ?? null;
  return (
    <span
      className="chat-message-actions"
      ref={root}
      onPointerDownCapture={() => {
        selected.current = selectionWithin(message());
      }}
    >
      <ActionsMenu
        label="Message actions"
        items={[
          {
            label: "Add to Project notes",
            onSelect: () => {
              const chosen = selected.current || selectionWithin(message());
              selected.current = "";
              setExcerpt(chosen.trim() ? chosen : text);
            },
          },
        ]}
      />
      {excerpt !== undefined &&
        // Outside the scrolling transcript, so the dialog covers the page.
        createPortal(
          <AddToProjectNotesDialog
            projectId={projectId}
            thread={thread}
            excerpt={excerpt}
            onClose={() => setExcerpt(undefined)}
          />,
          document.body,
        )}
    </span>
  );
}
