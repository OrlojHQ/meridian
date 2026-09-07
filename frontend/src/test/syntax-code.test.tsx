import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("shiki/core", () => ({
  createHighlighterCore: async () => ({
    codeToTokens: (code: string) => ({
      tokens: code.split("\n").map((line) => [
        { content: line, color: "#005cc5" },
      ]),
    }),
  }),
}));

vi.mock("shiki/engine/javascript", () => ({
  createJavaScriptRegexEngine: () => ({}),
}));

import {
  CodeViewer,
  languageForPath,
} from "../components/SyntaxCode";

afterEach(cleanup);

describe("workspace source viewer", () => {
  it("detects supported filenames and extensions", () => {
    expect(languageForPath("frontend/src/App.tsx")).toBe("tsx");
    expect(languageForPath("images/capsule/Dockerfile")).toBe("docker");
    expect(languageForPath("Makefile")).toBe("shellscript");
    expect(languageForPath("LICENSE")).toBeUndefined();
  });

  it("renders highlighted, numbered source without interpreting hostile markup", async () => {
    const source = `const safe = true;\n<script>steal()</script>`;
    const { container } = render(
      <CodeViewer content={source} path="src/example.ts" />,
    );

    expect(screen.getByLabelText("Contents of src/example.ts")).toHaveTextContent(
      "<script>steal()</script>",
    );
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelectorAll(".code-row")).toHaveLength(2);
    expect(container.querySelectorAll(".code-gutter")[1]).toHaveTextContent("2");
    await waitFor(() =>
      expect(container.querySelectorAll(".syntax-token")).toHaveLength(2),
    );
  });

  it("falls back to plain text for unsupported files", () => {
    const { container } = render(
      <CodeViewer content="Copyright Meridian" path="LICENSE" />,
    );

    expect(screen.getByText("Plain text")).toBeInTheDocument();
    expect(container.querySelector(".syntax-token")).toBeNull();
  });
});
