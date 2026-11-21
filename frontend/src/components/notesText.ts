import type {
  NoteProposalState,
  NoteRevision,
  NoteRevisionKind,
  NoteSource,
  NoteSummary,
  NotesDelivery,
} from "../api/generated/types.gen";
import { safeInline } from "./threadText";

// Notes (ADR 0060) are Markdown that agents may have written. Everything
// here is pure so the review surfaces can be tested without a browser.

export const NOTE_NAME_PATTERN = /^[a-z0-9][a-z0-9-]{0,47}$/;
export const NOTE_MAX_BYTES = 32 * 1024;

export function validNoteName(name: string) {
  return NOTE_NAME_PATTERN.test(name);
}

export function utf8Bytes(text: string) {
  return new TextEncoder().encode(text).length;
}

// ---------------------------------------------------------------------------
// Hidden characters

// isHiddenCodePoint says whether a code point can hide or reorder text when
// rendered: C0/C1 controls other than tab and newline, every Unicode format
// character (bidirectional embeddings, overrides, isolates and marks,
// zero-width spaces and joiners, the word joiner, the byte order mark, and
// the tag characters used to smuggle ASCII), line and paragraph separators,
// variation selectors, and the blank Hangul fillers.
const FORMAT = /^\p{Cf}$/u;

export function isHiddenCodePoint(codePoint: number) {
  if (codePoint === 0x09 || codePoint === 0x0a) return false;
  if (codePoint <= 0x1f || (codePoint >= 0x7f && codePoint <= 0x9f)) return true;
  if (codePoint === 0x2028 || codePoint === 0x2029) return true;
  if (codePoint >= 0xfe00 && codePoint <= 0xfe0f) return true;
  if (codePoint >= 0xe0100 && codePoint <= 0xe01ef) return true;
  if (codePoint === 0x115f || codePoint === 0x1160 || codePoint === 0x3164 || codePoint === 0xffa0) {
    return true;
  }
  // Explicit, so the review guarantee does not depend on the Unicode tables
  // of the browser running it.
  if (codePoint >= 0x202a && codePoint <= 0x202e) return true;
  if (codePoint >= 0x2066 && codePoint <= 0x2069) return true;
  if (codePoint >= 0x200b && codePoint <= 0x200f) return true;
  if (codePoint === 0x061c || codePoint === 0x2060 || codePoint === 0xfeff) return true;
  return FORMAT.test(String.fromCodePoint(codePoint));
}

export function escapeCodePoint(codePoint: number) {
  return `\\u{${codePoint.toString(16).toUpperCase().padStart(4, "0")}}`;
}

export type EscapedPart = { text: string; hidden: boolean };

// escapeHiddenParts splits text into visible runs and visible escapes such
// as \u{202E}, so a page can style the escapes. Lone surrogates are escaped
// too.
export function escapeHiddenParts(text: string): EscapedPart[] {
  const parts: EscapedPart[] = [];
  let visible = "";
  for (const char of text) {
    const codePoint = char.codePointAt(0) ?? 0;
    const lone = codePoint >= 0xd800 && codePoint <= 0xdfff;
    if (lone || isHiddenCodePoint(codePoint)) {
      if (visible) parts.push({ text: visible, hidden: false });
      visible = "";
      parts.push({ text: escapeCodePoint(codePoint), hidden: true });
    } else {
      visible += char;
    }
  }
  if (visible) parts.push({ text: visible, hidden: false });
  return parts;
}

export function escapeHiddenCharacters(text: string) {
  return escapeHiddenParts(text).map((part) => part.text).join("");
}

export function hasHiddenCharacters(text: string) {
  return escapeHiddenParts(text).some((part) => part.hidden);
}

// ---------------------------------------------------------------------------
// Line diff

export type DiffLine =
  | { kind: "same"; text: string; oldLine: number; newLine: number }
  | { kind: "remove"; text: string; oldLine: number }
  | { kind: "add"; text: string; newLine: number };

