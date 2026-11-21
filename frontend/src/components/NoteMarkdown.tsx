import { memo } from "react";
import Markdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

import { escapeHiddenCharacters } from "./notesText";

// The note preview (ADR 0060 section 7) is stricter than the transcript's:
// it renders no raw HTML, fetches nothing, and never navigates. A link is
// inert text with its target shown beside it, so the operator sees where it
// points without the page going there; an image is its alt text.
const components: Components = {
  a: ({ href, children }) => (
    <span className="note-link" data-target={typeof href === "string" ? href : undefined}>
      {children}
      {typeof href === "string" && href !== "" && (
        <span className="note-link-target"> ({href})</span>
      )}
    </span>
  ),
  img: ({ src, alt }) => (
    <span className="note-image" role="img" aria-label={alt ? `Image not loaded: ${alt}` : "Image not loaded"}>
      [image{alt ? `: ${alt}` : ""}{typeof src === "string" && src !== "" ? ` (${src})` : ""}]
    </span>
  ),
  table: ({ children }) => (
    <div className="markdown-table">
      <table>{children}</table>
    </div>
  ),
};

const remarkPlugins = [remarkGfm];

// URLs pass through only as text for the link and image components above;
// no element ever receives an href or src.
const keepUrlAsText = (url: string) => escapeHiddenCharacters(url);

export const NoteMarkdown = memo(function NoteMarkdown({ text }: { text: string }) {
  return (
    <div className="markdown note-preview">
      <Markdown
        remarkPlugins={remarkPlugins}
        components={components}
        skipHtml={false}
        urlTransform={keepUrlAsText}
      >
        {escapeHiddenCharacters(text)}
      </Markdown>
    </div>
  );
});
