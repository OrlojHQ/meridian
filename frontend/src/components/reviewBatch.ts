import { useCallback, useMemo, useSyncExternalStore } from "react";

import type { DiffFile, DiffLine } from "./DiffViewer";
import { sanitizeThreadText } from "./Threads";

// Pending review comments live only in this module's memory, keyed by
// Capsule. They are never written to browser storage or the server: a
// reload discards them, and they reach an agent only when the operator
// explicitly sends the composed message to a structured Thread.

export type ReviewSide = "old" | "new";

export type ReviewExcerptLine = {
  marker: "+" | "-" | " ";
  text: string;
};

export type ReviewAnchor = {
  path: string;
  side: ReviewSide;
  startLine: number;
  endLine: number;
  excerpt: ReviewExcerptLine[];
  omitted: number;
};

export type ReviewComment = ReviewAnchor & {
  id: string;
  body: string;
};

export const MAX_REVIEW_COMMENTS = 100;
export const MAX_COMMENT_LENGTH = 16 * 1024;
export const MAX_RANGE_LINES = 50;
// Matches the server's structured Thread message bound.
export const MAX_MESSAGE_BYTES = 128 * 1024;
const MAX_EXCERPT_LINES = 6;
const MAX_EXCERPT_WIDTH = 240;

const EMPTY: ReviewComment[] = [];
const store = new Map<string, ReviewComment[]>();
const listeners = new Set<() => void>();
let sequence = 0;

