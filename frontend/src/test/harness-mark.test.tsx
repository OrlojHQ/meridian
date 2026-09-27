import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";

import { HarnessMark } from "../components/ui/HarnessMark";

afterEach(cleanup);

it("draws published harness marks and falls back to a monogram", () => {
  const claude = render(<HarnessMark harness="claude" />).container.firstElementChild;
  expect(claude).toHaveAttribute("title", "Claude Code");
  expect(claude?.querySelector("svg path")).not.toBeNull();

  const codex = render(<HarnessMark harness="codex" />).container.firstElementChild;
  expect(codex).toHaveAttribute("title", "Codex");
  expect(codex?.querySelector("svg path")).toHaveAttribute("fill-rule", "evenodd");

  const mock = render(<HarnessMark harness="mock" />).container.firstElementChild;
  expect(mock).toHaveTextContent("M");
  expect(mock?.querySelector("svg")).toBeNull();

  const custom = render(<HarnessMark harness="aider" />).container.firstElementChild;
  expect(custom).toHaveClass("harness-mark-other");
  expect(custom).toHaveTextContent("Ai");
});
