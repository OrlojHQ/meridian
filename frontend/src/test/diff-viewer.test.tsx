import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("shiki/core", () => ({
  createHighlighterCore: async () => ({
    codeToTokens: (code: string) => ({
      tokens: code.split("\n").map((line) => [{ content: line, color: "#6a737d" }]),
    }),
  }),
}));

vi.mock("shiki/engine/javascript", () => ({
  createJavaScriptRegexEngine: () => ({}),
}));

import { DiffViewer, parseUnifiedDiff } from "../components/DiffViewer";

const diff = `diff --git a/src/example.ts b/src/example.ts
index 1111111..2222222 100644
--- a/src/example.ts
+++ b/src/example.ts
@@ -1,2 +1,3 @@
-const oldValue = true;
+const newValue = false;
 console.log("safe");
+<script>steal()</script>
`;

afterEach(cleanup);

describe("GitHub-style diff viewer", () => {
  it("parses files, line numbers, and change totals", () => {
    const [file] = parseUnifiedDiff(diff);
    expect(file.path).toBe("src/example.ts");
    expect(file.additions).toBe(2);
    expect(file.deletions).toBe(1);
    expect(file.lines.find((line) => line.kind === "deletion")).toMatchObject({
      oldLine: 1,
    });
    expect(file.lines.find((line) => line.kind === "addition")).toMatchObject({
      newLine: 1,
    });
  });

  it("renders hostile source as escaped text in semantic diff rows", () => {
    const { container } = render(<DiffViewer content={diff} />);
    expect(screen.getByText("src/example.ts")).toBeInTheDocument();
    expect(screen.getByLabelText("Unified diff")).toHaveTextContent(
      "<script>steal()</script>",
    );
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelectorAll(".diff-row.addition")).toHaveLength(2);
    expect(container.querySelectorAll(".diff-row.deletion")).toHaveLength(1);
  });
});
