import { useId, useState, type FormEvent } from "react";

import { MeridianAPIError, normalizeAPIError } from "../api/client";
import { NoteDiff } from "./NoteDiff";
import { NoteMarkdown } from "./NoteMarkdown";
import { formatBytes, NOTE_MAX_BYTES, utf8Bytes, validNoteName } from "./notesText";
import { Button } from "./ui/Button";
import { Input, TextArea } from "./ui/Field";
import { Tabs } from "./ui/Tabs";

export type NoteDraft = { name: string; content: string; expectedRevision: number };

export type NoteHead = { content: string; revision: number };

export function isConflict(error: unknown) {
  const failure = error instanceof MeridianAPIError ? error : normalizeAPIError(error);
  return failure.status === 409 || failure.code === "conflict";
}

// NoteEditor edits one note as Markdown source in a plain textarea, with a
// rendered preview on a second tab (ADR 0060 section 7). Saving sends the
// revision the editor opened as expectedRevision, so a second tab or an
// agent that wrote in between gets a conflict instead of being overwritten.
// On a conflict the text stays here; loadCurrent shows what changed so the
// operator can merge by hand and save against the new head.
export function NoteEditor({
  heading,
  initialName = "",
  nameEditable,
  initialContent,
  revision,
  existingNames = [],
  saveLabel = "Save note",
  notice,
  onSave,
  onCancel,
  loadCurrent,
}: {
  heading: string;
  initialName?: string;
  nameEditable: boolean;
  initialContent: string;
  // The note's current revision when the editor opened; 0 creates it.
  revision: number;
  existingNames?: string[];
  saveLabel?: string;
  notice?: string;
  onSave: (draft: NoteDraft) => Promise<unknown>;
  onCancel: () => void;
  loadCurrent?: (name: string) => Promise<NoteHead>;
}) {
  const id = useId();
  const [name, setName] = useState(initialName);
  const [content, setContent] = useState(initialContent);
  const [base, setBase] = useState(revision);
  const [view, setView] = useState("source");
  const [error, setError] = useState("");
  const [conflict, setConflict] = useState<NoteHead | "unknown">();
  const [saving, setSaving] = useState(false);
  const bytes = utf8Bytes(content);
  const tooLarge = bytes > NOTE_MAX_BYTES;

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    const trimmed = name.trim();
    if (!validNoteName(trimmed)) {
      setError("Use 1 to 48 lowercase letters, digits or dashes, starting with a letter or digit.");
      return;
    }
    if (nameEditable && base === 0 && existingNames.includes(trimmed)) {
      setError(`A note named ${trimmed} already exists. Open it to edit, or choose another name.`);
      return;
    }
    if (tooLarge) {
      setError(`A note holds at most ${formatBytes(NOTE_MAX_BYTES)}.`);
      return;
    }
    setSaving(true);
    try {
      await onSave({ name: trimmed, content, expectedRevision: base });
      setConflict(undefined);
    } catch (failure) {
      if (isConflict(failure)) {
        let head: NoteHead | "unknown" = "unknown";
        if (loadCurrent) {
          try {
            head = await loadCurrent(trimmed);
          } catch {
            head = "unknown";
          }
        }
        setConflict(head);
      } else {
        setError(normalizeAPIError(failure).message);
      }
    } finally {
      setSaving(false);
    }
  }

  return (
    <form className="note-editor" onSubmit={(event) => void submit(event)} aria-label={heading}>
      <header className="note-editor-header">
        <h3>{heading}</h3>
        <Tabs
          label="Editor view"
          value={view}
          onChange={setView}
          tabs={[
            { id: "source", label: "Markdown" },
            { id: "preview", label: "Preview" },
          ]}
        />
      </header>
      {notice && <p className="launch-hint">{notice}</p>}
      <div className="note-editor-name">
        <label htmlFor={`${id}-name`}>Name</label>
        <span className="note-name-field">
          <Input
            id={`${id}-name`}
            value={name}
            readOnly={!nameEditable}
            maxLength={48}
            placeholder="branch-naming"
            className="mono"
            onChange={(event) => setName(event.target.value.toLowerCase())}
          />
          <span className="mono note-suffix" aria-hidden="true">.md</span>
        </span>
      </div>
      {view === "source" ? (
        <TextArea
          aria-label="Note Markdown"
          className="note-editor-source mono"
          rows={14}
          spellCheck={false}
          value={content}
          onChange={(event) => setContent(event.target.value)}
        />
      ) : (
        <div className="note-editor-preview" aria-label="Rendered preview">
          {content.trim() === "" ? <p className="muted-copy">Nothing to preview.</p> : <NoteMarkdown text={content} />}
          <p className="launch-hint">The preview loads no images and follows no links.</p>
        </div>
      )}
      <p className={`note-editor-size${tooLarge ? " over" : ""}`}>
        {formatBytes(bytes)} of {formatBytes(NOTE_MAX_BYTES)}
        {base > 0 ? ` · editing revision ${base}` : " · new note"}
      </p>
      {conflict && (
        <div className="note-conflict" role="alert">
          <p>
            <strong>This note changed since you opened it.</strong>{" "}
            {conflict === "unknown"
              ? "Your text is still here. Reopen the note to see the current version before saving again."
              : `Someone saved revision ${conflict.revision}. Your text is still here; review what changed below, merge by hand, then save it against revision ${conflict.revision}.`}
          </p>
          {conflict !== "unknown" && (
            <>
              <p className="launch-hint">Current revision {conflict.revision} → your text</p>
              <NoteDiff before={conflict.content} after={content} label="Current note against your text" />
              <div className="note-conflict-actions">
                <Button
                  onClick={() => {
                    setBase(conflict.revision);
                    setConflict(undefined);
                  }}
                >
                  Keep my text against revision {conflict.revision}
                </Button>
                <Button
                  onClick={() => {
                    setContent(conflict.content);
                    setBase(conflict.revision);
                    setConflict(undefined);
                  }}
                >
                  Discard mine and load revision {conflict.revision}
                </Button>
              </div>
            </>
          )}
        </div>
      )}
      {error && <p className="inline-error" role="alert">{error}</p>}
      <div className="note-editor-actions">
        <Button type="submit" variant="primary" disabled={saving || tooLarge || conflict !== undefined}>
          {saving ? "Saving…" : saveLabel}
        </Button>
        <Button onClick={onCancel} disabled={saving}>Cancel</Button>
      </div>
    </form>
  );
}
