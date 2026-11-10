import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { App } from "../App";
import { api } from "../api/client";
import type { Capsule, Capabilities, Project, Thread } from "../api/generated/types.gen";

const project: Project = {
  id: "project-1",
  name: "Meridian",
  gitSecretName: "GIT_TOKEN",
  harnessSecretNames: ["TOKEN"],
  state: "active",
  createdAt: "2026-08-26T00:00:00Z",
  updatedAt: "2026-08-26T01:00:00Z",
  resourceVersion: 4,
};

const otherProject: Project = {
  ...project,
  id: "project-2",
  name: "Other",
  gitSecretName: undefined,
  harnessSecretNames: [],
  updatedAt: "2026-08-26T05:00:00Z",
  resourceVersion: 2,
};

const capabilities: Capabilities = {
  providerVersion: "fake/v1",
  attach: false,
  run: false,
  structured: true,
  git: false,
  pause: false,
  snapshot: false,
  clone: false,
  preview: false,
  browse: false,
  delivery: false,
  resourceMetrics: false,
  harnessImages: [],
};

function capsule(overrides: Partial<Capsule> & Pick<Capsule, "id" | "name" | "projectId">): Capsule {
  return {
    timelineId: `timeline-${overrides.id}`,
    harness: "opencode",
    state: "Ready",
    desiredState: "Ready",
    restoreComplete: true,
    createdAt: "2026-08-26T00:00:00Z",
    updatedAt: "2026-08-26T00:00:00Z",
    resourceVersion: 2,
    ...overrides,
  };
}

const current = capsule({
  id: "capsule-current",
  projectId: project.id,
  name: "Review the launcher",
});

const elsewhere = capsule({
  id: "capsule-other",
  projectId: otherProject.id,
  name: "Other work",
  updatedAt: "2026-08-26T06:00:00Z",
});

const thread: Thread = {
  id: "thread-1",
  capsuleId: current.id,
  state: "active",
  harness: "opencode",
  encryptedAtRest: true,
  messageCount: 1,
  encryptedBytes: 32,
  createdAt: "2026-08-26T00:00:00Z",
  updatedAt: "2026-08-26T00:00:00Z",
  resourceVersion: 1,
};

function HistoryProbe() {
  const location = useLocation();
  const navigate = useNavigate();
  return (
    <>
      <p data-testid="location">{`${location.pathname}${location.search}`}</p>
      <button type="button" onClick={() => navigate(-1)}>
        History back
      </button>
    </>
  );
}

function renderApp(initial: string | string[]) {
  const entries = Array.isArray(initial) ? initial : [initial];
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={entries} initialIndex={entries.length - 1}>
        <App />
        <HistoryProbe />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function mockAPI(capsules: Capsule[], projects: Project[] = [project, otherProject]) {
  vi.spyOn(api, "projects").mockResolvedValue({ items: projects });
  vi.spyOn(api, "capsules").mockImplementation(async (projectId: string) => ({
    items: capsules.filter((item) => item.projectId === projectId),
  }));
  vi.spyOn(api, "capsule").mockImplementation(async (id: string) => {
    const found = capsules.find((item) => item.id === id);
    if (!found) throw new Error("missing capsule");
    return found;
  });
  vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
  vi.spyOn(api, "threads").mockImplementation(async (capsuleId: string) => ({
    items: capsuleId === current.id ? [thread, { ...thread, id: "thread-2" }] : [],
  }));
  vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
  vi.spyOn(api, "board").mockResolvedValue({ truncated: false, items: [] });
  vi.spyOn(api, "coordinationRequests").mockResolvedValue({ items: [] });
  vi.spyOn(api, "moments").mockResolvedValue({ items: [] });
  vi.spyOn(api, "harnessProfiles").mockResolvedValue({ items: [] });
  vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: false });
  vi.spyOn(api, "projectHarnessSetup").mockResolvedValue({ setup: "" });
  vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
}

const location = () => screen.getByTestId("location").textContent ?? "";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("project delete", () => {
  it("requires the project name, lists counts, and replaces history with the next Project", async () => {
    mockAPI([current, elsewhere]);
    const deleted = vi.spyOn(api, "deleteProject").mockResolvedValue({
      ...project,
      state: "deleting",
      resourceVersion: 5,
    });
    renderApp(["/ui/", "/ui/capsules/capsule-current"]);

    expect(await screen.findByRole("heading", { name: "Review the launcher" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Delete Meridian" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete Meridian?" });
    await waitFor(() => expect(dialog).toHaveTextContent("1 Capsules"));
    expect(dialog).toHaveTextContent("2 threads");
    expect(dialog).toHaveTextContent("2 secrets");
    expect(dialog).toHaveTextContent("Transcripts and artifacts are deleted");
    const confirm = screen.getByRole("button", { name: "Delete project" });
    expect(confirm).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Project name"), "Other");
    expect(confirm).toBeDisabled();
    await userEvent.clear(screen.getByLabelText("Project name"));
    await userEvent.type(screen.getByLabelText("Project name"), "Meridian");
    await waitFor(() => expect(confirm).toBeEnabled());
    await userEvent.click(confirm);

    await waitFor(() => expect(location()).toBe("/ui/capsules/capsule-other"));
    expect(deleted).toHaveBeenCalledWith(expect.objectContaining({ id: project.id, resourceVersion: 4 }));
    expect(location()).not.toContain("capsule-current");
    expect(location()).not.toContain("project-1");

    await userEvent.click(screen.getByRole("button", { name: "History back" }));
    expect(location()).toMatch(/^\/ui\/?$/);
    expect(location()).not.toContain("capsule-current");
    expect(location()).not.toContain("project-1");
  });

  it("opens the Inbox when the deleted Project is the last one", async () => {
    mockAPI([current], [project]);
    vi.spyOn(api, "deleteProject").mockResolvedValue({ ...project, state: "deleting" });
    renderApp("/ui/projects/project-1");

    await userEvent.click(await screen.findByRole("button", { name: "Delete Meridian" }));
    await userEvent.type(await screen.findByLabelText("Project name"), "Meridian");
    await userEvent.click(screen.getByRole("button", { name: "Delete project" }));

    await waitFor(() => expect(location()).toMatch(/^\/ui\/?$/));
    expect(location()).not.toContain("project-1");
  });
});
