import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import type { HarnessImage } from "../api/generated/types.gen";
import { NewProjectDialog } from "../components/NewProjectDialog";

// Same order as domain.InstallationHarnessImages: real agents first, mock last.
const catalog: HarnessImage[] = [
  { name: "opencode", imageReference: "ghcr.io/orlojhq/meridian-capsule-opencode:v0.1.1" },
  { name: "pi", imageReference: "ghcr.io/orlojhq/meridian-capsule-pi:v0.1.1" },
  { name: "claude", imageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.1.1" },
  { name: "codex", imageReference: "ghcr.io/orlojhq/meridian-capsule-codex:v0.1.1" },
  { name: "mock", imageReference: "ghcr.io/orlojhq/meridian-capsule:v0.1.1" },
];

function renderDialog() {
  const client = new QueryClient();
  render(
    <QueryClientProvider client={client}>
      <NewProjectDialog open harnessImages={catalog} onClose={() => {}} />
    </QueryClientProvider>,
  );
}

describe("NewProjectDialog", () => {
  afterEach(cleanup);

  it("preselects a real agent and labels the mock test harness", () => {
    renderDialog();
    const select = screen.getByLabelText(/Initial harness pack/);
    expect(select).toHaveValue("opencode");
    expect(
      screen.getByRole("option", { name: "mock (test harness, no agent)" }),
    ).toHaveValue("mock");
    expect(screen.getByRole("option", { name: "claude" })).toHaveValue("claude");
  });
});
