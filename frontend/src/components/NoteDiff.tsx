import { useMemo } from "react";

import { diffStat, escapeHiddenParts, lineDiff } from "./notesText";

// EscapedText shows hidden characters as visible escapes such as \u{202E},
// so text cannot hide or reorder itself inside a review.
export function EscapedText({ text }: { text: string }) {
  return (
    <>
      {escapeHiddenParts(text).map((part, index) =>
        part.hidden ? (
          <mark key={index} className="note-escape" title="Hidden character">
            {part.text}
          </mark>
        ) : (
          <span key={index}>{part.text}</span>
        ),
      )}
    </>
  );
}

// NoteDiff is a source line diff, never a rendered view. Long lines wrap.
export function NoteDiff({ before, after, label }: { before: string; after: string; label: string }) {
  const lines = useMemo(() => lineDiff(before, after), [before, after]);
  const stat = diffStat(lines);
  return (
    <figure className="note-diff" aria-label={label}>
      <figcaption className="note-diff-stat">
        <span className="note-diff-added">+{stat.added}</span>{" "}
        <span className="note-diff-removed">−{stat.removed}</span>
        {stat.added === 0 && stat.removed === 0 && <span> · no changes</span>}
      </figcaption>
      {lines.length === 0 ? (
        <p className="muted-copy">Both versions are empty.</p>
      ) : (
        <ol className="note-diff-lines">
          {lines.map((line, index) => (
            <li
              key={index}
              className={`note-diff-line ${line.kind}`}
              aria-label={line.kind === "add" ? "Added line" : line.kind === "remove" ? "Removed line" : undefined}
            >
              <span className="note-diff-number" aria-hidden="true">
                {line.kind === "add" ? "" : line.oldLine}
              </span>
              <span className="note-diff-number" aria-hidden="true">
                {line.kind === "remove" ? "" : line.newLine}
              </span>
              <span className="note-diff-sign" aria-hidden="true">
                {line.kind === "add" ? "+" : line.kind === "remove" ? "−" : " "}
              </span>
              <code className="note-diff-text">
                <EscapedText text={line.text} />
              </code>
            </li>
          ))}
        </ol>
      )}
    </figure>
  );
}
