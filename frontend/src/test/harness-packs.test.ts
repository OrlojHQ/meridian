import { describe, expect, it } from "vitest";

import type { HarnessImage } from "../api/generated/types.gen";

import {
  catalogPack,
  olderPackRelease,
  packMayRunNative,
  packMayRunStructured,
  projectHarnessImage,
} from "../api/harnessPacks";

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
  { name: "custom", imageReference: "example.test/custom:1" },
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

// ADR 0041: an official pack saved under another release is the catalog pack.
const release = [
  { name: "codex", imageReference: "ghcr.io/orlojhq/meridian-capsule-codex:v0.4.1", interactionModes: ["native", "structured"] },
  { name: "dsh", imageReference: "ghcr.io/orlojhq/meridian-capsule-dsh:v0.4.1", interactionModes: ["structured"] },
] as HarnessImage[];

it("matches an official pack at an older release to the current catalog entry", () => {
  expect(catalogPack({ name: "codex", imageReference: "ghcr.io/orlojhq/meridian-capsule-codex:v0.4.0" }, release)?.imageReference)
    .toBe("ghcr.io/orlojhq/meridian-capsule-codex:v0.4.1");
  for (const imageReference of [
    "ghcr.io/orlojhq/meridian-capsule-codex:my-fork",
    `ghcr.io/orlojhq/meridian-capsule-codex:v0.4.0@sha256:${"a".repeat(64)}`,
    "example.test/meridian-capsule-codex:v0.4.0",
  ]) {
    expect(catalogPack({ name: "codex", imageReference }, release)).toBeUndefined();
  }
  // Modes come from the current pack, so an older dsh still has no terminal.
  const oldDsh = { name: "dsh", imageReference: "ghcr.io/orlojhq/meridian-capsule-dsh:v0.4.0" };
  expect(packMayRunNative(oldDsh, release)).toBe(false);
  expect(packMayRunStructured(oldDsh, release)).toBe(true);
});

it("reports the older release a Capsule was built on, and nothing otherwise", () => {
  expect(olderPackRelease("codex", "ghcr.io/orlojhq/meridian-capsule-codex:v0.4.0", release))
    .toEqual({ release: "v0.4.0", current: "v0.4.1" });
  expect(olderPackRelease("codex", "ghcr.io/orlojhq/meridian-capsule-codex:v0.4.1", release)).toBeUndefined();
  expect(olderPackRelease("codex", "ghcr.io/orlojhq/meridian-capsule-codex:my-fork", release)).toBeUndefined();
  expect(olderPackRelease("codex", undefined, release)).toBeUndefined();
  expect(olderPackRelease(undefined, "ghcr.io/orlojhq/meridian-capsule-codex:v0.4.0", release)).toBeUndefined();
  // Development builds advertise :dev, which is not a release to compare.
  expect(olderPackRelease("codex", "meridian-capsule-codex:dev", [{ name: "codex", imageReference: "meridian-capsule-codex:dev" }])).toBeUndefined();
});
