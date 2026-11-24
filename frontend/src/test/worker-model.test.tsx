import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Capsule, CoordinationGrant, Thread, ThreadLink } from "../api/generated/types.gen";
import { ThreadModelPicker } from "../components/ThreadModelPicker";
import { ThreadSubagents, subagentModel } from "../components/ThreadSubagents";
import type { ThreadModels } from "../components/threadModels";

const leadThread: Thread = {
  id: "thread-lead",
  capsuleId: "cap-lead",
  state: "active",
  harness: "claude",
  currentRunId: "run-lead",
  currentRunState: "Running",
  coordinationGrantId: "grant-1",
  encryptedAtRest: true,
  encryptedBytes: 128,
  messageCount: 1,
  createdAt: "2026-11-20T00:00:00Z",
  updatedAt: "2026-11-20T00:00:00Z",
  resourceVersion: 1,
};

const plainThread: Thread = {
  ...leadThread,
  id: "thread-plain",
  coordinationGrantId: undefined,
};

const subThread: Thread = {
  ...leadThread,
  id: "thread-sub",
  capsuleId: "cap-sub",
  harness: "codex",
  parentThreadId: "thread-lead",
  rootThreadId: "thread-lead",
  depth: 1,
};

const baseGrant: CoordinationGrant = {
  id: "grant-1",
  projectId: "proj-1",
  rootThreadId: "thread-lead",
  rootCapsuleId: "cap-lead",
  state: "active",
  spawnApproval: "auto",
  maxDepth: 1,
  maxChildren: 16,
  maxDescendants: 16,
  maxLiveCapsules: 4,
  maxMessagesPerLink: 50,
  allowedHarnesses: ["claude", "codex"],
  workerModel: { mode: "match" },
  lastOperatorAt: "2026-11-20T00:00:00Z",
  unattendedLimitSeconds: 86400,
  lapsesAt: "2026-11-21T00:00:00Z",
  onDeadline: "cancel_and_pause",
  archiveAfterSeconds: 3600,
  descendantsCreated: 0,
  spawnsInWindow: 0,
  createdAt: "2026-11-20T00:00:00Z",
  updatedAt: "2026-11-20T00:00:00Z",
  resourceVersion: 1,
};

const leadModels: ThreadModels = {
  current: "default",
  currentLabel: "Default (recommended)",
  resolved: "Opus 4.6",
  images: false,
  options: [
    { id: "default", label: "Default (recommended)", description: "Opus 4.6" },
    { id: "sonnet", label: "Sonnet 4.6", description: "Fast coding" },
    { id: "haiku", label: "Haiku 4.5", description: "Quick lookups" },
  ],
};

