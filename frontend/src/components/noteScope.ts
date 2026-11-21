import { api } from "../api/client";
import type {
  DeleteNoteRequest,
  NoteContent,
  NoteHistory,
  NoteSummary,
  RestoreNoteRequest,
  WriteNoteRequest,
} from "../api/generated/types.gen";

// NoteScope gives the Project-notes tier and one lead tree's working notes
// the same calls, so the editor and history work on either.
export type NoteScope = {
  tier: "project" | "working";
  id: string;
  listKey: readonly unknown[];
  read: (name: string, revision?: number, signal?: AbortSignal) => Promise<NoteContent>;
  write: (name: string, body: WriteNoteRequest) => Promise<NoteSummary>;
  remove: (name: string, body: DeleteNoteRequest) => Promise<NoteSummary>;
  restore: (name: string, body: RestoreNoteRequest) => Promise<NoteSummary>;
  revisions: (name: string, signal?: AbortSignal) => Promise<NoteHistory>;
  shred: () => Promise<void>;
};

export function projectNoteScope(projectId: string): NoteScope {
  return {
    tier: "project",
    id: projectId,
    listKey: ["project-notes", projectId],
    read: (name, revision, signal) => api.projectNote(projectId, name, revision, signal),
    write: (name, body) => api.putProjectNote(projectId, name, body),
    remove: (name, body) => api.deleteProjectNote(projectId, name, body),
    restore: (name, body) => api.restoreProjectNote(projectId, name, body),
    revisions: (name, signal) => api.projectNoteRevisions(projectId, name, signal),
    shred: () => api.shredProjectNotes(projectId),
  };
}

export function workingNoteScope(grantId: string): NoteScope {
  return {
    tier: "working",
    id: grantId,
    listKey: ["working-notes", grantId],
    read: (name, revision, signal) => api.workingNote(grantId, name, revision, signal),
    write: (name, body) => api.putWorkingNote(grantId, name, body),
    remove: (name, body) => api.deleteWorkingNote(grantId, name, body),
    restore: (name, body) => api.restoreWorkingNote(grantId, name, body),
    revisions: (name, signal) => api.workingNoteRevisions(grantId, name, signal),
    shred: () => api.shredWorkingNotes(grantId),
  };
}
