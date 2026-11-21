import { describe, expect, it } from "vitest";

import type { ThreadBlock } from "../api/generated/types.gen";
import { appendToNote } from "../components/AddToProjectNotes";
import { pendingProposalLine } from "../components/NotesToReview";
import {
  diffStat,
  escapeHiddenCharacters,
  escapeHiddenParts,
  estimateTokens,
  formatBytes,
  hasHiddenCharacters,
  includeNotesDefault,
  includeNotesRequest,
  lineDiff,
  notesBudget,
  notesUpdatedLabel,
  noteWriter,
  revisionProvenance,
  splitLines,
  validNoteName,
} from "../components/notesText";
import { buildThreadChat, notesChangedCount } from "../components/threadChat";
import { workingNotesWritable } from "../components/WorkingNotes";

describe("hidden character escapes", () => {
  it("shows bidirectional overrides, isolates and marks as escapes", () => {
    expect(escapeHiddenCharacters("abc‮def")).toBe("abc\\u{202E}def");
    for (const codePoint of [0x202a, 0x202b, 0x202c, 0x202d, 0x202e, 0x2066, 0x2067, 0x2068, 0x2069, 0x200e, 0x200f, 0x061c]) {
      expect(escapeHiddenCharacters(String.fromCodePoint(codePoint))).toBe(
        `\\u{${codePoint.toString(16).toUpperCase().padStart(4, "0")}}`,
      );
    }
  });

  it("shows zero-width characters as escapes", () => {
    expect(escapeHiddenCharacters("a​b‌c‍d⁠e﻿f")).toBe(
      "a\\u{200B}b\\u{200C}c\\u{200D}d\\u{2060}e\\u{FEFF}f",
    );
  });

  it("shows control characters except tab and newline", () => {
    expect(escapeHiddenCharacters("a\tb\nc")).toBe("a\tb\nc");
    expect(escapeHiddenCharacters("bell\u0007 esc\u001B[31m cr\r del\u007F c1\u0085")).toBe(
      "bell\\u{0007} esc\\u{001B}[31m cr\\u{000D} del\\u{007F} c1\\u{0085}",
    );
    expect(escapeHiddenCharacters("\u0000")).toBe("\\u{0000}");
  });

  it("shows tag characters and lone surrogates that could smuggle text", () => {
    expect(escapeHiddenCharacters("x\u{E0041}\u{E0042}")).toBe("x\\u{E0041}\\u{E0042}");
    expect(escapeHiddenCharacters("a\uD800b")).toBe("a\\u{D800}b");
    expect(escapeHiddenCharacters("line break")).toBe("line\\u{2028}break");
  });

  it("leaves ordinary text, accents and other scripts alone", () => {
    const text = "Run `make test` — café, naïve, 日本語, עברית, emoji 🙂";
    expect(escapeHiddenCharacters(text)).toBe(text);
    expect(hasHiddenCharacters(text)).toBe(false);
  });

  it("marks which parts are escapes", () => {
    expect(escapeHiddenParts("ok‮ko")).toEqual([
      { text: "ok", hidden: false },
      { text: "\\u{202E}", hidden: true },
      { text: "ko", hidden: false },
    ]);
    expect(hasHiddenCharacters("ok​ko")).toBe(true);
  });
});

describe("line diff", () => {
  it("splits lines without a trailing empty line", () => {
    expect(splitLines("")).toEqual([]);
    expect(splitLines("a\nb\n")).toEqual(["a", "b"]);
    expect(splitLines("a\n\nb")).toEqual(["a", "", "b"]);
  });

  it("returns only unchanged lines for equal text", () => {
    const lines = lineDiff("a\nb\n", "a\nb\n");
    expect(lines.every((line) => line.kind === "same")).toBe(true);
    expect(diffStat(lines)).toEqual({ added: 0, removed: 0 });
  });

  it("aligns insertions, deletions and replacements", () => {
    const lines = lineDiff("one\ntwo\nthree\nfour", "one\n2\nthree\nfour\nfive");
    expect(lines).toEqual([
      { kind: "same", text: "one", oldLine: 1, newLine: 1 },
      { kind: "remove", text: "two", oldLine: 2 },
      { kind: "add", text: "2", newLine: 2 },
      { kind: "same", text: "three", oldLine: 3, newLine: 3 },
      { kind: "same", text: "four", oldLine: 4, newLine: 4 },
      { kind: "add", text: "five", newLine: 5 },
    ]);
  });

  it("treats a new note as all additions and a deletion as all removals", () => {
    expect(diffStat(lineDiff("", "a\nb"))).toEqual({ added: 2, removed: 0 });
    expect(diffStat(lineDiff("a\nb", ""))).toEqual({ added: 0, removed: 2 });
  });

  it("keeps a minimal edit inside long unchanged text", () => {
    const before = Array.from({ length: 200 }, (_, index) => `line ${index}`).join("\n");
    const after = before.replace("line 100", "line one hundred");
    expect(diffStat(lineDiff(before, after))).toEqual({ added: 1, removed: 1 });
  });

  it("numbers lines on both sides after a change in the middle", () => {
    const lines = lineDiff("a\nb\nc", "a\nx\ny\nc");
    const last = lines[lines.length - 1];
    expect(last).toEqual({ kind: "same", text: "c", oldLine: 3, newLine: 4 });
  });
});

