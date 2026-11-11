import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Capabilities, Capsule, Project } from "../api/generated/types.gen";
import { HomeComposer } from "../components/HomeComposer";

const project: Project = {
  id: "project-1",
  name: "testing",
  harnessImages: [{ name: "opencode", imageReference: "example.test/opencode:v1" }],
  createdAt: "2026-11-11T00:00:00Z",
  updatedAt: "2026-11-11T00:00:00Z",
  resourceVersion: 1,
};
const capabilities: Capabilities = {
  providerVersion: "docker/v1",
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

function renderComposer() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/"]}>
        <Routes>
          <Route path="/ui/" element={<HomeComposer />} />
          <Route path="/ui/capsules/:capsuleId" element={<p>Capsule opened</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
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

describe("Home composer provider requirements", () => {
  it("explains the Thread requirement and lets CLI start without a saved connection", async () => {
    const create = vi.spyOn(api, "createCapsule").mockResolvedValue(capsule);
    const spawn = vi.spyOn(api, "createProjectThread");
    const grant = vi.spyOn(api, "grantConnection");
    renderComposer();

    await userEvent.type(await screen.findByRole("textbox", { name: "What should we work on?" }), "Inspect the repository");
    const start = screen.getByRole("button", { name: "Start" });
    await waitFor(() => expect(start).toHaveAccessibleDescription(/Thread mode needs a provider connection/));
    expect(start).toBeDisabled();
    expect(screen.getByText(/switch to CLI to sign in inside OpenCode/)).toBeInTheDocument();
    expect(spawn).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("tab", { name: "CLI" }));
    expect(start).toBeEnabled();
    expect(screen.queryByRole("link", { name: "Connect a provider" })).not.toBeInTheDocument();
    expect(screen.getByText("You can sign in inside OpenCode after it opens.")).toBeInTheDocument();
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
    renderComposer();

    await userEvent.type(await screen.findByRole("textbox", { name: "What should we work on?" }), "Inspect the repository");
    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Start" }));
    expect(await screen.findByText("Capsule opened")).toBeInTheDocument();
    expect(spawn).toHaveBeenCalledWith(project.id, "opencode", "Inspect the repository");
  });

  it("allows a terminal-only harness without a provider even when Thread is the initial preference", async () => {
    vi.mocked(api.capabilities).mockResolvedValue({
      ...capabilities,
      harnessImages: [{ ...project.harnessImages![0]!, interactionModes: ["native"] }],
    });
    renderComposer();

    await waitFor(() => expect(screen.getByRole("tab", { name: "CLI" })).toHaveAttribute("aria-selected", "true"));
    await waitFor(() => expect(screen.getByRole("button", { name: "Start" })).toBeEnabled());
    expect(screen.queryByRole("link", { name: "Connect a provider" })).not.toBeInTheDocument();
  });
});
