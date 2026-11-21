import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { NoteMarkdown } from "../components/NoteMarkdown";

function preview(text: string) {
  return render(<NoteMarkdown text={text} />).container;
}

afterEach(cleanup);

describe("note preview safety", () => {
  it("renders Markdown structure", () => {
    const root = preview("# Tests\n\n- run `make test`\n- **always**\n");
    expect(root.querySelector("h1")).toHaveTextContent("Tests");
    expect(root.querySelectorAll("li")).toHaveLength(2);
    expect(root.querySelector("code")).toHaveTextContent("make test");
  });

  it("renders no raw HTML", () => {
    const root = preview(
      'Hello <script>alert(1)</script> <img src="https://evil.test/x.png" onerror="alert(2)">\n\n' +
        '<iframe src="https://evil.test"></iframe>\n\n<a href="https://evil.test">click</a>\n\n' +
        '<style>body{display:none}</style><form action="https://evil.test"><input name="q"></form>',
    );
    for (const tag of ["script", "img", "iframe", "a", "style", "form", "input"]) {
      expect(root.querySelector(tag)).toBeNull();
    }
    // The markup is shown as text, so the operator can see it is there.
    expect(root.textContent).toContain("<script>alert(1)</script>");
  });

  it("loads no images, remote or otherwise", () => {
    const root = preview(
      "![build badge](https://evil.test/track.png?secret=1)\n\n![](data:image/png;base64,AAAA)\n\n" +
        "[![nested](https://evil.test/n.png)](https://evil.test)",
    );
    expect(root.querySelector("img")).toBeNull();
    expect(root.querySelector("[src]")).toBeNull();
    expect(root.querySelector("[srcset]")).toBeNull();
    expect(screen.getByRole("img", { name: "Image not loaded: build badge" })).toBeInTheDocument();
    expect(root.textContent).toContain("[image: build badge (https://evil.test/track.png?secret=1)]");
  });

  it("never follows links on its own", () => {
    const root = preview(
      "[docs](https://example.test/docs) [js](javascript:alert(1)) [rel](/api/projects) " +
        "<https://auto.test> www.bare.test [ref][1] footnote[^1]\n\n[1]: https://ref.test\n\n[^1]: note",
    );
    expect(root.querySelector("a")).toBeNull();
    expect(root.querySelector("[href]")).toBeNull();
    expect(root.textContent).toContain("docs (https://example.test/docs)");
    expect(root.textContent).toContain("js (javascript:alert(1))");
    expect(root.textContent).toContain("https://auto.test");
  });

  it("shows hidden characters as escapes in the preview too", () => {
    const root = preview("safe‮txt.exe and zero​width");
    expect(root.textContent).toContain("safe\\u{202E}txt.exe");
    expect(root.textContent).toContain("zero\\u{200B}width");
    expect(root.textContent).not.toContain("‮");
    expect(root.textContent).not.toContain("​");
  });

  it("emits no event handler attributes", () => {
    const root = preview('<div onclick="alert(1)">x</div> [x](https://a.test "title" )');
    for (const element of Array.from(root.querySelectorAll("*"))) {
      for (const attribute of Array.from(element.attributes)) {
        expect(attribute.name.startsWith("on")).toBe(false);
      }
    }
  });
});