describe("notes labels", () => {
  it("validates note names", () => {
    expect(validNoteName("branch-naming")).toBe(true);
    expect(validNoteName("-bad")).toBe(false);
    expect(validNoteName("Bad")).toBe(false);
    expect(validNoteName("a".repeat(49))).toBe(false);
  });

  it("estimates tokens at about four bytes each and shows the budget", () => {
    expect(estimateTokens(0)).toBe(0);
    expect(estimateTokens(4097)).toBe(1025);
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(131072)).toBe("128 KiB");
    expect(notesBudget(8192, 131072)).toBe("8.0 KiB of 128 KiB · about 2,048 tokens to read every note");
  });

  it("defaults Include Project notes from the Project setting", () => {
    expect(includeNotesDefault("all_threads", false)).toBe(true);
    expect(includeNotesDefault("leads_only", false)).toBe(false);
    expect(includeNotesDefault("leads_only", true)).toBe(true);
    expect(includeNotesDefault("off", true)).toBeUndefined();
    expect(includeNotesDefault(undefined, true)).toBeUndefined();
  });

  it("sends the choice only when it differs from the default", () => {
    expect(includeNotesRequest("all_threads", false, true)).toBeUndefined();
    expect(includeNotesRequest("all_threads", false, false)).toBe(false);
    expect(includeNotesRequest("leads_only", false, true)).toBe(true);
    expect(includeNotesRequest("leads_only", true, true)).toBeUndefined();
    expect(includeNotesRequest("off", false, true)).toBeUndefined();
  });

  it("names writers by role or bounded link name", () => {
    expect(noteWriter({ writer: "operator" })).toBe("You");
    expect(noteWriter({ writer: "agent", linkName: "api-tests" })).toBe("api-tests");
    expect(noteWriter({ writer: "agent" })).toBe("Lead");
    expect(
      revisionProvenance({
        kind: "approved_proposal", writer: "agent", baseRevision: 2,
        source: { linkName: "scout", workingNoteName: "findings", workingNoteRevision: 4 },
      }),
    ).toBe("Approved proposal · by scout · from working note findings revision 4 · on revision 2");
  });

  it("keeps working notes writable only while the grant is active", () => {
    expect(workingNotesWritable({ state: "active" })).toBe(true);
    expect(workingNotesWritable({ state: "lapsed" })).toBe(false);
    expect(workingNotesWritable({ state: "revoked" })).toBe(false);
    expect(workingNotesWritable(undefined)).toBe(false);
  });

  it("appends a transcript excerpt after a note", () => {
    expect(appendToNote("# Notes\n\n", "  new fact \n")).toBe("# Notes\n\nnew fact\n");
    expect(appendToNote("", "fact")).toBe("fact\n");
  });
});

describe("notes in the Inbox", () => {
  it("names the note, Project and Thread only", () => {
    const line = pendingProposalLine(
      { name: "branch-naming", delete: false, source: { threadId: "thread-1", linkName: "scout" } },
      "meridian",
    );
    expect(line).toEqual({ note: "branch-naming.md", detail: "meridian · from subagent scout" });
    expect(pendingProposalLine({ name: "old", delete: true }, undefined)).toEqual({
      note: "old.md (delete)",
      detail: "A Project · from a lead Thread",
    });
  });
});

describe("notes_updated transcript divider", () => {
  const block = (event: Record<string, unknown>): ThreadBlock =>
    ({
      id: "block-1",
      messageId: "message-1",
      messageSequence: 1,
      sequence: 1,
      role: "system",
      messageKind: "event",
      kind: "json",
      event,
    }) as unknown as ThreadBlock;

  it("becomes a content-free divider with the changed count", () => {
    const chat = buildThreadChat([block({ type: "notes_updated", notesChanged: 2 })], undefined, new Map());
    expect(chat.items).toEqual([{ kind: "notes_updated", key: "block-1", changed: 2 }]);
    expect(notesUpdatedLabel(2)).toBe("Project notes updated: 2 changed");
  });

  it("ignores anything but a small whole count", () => {
    expect(notesChangedCount({ notesChanged: "2 <script>" })).toBe(0);
    expect(notesChangedCount({ notesChanged: -1 })).toBe(0);
    expect(notesChangedCount({ notesChanged: 1.5 })).toBe(0);
    expect(notesChangedCount({})).toBe(0);
  });
});
