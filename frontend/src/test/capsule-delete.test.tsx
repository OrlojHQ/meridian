import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { App } from "../App";
import { MeridianAPIError, api } from "../api/client";
import type { Capsule, Capabilities, Project } from "../api/generated/types.gen";

const project: Project = {
  id: "project-1",
  name: "Meridian",
  harnessImages: [
    {
      name: "opencode",
      imageReference: "ghcr.io/orlojhq/meridian-capsule-opencode:v1",
    },
  ],
  createdAt: "2026-08-26T00:00:00Z",
  updatedAt: "2026-08-26T00:00:00Z",
  resourceVersion: 1,
};

const otherProject: Project = {
  ...project,
  id: "project-2",
  name: "Other",
};

const capabilities: Capabilities = {
  providerVersion: "fake/v1",
  attach: false,
  run: false,
  structured: false,
  git: false,
  pause: false,
  snapshot: false,
  clone: false,
  preview: false,
  browse: false,
  delivery: false,
  resourceMetrics: false,
  harnessImages: project.harnessImages,
};

function capsule(overrides: Partial<Capsule> & Pick<Capsule, "id" | "name">): Capsule {
  return {
    projectId: project.id,
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
  name: "Review the launcher",
  createdAt: "2026-08-26T03:00:00Z",
  updatedAt: "2026-08-26T03:00:00Z",
});

const recent = capsule({
  id: "capsule-recent",
  name: "Recent work",
  createdAt: "2026-08-26T00:00:00Z",
  updatedAt: "2026-08-26T05:00:00Z",
});

const createdLater = capsule({
  id: "capsule-created",
  name: "Created later",
  createdAt: "2026-08-26T04:00:00Z",
  updatedAt: "2026-08-26T01:00:00Z",
});

const deleting = capsule({
  id: "capsule-deleting",
  name: "Going away",
  state: "Deleting",
  desiredState: "Deleted",
  updatedAt: "2026-08-26T08:00:00Z",
});

const elsewhere = capsule({
  id: "capsule-other",
  name: "Other project",
  projectId: otherProject.id,
  updatedAt: "2026-08-26T09:00:00Z",
});

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

