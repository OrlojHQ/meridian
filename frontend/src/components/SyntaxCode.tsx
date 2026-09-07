import { useEffect, useMemo, useState } from "react";

export type HighlightToken = {
  content: string;
  color?: string;
  fontStyle?: number;
};

export type SyntaxHighlighter = {
  codeToTokens: (
    code: string,
    options: { lang: string; theme: string },
  ) => { tokens: HighlightToken[][] };
};

let highlighterPromise: Promise<SyntaxHighlighter> | undefined;

export function syntaxHighlighter() {
  highlighterPromise ??= Promise.all([
    import("shiki/core"),
    import("shiki/engine/javascript"),
    import("@shikijs/themes/github-light"),
    import("@shikijs/themes/github-dark"),
    import("@shikijs/langs/javascript"),
    import("@shikijs/langs/typescript"),
    import("@shikijs/langs/jsx"),
    import("@shikijs/langs/tsx"),
    import("@shikijs/langs/json"),
    import("@shikijs/langs/yaml"),
    import("@shikijs/langs/markdown"),
    import("@shikijs/langs/html"),
    import("@shikijs/langs/css"),
    import("@shikijs/langs/scss"),
    import("@shikijs/langs/go"),
    import("@shikijs/langs/rust"),
    import("@shikijs/langs/python"),
    import("@shikijs/langs/shellscript"),
    import("@shikijs/langs/sql"),
    import("@shikijs/langs/docker"),
    import("@shikijs/langs/toml"),
    import("@shikijs/langs/xml"),
  ]).then(async ([
    { createHighlighterCore },
    { createJavaScriptRegexEngine },
    githubLight,
    githubDark,
    ...languages
  ]) => {
    const highlighter = await createHighlighterCore({
      themes: [githubLight.default, githubDark.default],
      langs: languages.map((language) => language.default),
      engine: createJavaScriptRegexEngine({ forgiving: true }),
    });
    return highlighter as SyntaxHighlighter;
  });
  return highlighterPromise;
}

export function languageForPath(path: string): string | undefined {
  const name = path.toLowerCase().split("/").at(-1) ?? "";
  if (name === "dockerfile" || name.startsWith("dockerfile.")) return "docker";
  if (name === "makefile") return "shellscript";
  const extension = name.split(".").at(-1);
  const byExtension: Record<string, string> = {
    js: "javascript",
    mjs: "javascript",
    cjs: "javascript",
    ts: "typescript",
    mts: "typescript",
    cts: "typescript",
    jsx: "jsx",
    tsx: "tsx",
    json: "json",
    yaml: "yaml",
    yml: "yaml",
    md: "markdown",
    mdx: "markdown",
    html: "html",
    htm: "html",
    css: "css",
    scss: "scss",
    go: "go",
    rs: "rust",
    py: "python",
    sh: "shellscript",
    bash: "shellscript",
    zsh: "shellscript",
    sql: "sql",
    toml: "toml",
    xml: "xml",
    svg: "xml",
  };
  return extension ? byExtension[extension] : undefined;
}

export function SyntaxTokenLine({
  tokens,
  fallback,
}: {
  tokens?: HighlightToken[];
  fallback: string;
}) {
  if (!tokens) return <>{fallback || " "}</>;
  return (
    <>
      {tokens.map((token, index) => (
        <span
          // Token positions are stable for a single highlighted source line.
          key={`${index}-${token.content}`}
          className={[
            "syntax-token",
            token.fontStyle && token.fontStyle & 1 ? "italic" : "",
            token.fontStyle && token.fontStyle & 2 ? "bold" : "",
            token.fontStyle && token.fontStyle & 4 ? "underline" : "",
          ].filter(Boolean).join(" ")}
          data-color={token.color?.toLowerCase()}
        >
          {token.content}
        </span>
      ))}
    </>
  );
}

export function CodeViewer({
  content,
  path,
  maxHighlightedLines = 2_000,
}: {
  content: string;
  path: string;
  maxHighlightedLines?: number;
}) {
  const lines = useMemo(
    () => content.replaceAll("\r\n", "\n").split("\n"),
    [content],
  );
  const language = languageForPath(path);
  const [dark, setDark] = useState(() =>
    window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false,
  );
  const [tokens, setTokens] = useState<HighlightToken[][]>();

  useEffect(() => {
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    if (!media) return;
    const update = () => setDark(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    let active = true;
    setTokens(undefined);
    if (!language || lines.length > maxHighlightedLines) return;
    void syntaxHighlighter()
      .then((highlighter) => {
        const highlighted = highlighter.codeToTokens(content, {
          lang: language,
          theme: dark ? "github-dark" : "github-light",
        }).tokens;
        if (active) setTokens(highlighted);
      })
      .catch(() => {
        // Plain-text rendering remains available if highlighting fails.
      });
    return () => {
      active = false;
    };
  }, [content, dark, language, lines.length, maxHighlightedLines]);

  return (
    <div className="code-viewer" aria-label={`Contents of ${path}`}>
      <header className="code-viewer-header">
        <strong>{path}</strong>
        <small>
          {language ?? "Plain text"}
          {language && lines.length > maxHighlightedLines
            ? " · highlighting skipped for large file"
            : ""}
        </small>
      </header>
      <div className="code-rows" role="table" aria-label={`Source of ${path}`}>
        {lines.map((line, index) => (
          <div className="code-row" role="row" key={index}>
            <span className="code-gutter" aria-hidden="true">
              {index + 1}
            </span>
            <code>
              <SyntaxTokenLine tokens={tokens?.[index]} fallback={line} />
            </code>
          </div>
        ))}
      </div>
    </div>
  );
}
