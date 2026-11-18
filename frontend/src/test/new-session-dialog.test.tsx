import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Capabilities, Capsule, Project } from "../api/generated/types.gen";
import { NewSessionDialog } from "../components/NewSessionDialog";

const project: Project = {
  id: "project-1",
  name: "testing",
  harnessImages: [{ name: "opencode", imageReference: "example.test/opencode:v1" }],
  createdAt: "2026-11-11T00:00:00Z",
  updatedAt: "2026-11-11T00:00:00Z",
  resourceVersion: 1,
};
const capabilities: Capabilities = {
  providerVersion: "docker/v1", isolation: "container" as const,
  attach: true,
  run: true,
  structured: true,
  git: false,
  pause: false,
  snapshot: false,
  clone: false,
  browse: false,
  delivery: false,
  preview: false,
  resourceMetrics: false,
  harnessImages: project.harnessImages,
};
const capsule: Capsule = {
  id: "capsule-new",
  projectId: project.id,
  timelineId: "timeline-new",
  name: "opencode-work",
  state: "Creating",
  desiredState: "Ready",
  restoreComplete: false,
  createdAt: project.createdAt,
  updatedAt: project.updatedAt,
  resourceVersion: 1,
};

function renderDialog(onClose = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/"]}>
        <Routes>
          <Route
            path="/ui/"
            element={<NewSessionDialog projectId={project.id} onProjectChange={vi.fn()} onClose={onClose} />}
          />
          <Route path="/ui/capsules/:capsuleId" element={<p>Capsule opened</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return onClose;
}

beforeEach(() => {
  vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
  vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
  vi.spyOn(api, "board").mockResolvedValue({ items: [], truncated: false });
  vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: true });
  vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("New Capsule dialog", () => {
  it("explains the Thread requirement and lets CLI start without a saved connection", async () => {
    const create = vi.spyOn(api, "createCapsule").mockResolvedValue(capsule);
    const spawn = vi.spyOn(api, "createProjectThread");
    const grant = vi.spyOn(api, "grantConnection");
    renderDialog();

    const start = await screen.findByRole("button", { name: "Start" });
    await waitFor(() => expect(start).toHaveAccessibleDescription(/Thread mode needs a provider connection/));
    expect(start).toBeDisabled();
    expect(screen.getByText(/switch to CLI to sign in inside OpenCode/)).toBeInTheDocument();
    expect(spawn).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("tab", { name: "CLI" }));
    expect(start).toBeEnabled();
    expect(screen.queryByRole("link", { name: "Connect a provider" })).not.toBeInTheDocument();
    expect(screen.queryByRole("combobox", { name: "Project" })).not.toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Harness" })).toHaveTextContent("OpenCode");
    await userEvent.click(start);
    expect(await screen.findByText("Capsule opened")).toBeInTheDocument();
    expect(create).toHaveBeenCalledWith(project.id, expect.stringMatching(/^opencode-/), "opencode");
    expect(grant).not.toHaveBeenCalled();
    expect(spawn).not.toHaveBeenCalled();
  });

  it("starts a Thread with a compatible connection", async () => {
    vi.mocked(api.providerConnections).mockResolvedValue({
      enabled: true,
      items: [{
        id: "connection-1", name: "OpenAI", provider: "openai", revoked: false,
        createdAt: project.createdAt, resourceVersion: 1,
      }],
    });
    vi.mocked(api.projectConnection).mockResolvedValue({ connectionId: "connection-1" });
    const spawn = vi.spyOn(api, "createProjectThread").mockResolvedValue({
      id: "intent-1", projectId: project.id, capsuleId: capsule.id, capsuleName: "new-task", threadId: "thread-1",
      runId: "run-1", messageId: "message-1", harness: "opencode", state: "provisioning",
      createdAt: project.createdAt, updatedAt: project.updatedAt, resourceVersion: 1,
    });
    const onClose = renderDialog();

    expect(await screen.findByRole("dialog", { name: "New Capsule" })).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    const harness = await screen.findByRole("combobox", { name: "Harness" });
    expect(harness).toHaveTextContent("OpenCode");
    expect(harness).toHaveAttribute("aria-expanded", "false");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeEnabled());
    harness.focus();
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByText("Capsule opened")).toBeInTheDocument();
    // The Thread starts without a first message.
    expect(spawn).toHaveBeenCalledWith(project.id, "opencode", "", undefined, undefined);
    expect(onClose).toHaveBeenCalled();
    // Without coordination support there is no lead option.
    expect(screen.queryByRole("checkbox", { name: "Lead agent with subagents" })).not.toBeInTheDocument();
  });

  it("starts a lead Thread with fixed limits and optional spawn approval", async () => {
    vi.mocked(api.capabilities).mockResolvedValue({
      ...capabilities,
      coordination: { supported: true, maxDepth: 1, maxDescendants: 8 },
    });
    vi.mocked(api.providerConnections).mockResolvedValue({
      enabled: true,
      items: [{
        id: "connection-1", name: "OpenAI", provider: "openai", revoked: false,
        createdAt: project.createdAt, resourceVersion: 1,
      }],
    });
    vi.mocked(api.projectConnection).mockResolvedValue({ connectionId: "connection-1" });
    const spawn = vi.spyOn(api, "createProjectThread").mockResolvedValue({
      id: "intent-1", projectId: project.id, capsuleId: capsule.id, capsuleName: "lead", threadId: "thread-1",
      runId: "run-1", messageId: "message-1", harness: "opencode", state: "provisioning",
      createdAt: project.createdAt, updatedAt: project.updatedAt, resourceVersion: 1,
    });
    renderDialog();

    const lead = await screen.findByRole("checkbox", { name: "Lead agent with subagents" });
    expect(screen.queryByRole("checkbox", { name: "Approve each subagent spawn" })).not.toBeInTheDocument();
    await userEvent.click(lead);
    // No numbers to guess: the daemon keeps its own limits.
    expect(screen.queryByRole("spinbutton")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("checkbox", { name: "Approve each subagent spawn" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Start" }));
    expect(await screen.findByText("Capsule opened")).toBeInTheDocument();
    expect(spawn).toHaveBeenCalledWith(project.id, "opencode", "", undefined, {
      maxChildren: 16, maxDescendants: 16, spawnApproval: "operator",
    });
  });

  it("chooses a harness from the dropdown with the keyboard", async () => {
    const packs = [
      { name: "opencode", imageReference: "example.test/opencode:v1" },
      { name: "pi", imageReference: "example.test/pi:v1" },
    ];
    vi.mocked(api.projects).mockResolvedValue({ items: [{ ...project, harnessImages: packs }] });
    vi.mocked(api.capabilities).mockResolvedValue({ ...capabilities, harnessImages: packs });
    renderDialog();

    const harness = await screen.findByRole("combobox", { name: "Harness" });
    expect(harness).toHaveTextContent("OpenCode");
    harness.focus();
    await userEvent.keyboard("{ArrowDown}");
    const list = screen.getByRole("listbox", { name: "Harness" });
    const openCode = within(list).getByRole("option", { name: "OpenCode" });
    expect(openCode).toHaveAttribute("aria-selected", "true");
    expect(openCode.querySelector(".harness-mark")).toHaveClass("harness-mark-md");
    expect(openCode.querySelector(".harness-logo")).not.toHaveClass("harness-select-check");
    expect(openCode.querySelector(":scope > .harness-select-check")).toBeInTheDocument();
    expect(within(list).getByRole("option", { name: "Pi" })).toHaveAttribute("aria-selected", "false");
    await userEvent.keyboard("{ArrowDown}{Escape}");
    expect(screen.queryByRole("listbox", { name: "Harness" })).not.toBeInTheDocument();
    expect(harness).toHaveTextContent("OpenCode");

    harness.focus();
    await userEvent.keyboard("p");
    expect(harness).toHaveTextContent("Pi");
    await userEvent.keyboard("{ArrowDown}{ArrowDown}{Enter}");
    expect(harness).toHaveTextContent("OpenCode");
    expect(screen.queryByRole("listbox", { name: "Harness" })).not.toBeInTheDocument();
  });

  it("asks which Project only when none is current, recent first", async () => {
    const older = { ...project, id: "project-old", name: "Older", updatedAt: "2026-01-01T00:00:00Z" };
    const newer = { ...project, id: "project-new", name: "Newer", updatedAt: "2026-11-01T00:00:00Z" };
    vi.mocked(api.projects).mockResolvedValue({ items: [older, newer] });
    const onProjectChange = vi.fn();
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <NewSessionDialog projectId="" onProjectChange={onProjectChange} onClose={vi.fn()} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const dialog = await screen.findByRole("dialog", { name: "Choose a project" });
    const options = within(dialog).getAllByRole("option");
    expect(options.map((option) => option.textContent)).toEqual(["Newer", "Older"]);
    await userEvent.type(within(dialog).getByRole("searchbox"), "old");
    expect(within(dialog).getAllByRole("option")).toHaveLength(1);
    await userEvent.keyboard("{Enter}");
    expect(onProjectChange).toHaveBeenCalledWith("project-old");
  });

  it("allows a terminal-only harness without a provider even when Thread is the initial preference", async () => {
    vi.mocked(api.capabilities).mockResolvedValue({
      ...capabilities,
      harnessImages: [{ ...project.harnessImages![0]!, interactionModes: ["native"] }],
    });
    renderDialog();

    await waitFor(() => expect(screen.getByRole("tab", { name: "CLI" })).toHaveAttribute("aria-selected", "true"));
    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeEnabled());
    expect(screen.queryByRole("link", { name: "Connect a provider" })).not.toBeInTheDocument();
  });
});
