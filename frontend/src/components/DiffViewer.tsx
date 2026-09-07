import { useEffect, useMemo, useState } from "react";

import {
  type HighlightToken,
  languageForPath,
  syntaxHighlighter,
  SyntaxTokenLine,
} from "./SyntaxCode";

type DiffLineKind =
  | "addition"
  | "deletion"
  | "context"
  | "hunk"
  | "metadata";

export type DiffLine = {
  kind: DiffLineKind;
  text: string;
  oldLine?: number;
  newLine?: number;
};

export type DiffFile = {
  oldPath: string;
  newPath: string;
  path: string;
  additions: number;
  deletions: number;
  binary: boolean;
  lines: DiffLine[];
};

function displayPath(value: string) {
  if (value === "/dev/null") return value;
  return value.replace(/^[ab]\//, "");
}

function unquotePath(value: string) {
  if (value.startsWith('"') && value.endsWith('"')) {
    try {
      return JSON.parse(value) as string;
    } catch {
      return value.slice(1, -1);
    }
  }
  return value;
}

export function parseUnifiedDiff(content: string): DiffFile[] {
  const files: DiffFile[] = [];
  let current: DiffFile | undefined;
  let oldLine = 0;
  let newLine = 0;
  let inHunk = false;

  const ensureFile = () => {
    if (current) return current;
    current = {
      oldPath: "changes",
      newPath: "changes",
      path: "changes",
      additions: 0,
      deletions: 0,
      binary: false,
      lines: [],
    };
    files.push(current);
    return current;
  };

  for (const rawLine of content.replaceAll("\r\n", "\n").split("\n")) {
    if (rawLine.startsWith("diff --git ")) {
      const match = rawLine.match(/^diff --git (".+?"|\S+) (".+?"|\S+)$/);
      const oldPath = unquotePath(match?.[1] ?? "a/unknown");
      const newPath = unquotePath(match?.[2] ?? "b/unknown");
      current = {
        oldPath: displayPath(oldPath),
        newPath: displayPath(newPath),
        path: displayPath(newPath === "/dev/null" ? oldPath : newPath),
        additions: 0,
        deletions: 0,
        binary: false,
        lines: [],
      };
      files.push(current);
      inHunk = false;
      continue;
    }

    const file = ensureFile();
    const hunk = rawLine.match(
      /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/,
    );
    if (hunk) {
      oldLine = Number(hunk[1]);
      newLine = Number(hunk[2]);
      inHunk = true;
      file.lines.push({ kind: "hunk", text: rawLine });
      continue;
    }

    if (!inHunk && rawLine.startsWith("--- ")) {
      file.oldPath = displayPath(unquotePath(rawLine.slice(4).split("\t", 1)[0]));
      continue;
    }
    if (!inHunk && rawLine.startsWith("+++ ")) {
      file.newPath = displayPath(unquotePath(rawLine.slice(4).split("\t", 1)[0]));
      file.path = file.newPath === "/dev/null" ? file.oldPath : file.newPath;
      continue;
    }
    if (/^(Binary files .* differ|GIT binary patch)/.test(rawLine)) {
      file.binary = true;
      file.lines.push({ kind: "metadata", text: rawLine });
      inHunk = false;
      continue;
    }

    if (inHunk && rawLine.startsWith("+")) {
      file.lines.push({
        kind: "addition",
        text: rawLine.slice(1),
        newLine,
      });
      file.additions += 1;
      newLine += 1;
      continue;
    }
    if (inHunk && rawLine.startsWith("-")) {
      file.lines.push({
        kind: "deletion",
        text: rawLine.slice(1),
        oldLine,
      });
      file.deletions += 1;
      oldLine += 1;
      continue;
    }
    if (inHunk && rawLine.startsWith(" ")) {
      file.lines.push({
        kind: "context",
        text: rawLine.slice(1),
        oldLine,
        newLine,
      });
      oldLine += 1;
      newLine += 1;
      continue;
    }
    if (rawLine === "\\ No newline at end of file") {
      file.lines.push({ kind: "metadata", text: rawLine });
      continue;
    }
    if (
      /^(index |old mode |new mode |new file mode |deleted file mode )/.test(
        rawLine,
      )
    ) {
      continue;
    }
    if (rawLine) {
      file.lines.push({ kind: "metadata", text: rawLine });
    }
  }

  return files.filter(
    (file) => file.lines.length > 0 || file.additions > 0 || file.deletions > 0,
  );
}

export function DiffViewer({
  content,
  truncated = false,
}: {
  content: string;
  truncated?: boolean;
}) {
  const files = useMemo(() => parseUnifiedDiff(content), [content]);
  const [dark, setDark] = useState(() =>
    window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false,
  );
  const [tokens, setTokens] = useState<Record<string, HighlightToken[]>>({});

  useEffect(() => {
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!media) return;
    const update = () => setDark(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    let active = true;
    setTokens({});
    void syntaxHighlighter()
      .then((highlighter) => {
        const next: Record<string, HighlightToken[]> = {};
        files.forEach((file, fileIndex) => {
          const language = languageForPath(file.path);
          if (!language || file.binary || file.lines.length > 2_000) return;
          const oldRows = file.lines
            .map((line, lineIndex) => ({ line, lineIndex }))
            .filter(
              ({ line }) =>
                line.kind === "context" || line.kind === "deletion",
            );
          const newRows = file.lines
            .map((line, lineIndex) => ({ line, lineIndex }))
            .filter(
              ({ line }) =>
                line.kind === "context" || line.kind === "addition",
            );
          const theme = dark ? "github-dark" : "github-light";
          const oldTokens = highlighter.codeToTokens(
            oldRows.map(({ line }) => line.text).join("\n"),
            { lang: language, theme },
          ).tokens;
          const newTokens = highlighter.codeToTokens(
            newRows.map(({ line }) => line.text).join("\n"),
            { lang: language, theme },
          ).tokens;
          oldRows.forEach(({ lineIndex }, tokenIndex) => {
            next[`${fileIndex}:${lineIndex}:old`] =
              oldTokens[tokenIndex] ?? [];
          });
          newRows.forEach(({ lineIndex }, tokenIndex) => {
            next[`${fileIndex}:${lineIndex}:new`] =
              newTokens[tokenIndex] ?? [];
          });
        });
        if (active) setTokens(next);
      })
      .catch(() => {
        // Plain-text diff rendering remains available if highlighting fails.
      });
    return () => {
      active = false;
    };
  }, [dark, files]);

  if (!content) {
    return <div className="tool-empty">No uncommitted changes.</div>;
  }

  return (
    <div className="github-diff" aria-label="Unified diff">
      {truncated && (
        <p className="warning" role="alert">
          Diff truncated by the server response limit.
        </p>
      )}
      {files.map((file, fileIndex) => (
        <details className="diff-file" key={`${file.path}-${fileIndex}`} open>
          <summary className="diff-file-header">
            <span className="diff-file-icon" aria-hidden="true">▣</span>
            <strong>{file.path}</strong>
            {file.oldPath !== file.newPath &&
              file.oldPath !== "/dev/null" &&
              file.newPath !== "/dev/null" && (
                <small>{file.oldPath} → {file.newPath}</small>
              )}
            <span className="diff-file-stats" aria-label={`${file.additions} additions and ${file.deletions} deletions`}>
              <i className="additions">+{file.additions}</i>
              <i className="deletions">−{file.deletions}</i>
            </span>
          </summary>
          <div className="diff-rows" role="table" aria-label={`Changes in ${file.path}`}>
            {file.lines.map((line, lineIndex) => {
              if (line.kind === "hunk") {
                return (
                  <div className="diff-row hunk" role="row" key={lineIndex}>
                    <span className="diff-gutter" />
                    <span className="diff-gutter" />
                    <span className="diff-marker">@@</span>
                    <code>{line.text}</code>
                  </div>
                );
              }
              if (line.kind === "metadata") {
                return (
                  <div className="diff-row metadata" role="row" key={lineIndex}>
                    <span className="diff-gutter" />
                    <span className="diff-gutter" />
                    <span className="diff-marker" />
                    <code>{line.text}</code>
                  </div>
                );
              }
              const tokenSide = line.kind === "deletion" ? "old" : "new";
              return (
                <div className={`diff-row ${line.kind}`} role="row" key={lineIndex}>
                  <span className="diff-gutter" aria-label={line.oldLine ? `Old line ${line.oldLine}` : undefined}>
                    {line.oldLine}
                  </span>
                  <span className="diff-gutter" aria-label={line.newLine ? `New line ${line.newLine}` : undefined}>
                    {line.newLine}
                  </span>
                  <span className="diff-marker" aria-hidden="true">
                    {line.kind === "addition"
                      ? "+"
                      : line.kind === "deletion"
                        ? "−"
                        : " "}
                  </span>
                  <code>
                    <SyntaxTokenLine
                      tokens={tokens[`${fileIndex}:${lineIndex}:${tokenSide}`]}
                      fallback={line.text}
                    />
                  </code>
                </div>
              );
            })}
          </div>
        </details>
      ))}
    </div>
  );
}
