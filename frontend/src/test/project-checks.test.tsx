import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { ProjectChecks } from "../api/generated/types.gen";
import { ProjectChecks as ProjectChecksPanel } from "../components/ProjectChecks";
import { buildThreadChat } from "../components/threadChat";
import type { ThreadBlock } from "../api/generated/types.gen";

const saved: ProjectChecks = {
  checks: [],
  autoRework: true,
  maxReworkAttempts: 3,
  resourceVersion: 4,
};

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ProjectChecksPanel projectId="project-1" />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Project checks settings", () => {
  it("saves one command and the retry limit", async () => {
    vi.spyOn(api, "projectChecks").mockResolvedValue(saved);
    vi.spyOn(api, "suggestProjectChecks").mockResolvedValue({ checks: [] });
    const update = vi.spyOn(api, "updateProjectChecks").mockResolvedValue({
      ...saved,
      checks: [{ name: "test", argv: ["make", "test"], timeoutSeconds: 600 }],
      resourceVersion: 5,
    });
    renderPanel();
    const command = await screen.findByLabelText("Check 1 command");
    await userEvent.type(command, "make test");
    await userEvent.clear(screen.getByLabelText("Attempts"));
    await userEvent.type(screen.getByLabelText("Attempts"), "3");
    await userEvent.click(screen.getByRole("button", { name: "Save checks" }));
    expect(update).toHaveBeenCalledWith("project-1", {
      checks: [{ name: "test", argv: ["make", "test"], timeoutSeconds: 600 }],
      autoRework: true,
      maxReworkAttempts: 3,
      expectedResourceVersion: 4,
    });
  });
});

describe("Meridian rework messages", () => {
  it("renders a system check message as from Meridian", () => {
    const blocks: ThreadBlock[] = [{
      id: "block-1",
      messageId: "message-1",
      messageSequence: 2,
      sequence: 1,
      role: "system",
      messageKind: "status",
      kind: "text",
      content: "Required check `test` (`make test`) failed with exit code 1.\n(Attempt 2 of 3)",
      createdAt: "2026-11-16T00:00:00Z",
    }];
    expect(buildThreadChat(blocks).items).toContainEqual({
      kind: "meridian",
      key: "block-1",
      text: expect.stringContaining("Attempt 2 of 3"),
    });
  });
});
