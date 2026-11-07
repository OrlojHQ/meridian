import { memo, type ReactNode } from "react";
import Markdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

const SAFE_PROTOCOLS = new Set(["http:", "https:", "mailto:"]);

// Agent output is hostile: only absolute web and mail links survive, so a
// transcript cannot link into this origin's API or run script URLs.
export function safeLinkTarget(url: unknown): string | undefined {
  if (typeof url !== "string") return undefined;
  try {
    const parsed = new URL(url.trim());
    return SAFE_PROTOCOLS.has(parsed.protocol) ? parsed.href : undefined;
  } catch {
    return undefined;
  }
}

function SafeLink({ href, children }: { href?: string; children?: ReactNode }) {
  const target = safeLinkTarget(href);
  if (!target) return <span className="markdown-unsafe-link">{children}</span>;
  return (
    <a href={target} target="_blank" rel="noopener noreferrer nofollow" referrerPolicy="no-referrer">
      {children}
    </a>
  );
}

const components: Components = {
  a: ({ href, children }) => <SafeLink href={href}>{children}</SafeLink>,
  // Remote images are never fetched: an image URL can carry exfiltrated data
  // the moment it loads, without any click.
  img: ({ src, alt }) => (
    <SafeLink href={typeof src === "string" ? src : undefined}>
      {alt ? `Image: ${alt}` : "Image"}
    </SafeLink>
  ),
  table: ({ children }) => (
    <div className="markdown-table">
      <table>{children}</table>
    </div>
  ),
};

const remarkPlugins = [remarkGfm];

// Raw HTML in the source is rendered as literal text; react-markdown builds
// React elements and never injects markup.
export const ThreadMarkdown = memo(function ThreadMarkdown({ text }: { text: string }) {
  return (
    <div className="markdown">
      <Markdown
        remarkPlugins={remarkPlugins}
        components={components}
        urlTransform={(url) => safeLinkTarget(url)}
      >
        {text}
      </Markdown>
    </div>
  );
});