function renderWithQuery(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Composer Worker Model picker", () => {
  it("shows Worker Model row for leads, supports Match vs Custom, and persists via grant update", async () => {
    let currentGrant: CoordinationGrant = { ...baseGrant };
    vi.spyOn(api, "coordinationGrant").mockImplementation(async () => currentGrant);
    const update = vi.spyOn(api, "updateCoordinationGrant").mockImplementation(async (grant, body) => {
      currentGrant = {
        ...grant,
        workerModel: body.workerModel ?? grant.workerModel,
        resourceVersion: grant.resourceVersion + 1,
      };
      return currentGrant;
    });
    vi.spyOn(api, "reportedModels").mockResolvedValue({
      models: [{ id: "gpt-5.4", label: "GPT-5.4" }],
      efforts: [],
    });

    renderWithQuery(
      <ThreadModelPicker
        thread={leadThread}
        models={leadModels}
        disabled={false}
        onSwitched={() => undefined}
        onConflict={async () => undefined}
      />,
    );

    const modelButton = screen.getByRole("button", { name: /Model: Opus 4\.6/ });
    await userEvent.click(modelButton);

    // Worker Model row appears with "Match" on the right.
    const workerRow = await screen.findByRole("menuitem", { name: /Worker Model.*Match/ });
    await userEvent.click(workerRow);

    // Submenu shows Match, Custom, and the resolved hint line.
    expect(screen.getByRole("menuitemradio", { name: /Match/ })).toHaveAttribute("aria-checked", "true");
    expect(screen.getByText("New workers use Opus 4.6 (the model's default effort)")).toBeInTheDocument();

    // Pick Custom -> Sonnet 4.6 on lead's harness.
    await userEvent.click(screen.getByRole("menuitemradio", { name: /Custom/ }));
    await userEvent.click(screen.getByRole("menuitemradio", { name: /Sonnet 4\.6/ }));

    await waitFor(() =>
      expect(update).toHaveBeenCalledWith(baseGrant, {
        workerModel: { mode: "custom", model: "sonnet" },
      }),
    );
    expect(
      await screen.findByText("New workers use Sonnet 4.6 (the model's default effort)"),
    ).toBeInTheDocument();

    // Pick another allowed harness (Codex -> GPT-5.4).
    await userEvent.click(screen.getByRole("button", { name: "Codex" }));
    const gptOption = await screen.findByRole("menuitemradio", { name: /GPT-5\.4/ });
    await userEvent.click(gptOption);

    await waitFor(() =>
      expect(update).toHaveBeenLastCalledWith(
        expect.objectContaining({ resourceVersion: 2 }),
        { workerModel: { mode: "custom", harness: "codex", model: "gpt-5.4" } },
      ),
    );
    expect(
      await screen.findByText("New workers use GPT-5.4 on Codex (the model's default effort)"),
    ).toBeInTheDocument();

    // Switch back to Match.
    await userEvent.click(screen.getByRole("menuitemradio", { name: /Match/ }));
    await waitFor(() =>
      expect(update).toHaveBeenLastCalledWith(
        expect.objectContaining({ resourceVersion: 3 }),
        { workerModel: { mode: "match" } },
      ),
    );
  });

  it("hides Worker Model on plain and subagent Threads", async () => {
    for (const thread of [plainThread, subThread]) {
      renderWithQuery(
        <ThreadModelPicker
          thread={thread}
          models={leadModels}
          disabled={false}
          onSwitched={() => undefined}
          onConflict={async () => undefined}
        />,
      );

      await userEvent.click(screen.getByRole("button", { name: /Model: Opus 4\.6/ }));
      expect(screen.queryByRole("menuitem", { name: /Worker Model/ })).not.toBeInTheDocument();
      cleanup();
    }
  });
});

describe("Subagent model and harness status", () => {
  it("shows requestedModel while provisioning and displays harness and model in the subagent header", async () => {
    expect(subagentModel({ requestedModel: "sonnet" })).toBe("sonnet");
    expect(subagentModel({ model: "Sonnet 4.6", requestedModel: "sonnet", effort: "High" })).toBe(
      "Sonnet 4.6 · High",
    );

    const subCapsule: Capsule = {
      id: "cap-sub",
      projectId: "proj-1",
      timelineId: "tl-1",
      name: "worker-codex",
      state: "Ready",
      desiredState: "Ready",
      role: "sub",
      restoreComplete: true,
      createdAt: "2026-11-20T00:00:00Z",
      updatedAt: "2026-11-20T00:00:00Z",
      resourceVersion: 1,
    };
    const link: ThreadLink = {
      id: "link-1",
      grantId: "grant-1",
      parentThreadId: "thread-lead",
      parentCapsuleId: "cap-lead",
      childThreadId: "thread-sub",
      childCapsuleId: "cap-sub",
      childIntentId: "intent-sub",
      depth: 1,
      name: "worker-codex",
      harness: "codex",
      requestedModel: "gpt-5.4",
      state: "active",
      phase: "provisioning",
      reportSeq: 0,
      reportDeliveredSeq: 0,
      messagesForwarded: 0,
      operatorMessages: 0,
      createdAt: "2026-11-20T00:00:00Z",
      updatedAt: "2026-11-20T00:00:00Z",
      resourceVersion: 1,
    };
    vi.spyOn(api, "thread").mockResolvedValue(leadThread);
    vi.spyOn(api, "capsule").mockResolvedValue(subCapsule);
    vi.spyOn(api, "threadLinks").mockResolvedValue({ items: [link] });

    renderWithQuery(<ThreadSubagents thread={subThread} />);

    expect(await screen.findByText("Codex")).toBeInTheDocument();
    expect(await screen.findByText("gpt-5.4")).toBeInTheDocument();
  });
});