function renderApp(initial: string | string[], index?: number) {
  const entries = Array.isArray(initial) ? initial : [initial];
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={entries} initialIndex={index ?? entries.length - 1}>
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
  // Stay on the pre-delete record. Navigation must not wait for Deleted.
  vi.spyOn(api, "capsule").mockImplementation(async (id: string) => {
    const found = capsules.find((item) => item.id === id);
    if (!found) throw new MeridianAPIError("Capsule missing", "not_found", 404);
    return found;
  });
  vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
  vi.spyOn(api, "threads").mockResolvedValue({ items: [] });
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

async function confirmDelete() {
  await userEvent.click(await screen.findByRole("button", { name: "Capsule actions" }));
  await userEvent.click(screen.getByRole("menuitem", { name: "Delete…" }));
  await userEvent.click(screen.getByRole("button", { name: "Confirm delete" }));
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("delete navigation", () => {
  it("replaces the Capsule page with the most recent sibling and Back skips it", async () => {
    mockAPI([current, recent, createdLater, deleting, elsewhere]);
    const deleted = vi.spyOn(api, "lifecycle").mockResolvedValue({
      ...current,
      state: "Deleting",
      desiredState: "Deleted",
    });
    renderApp("/ui/");

    const sidebar = await screen.findByRole("complementary", { name: "Activity" });
    await userEvent.click(await within(sidebar).findByRole("link", { name: /Review the launcher/ }));
    expect(location()).toBe("/ui/capsules/capsule-current");
    await screen.findByRole("link", { name: /Recent work/ });
    await confirmDelete();

    await waitFor(() => expect(location()).toBe("/ui/capsules/capsule-recent"));
    expect(deleted).toHaveBeenCalledWith("delete", current.id, current.resourceVersion);
    expect(await screen.findByRole("heading", { name: "Recent work" })).toBeInTheDocument();
    expect(location()).not.toContain("capsule-current");

    await userEvent.click(screen.getByRole("button", { name: "History back" }));
    expect(location()).toMatch(/^\/ui\/?$/);
    expect(location()).not.toContain("capsule-current");
  });

  it("opens the Project empty state when nothing remains and Back skips the Capsule", async () => {
    mockAPI([current]);
    vi.spyOn(api, "lifecycle").mockResolvedValue({
      ...current,
      state: "Deleting",
      desiredState: "Deleted",
    });
    renderApp("/ui/");

    const sidebar = await screen.findByRole("complementary", { name: "Activity" });
    await userEvent.click(await within(sidebar).findByRole("link", { name: /Review the launcher/ }));
    expect(location()).toBe("/ui/capsules/capsule-current");
    await confirmDelete();

    await waitFor(() => expect(location()).toBe("/ui/projects/project-1"));
    expect(await screen.findByRole("heading", { name: "Meridian" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start work in Meridian" })).toBeInTheDocument();
    expect(location()).not.toContain("capsule-current");

    await userEvent.click(screen.getByRole("button", { name: "Start work in Meridian" }));
    expect(screen.getByRole("dialog", { name: "Start work in Meridian" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Close new workspace" }));

    await userEvent.click(screen.getByRole("button", { name: "History back" }));
    expect(location()).toMatch(/^\/ui\/?$/);
    expect(location()).not.toContain("capsule-current");
  });

  it("falls back to the Inbox when the Project is not available", async () => {
    mockAPI([current], []);
    vi.spyOn(api, "lifecycle").mockResolvedValue({
      ...current,
      state: "Deleting",
      desiredState: "Deleted",
    });
    renderApp(["/ui/threads", "/ui/capsules/capsule-current"]);

    expect(await screen.findByRole("heading", { name: "Review the launcher" })).toBeInTheDocument();
    await confirmDelete();

    await waitFor(() => expect(location()).toMatch(/^\/ui\/?$/));
    expect(location()).not.toContain("capsule-current");
    expect(screen.getByRole("heading", { name: "No projects yet" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "History back" }));
    expect(location()).toBe("/ui/threads");
    expect(location()).not.toContain("capsule-current");
  });

  it("keeps a direct visit to an already-deleted Capsule", async () => {
    const gone = capsule({
      id: "capsule-deleted",
      name: "Already gone",
      state: "Deleted",
      desiredState: "Deleted",
    });
    mockAPI([gone]);
    const deleted = vi.spyOn(api, "lifecycle");
    renderApp("/ui/capsules/capsule-deleted");

    expect(await screen.findByRole("heading", { name: "Already gone" })).toBeInTheDocument();
    expect(screen.getByText("Deleted")).toBeInTheDocument();
    expect(location()).toBe("/ui/capsules/capsule-deleted");
    await userEvent.click(screen.getByRole("button", { name: "Capsule actions" }));
    expect(screen.queryByRole("menuitem", { name: "Delete…" })).not.toBeInTheDocument();
    expect(deleted).not.toHaveBeenCalled();
    expect(location()).toBe("/ui/capsules/capsule-deleted");
  });

  it("stays put when the delete request fails", async () => {
    mockAPI([current, recent]);
    vi.spyOn(api, "lifecycle").mockRejectedValue(
      new MeridianAPIError("Capsule quota exceeded", "resource_exhausted", 429),
    );
    renderApp("/ui/capsules/capsule-current");

    expect(await screen.findByRole("heading", { name: "Review the launcher" })).toBeInTheDocument();
    await screen.findByRole("link", { name: /Recent work/ });
    await confirmDelete();

    expect(await screen.findByRole("alert")).toHaveTextContent("Capsule quota exceeded");
    expect(location()).toBe("/ui/capsules/capsule-current");
  });

  it("deletes from the command palette and Back skips the Capsule", async () => {
    mockAPI([current, recent]);
    vi.spyOn(navigator, "platform", "get").mockReturnValue("Linux x86_64");
    vi.spyOn(api, "lifecycle").mockResolvedValue({
      ...current,
      state: "Deleting",
      desiredState: "Deleted",
    });
    renderApp(["/ui/", "/ui/capsules/capsule-current"]);

    expect(await screen.findByRole("heading", { name: "Review the launcher" })).toBeInTheDocument();
    await screen.findByRole("link", { name: /Recent work/ });
    await userEvent.keyboard("{Control>}k{/Control}");
    const palette = await screen.findByRole("dialog", { name: "Command palette" });
    await userEvent.type(screen.getByRole("combobox"), "delete");
    await userEvent.keyboard("{Enter}");
    expect(palette).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: "Confirm delete" }));

    await waitFor(() => expect(location()).toBe("/ui/capsules/capsule-recent"));
    await userEvent.click(screen.getByRole("button", { name: "History back" }));
    expect(location()).toMatch(/^\/ui\/?$/);
    expect(location()).not.toContain("capsule-current");
  });
});
