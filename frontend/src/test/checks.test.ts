import { describe, expect, it } from "vitest";

import type { ChecksSummary } from "../api/generated/types.gen";
import { checkBanner, describeChecks } from "../shell/checks";

const base: ChecksSummary = {
  configured: true,
  autoRework: true,
  maxAttempts: 3,
  attempt: 1,
};

describe("check presentation", () => {
  it("keeps an in-progress retry out of needs-you", () => {
    expect(describeChecks({ ...base, phase: "reworking", attempt: 1 }, false)).toEqual({
      group: "working",
      label: "Reworking · attempt 2 of 3",
    });
    expect(describeChecks({ ...base, phase: "running", attempt: 1 }, false)?.group).toBe("working");
  });

  it("asks for the operator when checks pass or the budget is used up", () => {
    expect(describeChecks({ ...base, phase: "passed", attempt: 2 }, false)).toEqual({
      group: "attention",
      label: "Checks passed",
    });
    expect(describeChecks({ ...base, phase: "failing", attempt: 3 }, false)).toEqual({
      group: "attention",
      label: "Checks failing (3/3)",
    });
    expect(describeChecks({ ...base, phase: "passed" }, true)).toBeUndefined();
  });

  it("marks a result stale when the tree no longer matches", () => {
    expect(checkBanner({ ...base, phase: "passed", tree: "abc" }, "def")).toBe(
      "Checks are out of date for this tree",
    );
    expect(checkBanner({ ...base, phase: "passed", tree: "abc" }, "abc")).toBe(
      "Checks passed on this tree",
    );
    expect(checkBanner({ ...base, phase: "failing", attempt: 3 }, "abc")).toBe(
      "Checks failing (3/3 attempts)",
    );
    expect(checkBanner({ ...base, phase: "reworking", attempt: 1 }, "abc")).toBe(
      "Reworking · attempt 2 of 3",
    );
  });
});
