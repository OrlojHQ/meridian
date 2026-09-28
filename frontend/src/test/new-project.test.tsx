import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { HarnessImage, Project } from "../api/generated/types.gen";
import { NewProjectDialog } from "../components/NewProjectDialog";

// Same order as domain.InstallationHarnessImages: real agents first, mock last.
const catalog: HarnessImage[] = [
  { name: "opencode", imageReference: "ghcr.io/orlojhq/meridian-capsule-opencode:v0.1.1" },
  { name: "pi", imageReference: "ghcr.io/orlojhq/meridian-capsule-pi:v0.1.1" },
  { name: "claude", imageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.1.1" },
  { name: "codex", imageReference: "ghcr.io/orlojhq/meridian-capsule-codex:v0.1.1" },
  { name: "mock", imageReference: "ghcr.io/orlojhq/meridian-capsule:v0.1.1" },
];

const preferenceKey = "meridian.newProjectHarness";

const project: Project = {
  id: "project-1",
  name: "Meridian",
  createdAt: "2026-09-27T00:00:00Z",
  updatedAt: "2026-09-27T00:00:00Z",
  resourceVersion: 1,
};

function renderDialog(harnessImages = catalog) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const onClose = vi.fn();
  const view = render(
    <QueryClientProvider client={client}>
      <NewProjectDialog open harnessImages={harnessImages} onClose={onClose} />
    </QueryClientProvider>,
  );
  return { ...view, onClose };
}

const harnessSelect = () => screen.getByLabelText(/Initial harness pack/);

describe("NewProjectDialog", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    localStorage.clear();
  });

  it("preselects a real agent and labels the mock test harness", () => {
    renderDialog();
    expect(harnessSelect()).toHaveValue("opencode");
    expect(
      screen.getByRole("option", { name: "mock (test harness, no agent)" }),
    ).toHaveValue("mock");
    expect(screen.getByRole("option", { name: "claude" })).toHaveValue("claude");
  });

  it("skips mock in catalog order unless it is the only pack", () => {
    const mockFirst = [catalog[4], catalog[3]];
    const first = renderDialog(mockFirst);
    expect(harnessSelect()).toHaveValue("codex");
    first.unmount();

    renderDialog([catalog[4]]);
    expect(harnessSelect()).toHaveValue("mock");
  });

  it("preselects the harness remembered from the last Project", () => {
    localStorage.setItem(preferenceKey, "claude");
    renderDialog();
    expect(harnessSelect()).toHaveValue("claude");
  });

  it("falls back to catalog order when the remembered harness is gone", () => {
    localStorage.setItem(preferenceKey, "retired-agent");
    renderDialog();
    expect(harnessSelect()).toHaveValue("opencode");
  });

  it("remembers only the harness name after a successful create", async () => {
    const create = vi.spyOn(api, "createProject").mockResolvedValue(project);
    const user = userEvent.setup();
    const { onClose } = renderDialog();

    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.selectOptions(harnessSelect(), "codex");
    await user.click(screen.getByRole("button", { name: "Create Project" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(create).toHaveBeenCalledWith({
      name: "Meridian",
      harnessImages: [catalog[3]],
    });
    expect(localStorage.getItem(preferenceKey)).toBe("codex");
  });

  it("does not remember a choice when creation fails", async () => {
    vi.spyOn(api, "createProject").mockRejectedValue(new Error("conflict"));
    const user = userEvent.setup();
    renderDialog();

    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.selectOptions(harnessSelect(), "pi");
    await user.click(screen.getByRole("button", { name: "Create Project" }));

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(localStorage.getItem(preferenceKey)).toBeNull();
  });

  it("works when browser storage throws", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    const create = vi.spyOn(api, "createProject").mockResolvedValue(project);
    const user = userEvent.setup();
    const { onClose } = renderDialog();

    expect(harnessSelect()).toHaveValue("opencode");
    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.click(screen.getByRole("button", { name: "Create Project" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(create).toHaveBeenCalledWith({
      name: "Meridian",
      harnessImages: [catalog[0]],
    });
    expect(setItem).toHaveBeenCalledWith(preferenceKey, "opencode");
  });
});
