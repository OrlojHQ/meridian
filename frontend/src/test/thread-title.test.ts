import { describe, expect, it } from "vitest";

import { headerAttention } from "../shell/capsuleActivity";
import { changeSummary, deliveryHasChanges, suggestedBranch } from "../shell/deliveryDraft";
import {
  displayThreadTitle,
  firstUserText,
  pullRequestBodyFromMessage,
  threadTitleFromMessage,
} from "../shell/threadTitle";

describe("thread titles", () => {
  it("replaces an automatic id with a short title from the first message", () => {
    const message = "Please fix the header layout so it does not wrap onto two lines.";
    expect(displayThreadTitle("thread-ad942540f1e7", message)).toBe(
      "Please fix the header layout so it does not",
    );
    expect(displayThreadTitle("release notes", message)).toBe("release notes");
    expect(displayThreadTitle("thread-ad942540f1e7", "")).toBe("thread-ad942540f1e7");
  });

  it("cleans markup and keeps the first sentence when it fits", () => {
    expect(threadTitleFromMessage("#  Fix the **header**.\nThen stop.")).toBe("Fix the header");
    expect(firstUserText([
      { role: "assistant", kind: "text", content: "later" },
      { role: "user", kind: "text", content: "  First ask  " },
    ])).toBe("  First ask  ");
  });

  it("keeps line breaks in a pull request description", () => {
    expect(pullRequestBodyFromMessage("One\nTwo")).toBe("One\nTwo");
  });
});

describe("header attention", () => {
  it("shows only working, needs input, and failed", () => {
    expect(headerAttention("Ready", { group: "working", label: "Working" })).toEqual({
      tone: "working",
      label: "Working",
    });
    expect(headerAttention("Ready", { group: "attention", label: "Waiting for input" })).toEqual({
      tone: "attention",
      label: "Needs input",
    });
    expect(headerAttention("Ready", { group: "attention", label: "Waiting for permission" })?.label).toBe(
      "Needs input",
    );
    expect(headerAttention("Ready", { group: "attention", label: "Failed" })).toEqual({
      tone: "attention",
      label: "Failed",
    });
    expect(headerAttention("Failed", { group: "idle", label: "Idle" })?.label).toBe("Failed");
    expect(headerAttention("Ready", { group: "idle", label: "Idle" })).toBeUndefined();
    expect(headerAttention("Ready", { group: "finished", label: "Finished" })).toBeUndefined();
  });
});

describe("delivery draft", () => {
  it("hides creation when nothing differs from the default branch", () => {
    expect(deliveryHasChanges({ dirty: false, branch: "main", defaultBranch: "main" })).toBe(false);
    expect(deliveryHasChanges({ dirty: true, branch: "main", defaultBranch: "main" })).toBe(true);
    expect(deliveryHasChanges({ dirty: false, branch: "feature", defaultBranch: "main" })).toBe(true);
    expect(deliveryHasChanges(undefined)).toBe(false);
  });

  it("suggests the current branch unless it is protected", () => {
    expect(suggestedBranch("feature/local", "main", "Fix the header")).toBe("feature/local");
    expect(suggestedBranch("main", "main", "Fix the header")).toBe("fix-the-header");
  });

  it("summarizes files, lines, and the commit this action will create", () => {
    expect(changeSummary({
      files: 2,
      additions: 4,
      deletions: 1,
      dirty: true,
      branchAhead: false,
    })).toBe("2 files changed · +4 −1 · 1 commit");
    expect(changeSummary({ dirty: false, branchAhead: true })).toBe(
      "0 files changed · +0 −0 · commits on this branch",
    );
  });
});
