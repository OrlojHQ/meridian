import { describe, expect, it } from "vitest";

import { packMayRunStructured, projectHarnessImage } from "../api/harnessPacks";

const catalog = [
  {
    name: "claude",
    imageReference: "meridian-capsule-claude:dev",
    interactionModes: ["native" as const],
  },
  {
    name: "agent",
    imageReference: "agent:1",
    interactionModes: ["native" as const, "structured" as const],
  },
  { name: "mock", imageReference: "meridian-capsule:dev" },
];

describe("harness pack interaction modes", () => {
  it("rejects only a catalog pack that declares no structured mode", () => {
    expect(packMayRunStructured(projectHarnessImage(catalog[0]!), catalog)).toBe(false);
    expect(packMayRunStructured(projectHarnessImage(catalog[1]!), catalog)).toBe(true);
    expect(packMayRunStructured(catalog[2]!, catalog)).toBe(true);
    expect(
      packMayRunStructured({ name: "claude", imageReference: "example.test/claude:1" }, catalog),
    ).toBe(true);
    expect(packMayRunStructured(catalog[0]!, [])).toBe(true);
  });

  it("keeps read-only catalog metadata out of Project requests", () => {
    expect(projectHarnessImage(catalog[0]!)).toEqual({
      name: "claude",
      imageReference: "meridian-capsule-claude:dev",
    });
  });
});