export function splitLines(text: string) {
  if (text === "") return [];
  const lines = text.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}

// Above this many cells the middle of a diff is shown as a whole
// replacement rather than aligned line by line.
const MAX_DIFF_CELLS = 4_000_000;

// lineDiff aligns two texts by their longest common subsequence of lines.
// A note is at most 32 KiB, so a quadratic table over the part that differs
// is small; common leading and trailing lines are matched first.
export function lineDiff(before: string, after: string): DiffLine[] {
  const a = splitLines(before);
  const b = splitLines(after);
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start += 1;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA -= 1;
    endB -= 1;
  }
  const out: DiffLine[] = [];
  for (let index = 0; index < start; index += 1) {
    out.push({ kind: "same", text: a[index], oldLine: index + 1, newLine: index + 1 });
  }
  const midA = a.slice(start, endA);
  const midB = b.slice(start, endB);
  const rows = midA.length;
  const cols = midB.length;
  if (rows * cols > MAX_DIFF_CELLS) {
    midA.forEach((text, index) => out.push({ kind: "remove", text, oldLine: start + index + 1 }));
    midB.forEach((text, index) => out.push({ kind: "add", text, newLine: start + index + 1 }));
  } else {
    // lcs[i][j]: common lines of midA[i:] and midB[j:].
    const width = cols + 1;
    const lcs = new Uint32Array((rows + 1) * width);
    for (let i = rows - 1; i >= 0; i -= 1) {
      for (let j = cols - 1; j >= 0; j -= 1) {
        lcs[i * width + j] = midA[i] === midB[j]
          ? lcs[(i + 1) * width + j + 1] + 1
          : Math.max(lcs[(i + 1) * width + j], lcs[i * width + j + 1]);
      }
    }
    let i = 0;
    let j = 0;
    while (i < rows || j < cols) {
      if (i < rows && j < cols && midA[i] === midB[j]) {
        out.push({ kind: "same", text: midA[i], oldLine: start + i + 1, newLine: start + j + 1 });
        i += 1;
        j += 1;
      } else if (i < rows && (j === cols || lcs[(i + 1) * width + j] >= lcs[i * width + j + 1])) {
        // Removals come before additions, as in a unified diff.
        out.push({ kind: "remove", text: midA[i], oldLine: start + i + 1 });
        i += 1;
      } else {
        out.push({ kind: "add", text: midB[j], newLine: start + j + 1 });
        j += 1;
      }
    }
  }
  for (let index = 0; index < a.length - endA; index += 1) {
    out.push({
      kind: "same",
      text: a[endA + index],
      oldLine: endA + index + 1,
      newLine: endB + index + 1,
    });
  }
  return out;
}

export function diffStat(lines: DiffLine[]) {
  return {
    added: lines.filter((line) => line.kind === "add").length,
    removed: lines.filter((line) => line.kind === "remove").length,
  };
}

// ---------------------------------------------------------------------------
// Sizes and labels

export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  const kib = bytes / 1024;
  return `${kib < 10 ? kib.toFixed(1) : Math.round(kib)} KiB`;
}

// A rough count for an agent that reads every note: about four bytes a
// token for English Markdown.
export function estimateTokens(bytes: number) {
  return Math.ceil(bytes / 4);
}

export function notesBudget(totalBytes: number, maxBytes: number) {
  const tokens = estimateTokens(totalBytes);
  return `${formatBytes(totalBytes)} of ${formatBytes(maxBytes)} · about ${tokens.toLocaleString("en-US")} tokens to read every note`;
}

export const notesDeliveryLabels: Record<NotesDelivery, string> = {
  all_threads: "Every Thread",
  leads_only: "Lead Threads and their subagents",
  off: "No Thread",
};

// The new-Thread dialog's "Include Project notes" default follows the
// Project's notesDelivery. undefined: the choice is not offered.
export function includeNotesDefault(delivery: NotesDelivery | undefined, lead: boolean) {
  if (delivery === undefined || delivery === "off") return undefined;
  return delivery === "all_threads" ? true : lead;
}

