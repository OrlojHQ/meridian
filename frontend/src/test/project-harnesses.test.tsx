import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { MemoryRouter, Outlet, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MeridianAPIError, api } from "../api/client";
import type { Capabilities, Project } from "../api/generated/types.gen";
import { NewSessionDialog } from "../components/NewSessionDialog";
import { ProjectSettings } from "../components/ProjectSettings";

const opencode = { name: "opencode", imageReference: "example.test/opencode:v1" };
const pi = { name: "pi", imageReference: "example.test/pi:v1" };
const project: Project = {
  id: "project-1",
  name: "Meridian",
  createdAt: "2026-11-11T00:00:00Z",
  updatedAt: "2026-11-11T00:00:00Z",
  resourceVersion: 1,
  harnessImages: [opencode],
};
const capabilities: Capabilities = {
  providerVersion: "docker/v1", isolation: "container" as const,
  attach: true,
  run: true,
  structured: false,
  git: false,
  pause: false,
  snapshot: false,
  clone: false,
  browse: false,
  delivery: false,
  preview: false,
  resourceMetrics: false,
  harnessImages: [opencode, pi],
};

function SettingsLayout() {
  const [projectId, setProjectId] = useState("");
  return <>
    <Outlet context={{ openNewSession: setProjectId }} />
    {projectId && (
      <NewSessionDialog
        projectId={projectId}
        onProjectChange={setProjectId}
        onClose={() => setProjectId("")}
      />
    )}
  </>;
}

function renderView() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/projects/project-1"]}>
        <Routes>
          <Route element={<SettingsLayout />}>
            <Route path="/ui/projects/:projectId" element={<ProjectSettings />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  localStorage.clear();
  vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
  vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
  vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: false });
  vi.spyOn(api, "projectHarnessSetup").mockResolvedValue({ setup: "" });
  vi.spyOn(api, "projectChecks").mockResolvedValue({ checks: [], autoRework: true, maxReworkAttempts: 3, resourceVersion: 1 });
  vi.spyOn(api, "suggestProjectChecks").mockResolvedValue({ checks: [] });
  vi.spyOn(api, "allRoutines").mockResolvedValue({ items: [] });
  vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Project harness management", () => {
  it("adds a second harness in settings and offers both for a new Capsule", async () => {
    const updated = { ...project, resourceVersion: 2, harnessImages: [opencode, pi] };
    const apply = vi.spyOn(api, "applyProjectHarness").mockImplementation(async () => {
      vi.mocked(api.projects).mockResolvedValue({ items: [updated] });
      return updated;
    });
    renderView();

    const packs = await screen.findByRole("group", { name: "Harness packs" });
    expect(within(packs).getByText("OpenCode")).toBeInTheDocument();
    expect(within(packs).queryByRole("button", { name: "Add OpenCode" })).not.toBeInTheDocument();
    await userEvent.click(await within(packs).findByRole("button", { name: "Add Pi" }));
    expect(apply).toHaveBeenCalledWith(project.id, pi);
    await waitFor(() => expect(within(packs).queryByRole("button", { name: "Add Pi" })).not.toBeInTheDocument());
    expect(within(packs).getByText("Pi")).toBeInTheDocument();
    expect(within(packs).getByText("All installed agents have been added to this Project.")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "New Capsule" }));
    const dialog = screen.getByRole("dialog", { name: "New Capsule" });
    const harness = within(dialog).getByRole("combobox", { name: "Harness" });
    expect(harness).toHaveTextContent("OpenCode");
    await userEvent.click(harness);
    const options = within(dialog).getByRole("listbox", { name: "Harness" });
    expect(within(options).getByRole("option", { name: "OpenCode" })).toHaveAttribute("aria-selected", "true");
    await userEvent.click(within(options).getByRole("option", { name: "Pi" }));
    expect(harness).toHaveTextContent("Pi");
    expect(within(dialog).queryByRole("listbox")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Start" })).toBeInTheDocument();
  });

  it("keeps the existing harness and allows retrying a failed addition", async () => {
    const apply = vi.spyOn(api, "applyProjectHarness").mockRejectedValueOnce(
      new MeridianAPIError("Project changed; try again", "conflict", 409),
    );
    renderView();
    await userEvent.click(await screen.findByRole("button", { name: "Add Pi" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Project changed; try again");
    expect(screen.getByText("OpenCode")).toBeInTheDocument();
    apply.mockImplementation(async () => {
      const updated = { ...project, resourceVersion: 2, harnessImages: [opencode, pi] };
      vi.mocked(api.projects).mockResolvedValue({ items: [updated] });
      return updated;
    });
    await userEvent.click(screen.getByRole("button", { name: "Add Pi" }));
    expect(await screen.findByText("Pi")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("reports catalog errors while loading and retries", async () => {
    let rejectCatalog!: (error: Error) => void;
    vi.mocked(api.capabilities).mockImplementationOnce(() => new Promise((_, reject) => {
      rejectCatalog = reject;
    }));
    const updated = { ...project, resourceVersion: 2, harnessImages: [opencode, pi] };
    const apply = vi.spyOn(api, "applyProjectHarness").mockImplementation(async () => {
      vi.mocked(api.projects).mockResolvedValue({ items: [updated] });
      return updated;
    });
    renderView();

    expect(await screen.findByText("Loading installed agents…")).toBeInTheDocument();
    rejectCatalog(new MeridianAPIError("Catalog unavailable", "unavailable", 503));
    expect(await screen.findByRole("alert")).toHaveTextContent("Catalog unavailable");
    await userEvent.click(screen.getByRole("button", { name: "Retry loading agents" }));
    await userEvent.click(await screen.findByRole("button", { name: "Add Pi" }));
    expect(apply).toHaveBeenCalledWith(project.id, pi);
    const packs = screen.getByRole("group", { name: "Harness packs" });
    expect(await within(packs).findByText("Pi")).toBeInTheDocument();
    expect(within(packs).getByText("OpenCode")).toBeInTheDocument();
  });

  it.each([
    [[], "This Meridian installation has no agent images."],
    [[opencode], "All installed agents have been added to this Project."],
  ])("explains why there are no additional harnesses (%j)", async (harnessImages, message) => {
    vi.mocked(api.capabilities).mockResolvedValue({ ...capabilities, harnessImages });
    renderView();
    expect(await screen.findByText(message, { exact: false })).toBeInTheDocument();
  });
});
