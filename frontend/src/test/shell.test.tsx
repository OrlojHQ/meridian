import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type {
  Capabilities,
  Capsule,
  Project,
} from "../api/generated/types.gen";
import { AppShell } from "../shell/AppShell";
import { WorkspaceContext } from "../shell/WorkspaceContext";

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

const capsule: Capsule = {
  id: "capsule-1",
  projectId: project.id,
  timelineId: "timeline-1",
  name: "Review the launcher",
  harness: "opencode",
  state: "Ready",
  desiredState: "Ready",
  restoreComplete: true,
  createdAt: "2026-08-26T00:00:00Z",
  updatedAt: "2026-08-26T00:01:00Z",
  resourceVersion: 2,
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

function wrapper(node: React.ReactNode, initial = "/ui/") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[initial]}>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("application shell", () => {
  it.each(["/ui/", "/ui/settings/harnesses"])("closes the launcher when navigating to provider settings from %s", async (initial) => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
    vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
    vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: true });
    vi.spyOn(api, "projectHarnessSetup").mockResolvedValue({ setup: "" });
    vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
          <Route path="settings/harnesses" element={<h1>Import from your computer</h1>} />
        </Route>
      </Routes>, initial,
    );
    await userEvent.click(await screen.findByRole("button", { name: "New Capsule in Meridian" }));
    expect(screen.getByRole("dialog", { name: "Start work in Meridian" })).toBeInTheDocument();
    await userEvent.click(await screen.findByRole("link", { name: "Connect provider" }));
    expect(await screen.findByRole("heading", { name: "Import from your computer" })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await userEvent.click(screen.getByRole("button", { name: "New Capsule in Meridian" }));
    expect(screen.getByRole("dialog", { name: "Start work in Meridian" })).toBeInTheDocument();
  });

  it("creates Projects globally and Capsules within a Project", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({
      items: [
        capsule,
        {
          ...capsule,
          id: "capsule-deleted",
          name: "Deleted harness work",
          state: "Deleted",
          desiredState: "Deleted",
        },
      ],
    });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
    const createProject = vi.spyOn(api, "createProject").mockResolvedValue({
      ...project,
      id: "project-2",
      name: "Second Project",
    });

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );

    expect(
      await screen.findByRole("link", { name: /Review the launcher/ }),
    ).toBeInTheDocument();
    expect(screen.queryByText("Deleted harness work")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "New project" }));
    expect(
      screen.getByRole("dialog", { name: "Create a Project" }),
    ).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Project name"), "Second Project");
    await userEvent.click(screen.getByRole("button", { name: "Create Project" }));
    await waitFor(() =>
      expect(createProject).toHaveBeenCalledWith({
        name: "Second Project",
        harnessImages: project.harnessImages,
      }),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Create a Project" }),
      ).not.toBeInTheDocument(),
    );

    await userEvent.click(
      screen.getByRole("button", { name: "New Capsule in Meridian" }),
    );
    expect(
      screen.getByRole("dialog", { name: "Start work in Meridian" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("tab", { name: "Native harness" }),
    ).toHaveAttribute("aria-selected", "true");
    await userEvent.click(
      screen.getByRole("tab", { name: "Structured Thread" }),
    );
    expect(
      screen.getByLabelText("Structured harness"),
    ).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Start work in Meridian" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("gates unsupported context tools without issuing their requests", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    const diff = vi.spyOn(api, "gitDiff");
    const files = vi.spyOn(api, "workspaceFiles");

    wrapper(
      <WorkspaceContext
        capsuleId={capsule.id}
        pathname={`/ui/capsules/${capsule.id}`}
        capabilities={capabilities}
      />,
    );

    expect(
      await screen.findByText(/Git review is unsupported/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("tab", { name: "Terminal" }),
    ).not.toBeInTheDocument();
    expect(diff).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("tab", { name: "Files" }));
    expect(
      await screen.findByText(/Workspace browsing is unsupported/),
    ).toBeInTheDocument();
    expect(files).not.toHaveBeenCalled();
  });
});