function publish(capsuleId: string, comments: ReviewComment[]) {
  if (comments.length === 0) store.delete(capsuleId);
  else store.set(capsuleId, comments);
  listeners.forEach((listener) => listener());
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function resetReviewComments() {
  store.clear();
  listeners.forEach((listener) => listener());
}

export function useReviewComments(capsuleId: string) {
  const comments = useSyncExternalStore(
    subscribe,
    () => store.get(capsuleId) ?? EMPTY,
  );
  const add = useCallback(
    (anchor: ReviewAnchor, body: string) => {
      const current = store.get(capsuleId) ?? EMPTY;
      if (current.length >= MAX_REVIEW_COMMENTS) return false;
      sequence += 1;
      publish(capsuleId, [
        ...current,
        { ...anchor, id: `review-${sequence}`, body: body.slice(0, MAX_COMMENT_LENGTH) },
      ]);
      return true;
    },
    [capsuleId],
  );
  const update = useCallback(
    (id: string, body: string) =>
      publish(
        capsuleId,
        (store.get(capsuleId) ?? EMPTY).map((comment) =>
          comment.id === id
            ? { ...comment, body: body.slice(0, MAX_COMMENT_LENGTH) }
            : comment,
        ),
      ),
    [capsuleId],
  );
  const remove = useCallback(
    (id: string) =>
      publish(
        capsuleId,
        (store.get(capsuleId) ?? EMPTY).filter((comment) => comment.id !== id),
      ),
    [capsuleId],
  );
  const clear = useCallback(() => publish(capsuleId, EMPTY), [capsuleId]);
  return useMemo(
    () => ({ comments, add, update, remove, clear }),
    [comments, add, update, remove, clear],
  );
}

export type ReviewCommentsController = ReturnType<typeof useReviewComments>;

// Deletions are commented on the old side; additions and context on the new
// side, so every commentable row has exactly one (side, line) anchor.
export function lineSide(line: DiffLine): ReviewSide | undefined {
  if (line.kind === "deletion") return "old";
  if (line.kind === "addition" || line.kind === "context") return "new";
  return undefined;
}

export function sideLine(line: DiffLine, side: ReviewSide) {
  if (side === "old") {
    return line.kind === "deletion" || line.kind === "context"
      ? line.oldLine
      : undefined;
  }
  return line.kind === "addition" || line.kind === "context"
    ? line.newLine
    : undefined;
}

// rangeCandidates lists the rows a range starting at lineIndex may extend
// to: later rows on the same side within the same hunk.
export function rangeCandidates(file: DiffFile, lineIndex: number) {
  const start = file.lines[lineIndex];
  const side = start ? lineSide(start) : undefined;
  if (!start || !side) return [];
  const rows: Array<{ lineIndex: number; line: number }> = [];
  for (
    let index = lineIndex;
    index < file.lines.length && rows.length < MAX_RANGE_LINES;
    index += 1
  ) {
    const row = file.lines[index];
    if (row.kind === "hunk" || row.kind === "metadata") break;
    const line = sideLine(row, side);
    if (line !== undefined) rows.push({ lineIndex: index, line });
  }
  return rows;
}

const excerptText = (value: string) => {
  const safe = sanitizeThreadText(value).replace(/\n/g, " ");
  return safe.length > MAX_EXCERPT_WIDTH
    ? `${safe.slice(0, MAX_EXCERPT_WIDTH)}…`
    : safe;
};

export function reviewAnchor(
  file: DiffFile,
  lineIndex: number,
  endLineIndex = lineIndex,
): ReviewAnchor | undefined {
  const start = file.lines[lineIndex];
  const side = start ? lineSide(start) : undefined;
  const startLine = start && side ? sideLine(start, side) : undefined;
  if (!side || startLine === undefined) return undefined;
  const rows = rangeCandidates(file, lineIndex).filter(
    (row) => row.lineIndex <= endLineIndex,
  );
  const excerpt = rows.map(({ lineIndex: index }) => {
    const row = file.lines[index];
    return {
      marker:
        row.kind === "addition" ? "+" : row.kind === "deletion" ? "-" : " ",
      text: excerptText(row.text),
    } satisfies ReviewExcerptLine;
  });
  return {
    path: file.path,
    side,
    startLine,
    endLine: rows.at(-1)?.line ?? startLine,
    excerpt: excerpt.slice(0, MAX_EXCERPT_LINES),
    omitted: Math.max(0, excerpt.length - MAX_EXCERPT_LINES),
  };
}

export function anchorLabel(anchor: Pick<ReviewAnchor, "path" | "side" | "startLine" | "endLine">) {
  const lines =
    anchor.startLine === anchor.endLine
      ? `${anchor.startLine}`
      : `${anchor.startLine}-${anchor.endLine}`;
  return `${safePath(anchor.path)}:${lines}${anchor.side === "old" ? " (old)" : ""}`;
}

const safePath = (value: string) =>
  sanitizeThreadText(value).replace(/[\n\t]+/g, " ");

const longestBacktickRun = (value: string) =>
  Math.max(0, ...Array.from(value.matchAll(/`+/g), (match) => match[0].length));

// Repository paths and code are untrusted. Each is wrapped in a code span or
// fence longer than any backtick run it contains, so quoted content cannot
// close its fence and pose as the operator's instructions.
export function inlineCode(value: string) {
  const fence = "`".repeat(longestBacktickRun(value) + 1);
  const pad = value.startsWith("`") || value.endsWith("`") ? " " : "";
  return `${fence}${pad}${value}${pad}${fence}`;
}

export function fencedCode(lines: string[], info = "") {
  const body = lines.join("\n");
  const fence = "`".repeat(Math.max(3, longestBacktickRun(body) + 1));
  return `${fence}${info}\n${body}\n${fence}`;
}

export function composeReviewMessage(comments: ReviewComment[]) {
  const count =
    comments.length === 1 ? "this review comment" : `these ${comments.length} review comments`;
  const sections = comments.map((comment, index) => {
    const excerpt = fencedCode(
      comment.excerpt.map((line) => `${line.marker}${line.text}`),
      "diff",
    );
    const omitted =
      comment.omitted > 0
        ? `\n\n(${comment.omitted} more line${comment.omitted === 1 ? "" : "s"} not quoted)`
        : "";
    const lines =
      comment.startLine === comment.endLine
        ? `${comment.startLine}`
        : `${comment.startLine}-${comment.endLine}`;
    const location = inlineCode(`${safePath(comment.path)}:${lines}`);
    const side = comment.side === "old" ? " (old)" : "";
    return `## ${index + 1}. ${location}${side}\n\n${excerpt}${omitted}\n\n${comment.body.trim()}`;
  });
  return [
    `Please address ${count} on the working-tree diff. Line numbers refer to the changed version unless marked (old), which means the version before the change. Quoted code is from the diff.`,
    ...sections,
  ].join("\n\n");
}

export const messageBytes = (value: string) =>
  new TextEncoder().encode(value).length;

export function anchorKey(side: ReviewSide, path: string, line: number) {
  return `${side}:${line}:${path}`;
}

// commentsByRow places each comment after the last line of its range, when
// that line is still part of the rendered diff. A context row carries both
// an old and a new line number, so it can hold comments from either side.
export function commentsByRow(files: DiffFile[], comments: ReviewComment[]) {
  const byAnchor = new Map<string, ReviewComment[]>();
  comments.forEach((comment) => {
    const key = anchorKey(comment.side, comment.path, comment.endLine);
    byAnchor.set(key, [...(byAnchor.get(key) ?? []), comment]);
  });
  const rows = new Map<string, ReviewComment[]>();
  const placed = new Set<string>();
  files.forEach((file, fileIndex) => {
    file.lines.forEach((line, lineIndex) => {
      (["old", "new"] as const).forEach((side) => {
        const number = sideLine(line, side);
        if (number === undefined) return;
        const key = anchorKey(side, file.path, number);
        const found = byAnchor.get(key);
        if (!found) return;
        const rowKey = `${fileIndex}:${lineIndex}`;
        rows.set(rowKey, [...(rows.get(rowKey) ?? []), ...found]);
        found.forEach((comment) => placed.add(comment.id));
        byAnchor.delete(key);
      });
    });
  });
  return { rows, placed };
}
