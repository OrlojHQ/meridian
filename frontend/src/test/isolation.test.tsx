import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { IsolationClass, Project } from "../api/generated/types.gen";
import { NewProjectDialog } from "../components/NewProjectDialog";
import { ProjectSettings } from "../components/ProjectSettings";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { queries } from "../api/queries";

const project: Project = {
  id: "project-1",
  name: "Meridian",
  createdAt: "2026-11-15T00:00:00Z",
  updatedAt: "2026-11-15T00:00:00Z",
  resourceVersion: 1,
};

function renderDialog(verifiedIsolation?: IsolationClass) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const onClose = vi.fn();
  const view = render(
    <QueryClientProvider client={client}>
      <NewProjectDialog
        open
        harnessImages={[]}
        verifiedIsolation={verifiedIsolation}
        onClose={onClose}
      />
    </QueryClientProvider>,
  );
  return { ...view, onClose };
}

function renderSettings(project: Project) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
  vi.spyOn(api, "projectChecks").mockResolvedValue({ checks: [], autoRework: true, maxReworkAttempts: 3, resourceVersion: 1 });
  vi.spyOn(api, "suggestProjectChecks").mockResolvedValue({ checks: [] });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/ui/projects/${project.id}/settings`]}>
        <Routes>
          <Route
            path="/ui/projects/:projectId/settings"
            element={<ProjectSettings />}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const isolationSelect = () => screen.getByLabelText("Isolation");

describe("NewProjectDialog isolation", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("renders the isolation select with every class offered", () => {
    renderDialog("container");
    expect(isolationSelect()).toHaveValue("");
    expect(screen.getByRole("option", { name: "No requirement" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Container" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "Sandbox (gVisor-class)" })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "MicroVM" })).toBeInTheDocument();
    expect(screen.getByText("This Station provides container isolation.")).toBeInTheDocument();
  });

  it("disables stronger options for a container Station", () => {
    renderDialog("container");
    expect(screen.getByRole("option", { name: "Container" })).not.toBeDisabled();
    expect(screen.getByRole("option", { name: "Sandbox (gVisor-class)" })).toBeDisabled();
    expect(screen.getByRole("option", { name: "MicroVM" })).toBeDisabled();
  });

  it("sends requiredIsolation only when a class is chosen", async () => {
    const create = vi.spyOn(api, "createProject").mockResolvedValue(project);
    const user = userEvent.setup();

    renderDialog("sandbox");
    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.click(screen.getByRole("button", { name: "Create Project" }));
    await waitFor(() => expect(create).toHaveBeenCalled());
    expect(create).toHaveBeenLastCalledWith({ name: "Meridian" });

    await user.selectOptions(isolationSelect(), "sandbox");
    await user.click(screen.getByRole("button", { name: "Create Project" }));
    await waitFor(() => expect(create).toHaveBeenCalledTimes(2));
    expect(create).toHaveBeenLastCalledWith({
      name: "Meridian",
      requiredIsolation: "sandbox",
    });
  });

  it("shows the server message when the floor is refused", async () => {
    vi.spyOn(api, "createProject").mockRejectedValue({
      error: {
        code: "unsupported",
        message: "provider capability unsupported: microvm isolation is required; this installation provides container",
      },
    });
    const user = userEvent.setup();
    renderDialog("container");

    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.click(screen.getByRole("button", { name: "Create Project" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "microvm isolation is required; this installation provides container",
    );
  });
});

describe("ProjectSettings isolation label", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
  });

  it("shows the requirement on a Project that has one", async () => {
    renderSettings({ ...project, requiredIsolation: "microvm" });
    expect(await screen.findByText(/Requires MicroVM/)).toBeInTheDocument();
  });

  it("shows no requirement label when the field is absent", async () => {
    renderSettings(project);
    await screen.findByText("Meridian");
    expect(screen.queryByText(/Requires MicroVM/)).not.toBeInTheDocument();
  });
});