// includeNotesRequest is what a new Thread sends: nothing when the
// operator kept the Project's default.
export function includeNotesRequest(
  delivery: NotesDelivery | undefined,
  lead: boolean,
  checked: boolean,
) {
  const fallback = includeNotesDefault(delivery, lead);
  if (fallback === undefined || fallback === checked) return undefined;
  return checked;
}

export const proposalStateLabels: Record<NoteProposalState, string> = {
  pending: "Pending review",
  approved: "Approved",
  approved_with_edits: "Approved with your edits",
  rejected: "Rejected",
  stale: "Stale: the note changed since it was proposed",
  expired: "Expired",
};

export const revisionKindLabels: Record<NoteRevisionKind, string> = {
  operator_write: "Written by you",
  agent_write: "Written by an agent",
  approved_proposal: "Approved proposal",
  approved_with_edits: "Approved with your edits",
  restore: "Restored",
  delete: "Deleted",
};

// Who wrote a note's head: a named subagent, "Lead" for the lead itself, or
// "You". Names are bounded identifiers, never note text.
export function noteWriter(note: Pick<NoteSummary, "writer" | "linkName">) {
  if (note.writer === "operator") return "You";
  return note.linkName ? note.linkName : "Lead";
}

export function revisionProvenance(revision: Pick<NoteRevision, "writer" | "source" | "kind" | "restoredFrom" | "baseRevision">) {
  const parts: string[] = [revisionKindLabels[revision.kind]];
  if (revision.kind === "restore" && revision.restoredFrom) parts.push(`from revision ${revision.restoredFrom}`);
  const source = revision.source;
  if (source?.linkName) parts.push(`by ${source.linkName}`);
  else if (revision.writer === "agent") parts.push("by the lead");
  if (source?.workingNoteName) {
    parts.push(
      `from working note ${source.workingNoteName}${source.workingNoteRevision ? ` revision ${source.workingNoteRevision}` : ""}`,
    );
  }
  if (revision.baseRevision > 0) parts.push(`on revision ${revision.baseRevision}`);
  return parts.join(" · ");
}

// sourceThreadId is the Thread a proposal or promotion came from.
export function sourceThreadId(source: NoteSource | undefined) {
  return source?.threadId ?? source?.leadThreadId;
}

const MAX_PROPOSER_NAME = 64;

// leadCapsuleId is the lead Capsule whose name a proposal's label may show:
// none when a subagent proposed or wrote it, since its link name says who.
export function leadCapsuleId(source: NoteSource | undefined) {
  if (!source || source.linkName) return "";
  return source.capsuleId ?? source.workingCapsuleId ?? "";
}

// proposerName names the proposing or writing Thread with bounded text only
// (ADR 0060 section 7): a subagent's link name, which follows the name
// grammar, or the lead's Capsule name, made inline-safe and cut short. No
// role word precedes a subagent's name, so a name such as "subagent-2" is
// not doubled.
export function proposerName(source: NoteSource | undefined, leadName?: string) {
  if (source?.linkName) return `“${source.linkName}”`;
  let name = safeInline(leadName ?? "").trim();
  if (name.length > MAX_PROPOSER_NAME) name = `${name.slice(0, MAX_PROPOSER_NAME - 1)}…`;
  return name ? `the lead “${name}”` : "a lead Thread";
}

// proposalSourceLabel says where a proposal came from without any text an
// agent wrote.
export function proposalSourceLabel(
  source: NoteSource | undefined,
  origin: "agent" | "promotion",
  leadName?: string,
) {
  const who = proposerName(source, leadName);
  return origin === "promotion"
    ? `Promoted from a working note by ${who}`
    : `Proposed by ${who}`;
}

export function notesUpdatedLabel(changed: number) {
  return `Project notes updated: ${changed} changed`;
}
