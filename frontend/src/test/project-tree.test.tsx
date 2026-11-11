import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import type { Capsule, Project } from "../api/generated/types.gen";
import type { ActivityGroup } from "../shell/capsuleActivity";
import { ProjectTree } from "../shell/ProjectTree";
import type { WorkingSetItem } from "../shell/useWorkingSet";

const project = (id: string, name: string, updatedAt: string, extra: Partial<Project> = {}): Project => ({
  id,
  name,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt,
  resourceVersion: 1,
  ...extra,
});

const item = (id: string, projectId: string, since: string, group: ActivityGroup = "idle"): WorkingSetItem => ({
  capsule: {
    id,
    projectId,
    timelineId: `timeline-${id}`,
    name: id,
    state: "Ready",
    desiredState: "Ready",
    restoreComplete: true,
    createdAt: since,
    updatedAt: since,
    resourceVersion: 1,
  } as Capsule,
  runs: [],
  threads: [],
  activity: { group, label: group, agent: "Codex", since },
  sessions: [],
});

function show(props: Partial<Parameters<typeof ProjectTree>[0]> = {}) {
  const onNewCapsule = vi.fn();
  const onDeleteProject = vi.fn();
  render(
    <MemoryRouter>
      <ProjectTree
        projects={[
          project("old", "old-project", "2026-11-02T00:00:00Z"),
          project("busy", "busy-project", "2026-11-03T00:00:00Z"),
          project("empty", "empty-project", "2026-11-04T00:00:00Z"),
        ]}
        items={[
          item("fix-login", "busy", "2026-11-10T10:00:00Z"),
          item("docs-pass", "busy", "2026-11-10T09:00:00Z"),
          item("needs-review", "busy", "2026-11-10T08:00:00Z", "attention"),
          item("spike", "old", "2026-11-05T00:00:00Z"),
        ]}
        filter=""
        renderItem={(entry) => <li key={entry.capsule.id}>{entry.capsule.name}</li>}
        onNewCapsule={onNewCapsule}
        onDeleteProject={onDeleteProject}
        {...props}
      />
    </MemoryRouter>,
  );
  return { onNewCapsule, onDeleteProject };
}

beforeEach(() => localStorage.clear());
afterEach(() => {
  cleanup();
  localStorage.clear();
});

it("lists Projects by latest activity with their Capsules nested", () => {
  show();
  const toggles = screen.getAllByRole("button", { expanded: true });
  expect(toggles.map((toggle) => toggle.querySelector(".project-name")?.textContent)).toEqual([
    "busy-project",
    "old-project",
    "empty-project",
  ]);
  const busy = screen.getByRole("list", { name: "busy-project Capsules" });
  expect(within(busy).getAllByRole("listitem").map((row) => row.textContent)).toEqual(["fix-login", "docs-pass"]);
  // A Capsule that needs you is counted here but listed once, under Needs you.
  expect(toggles[0]).toHaveTextContent("1 need you");
  expect(toggles[0]).toHaveTextContent("3 Capsules");
  expect(within(busy).queryByText("needs-review")).toBeNull();
});

it("shows a new Project with no Capsules and offers to start one", async () => {
  const { onNewCapsule } = show();
  expect(screen.getByText(/No Capsules yet/)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Start one" }));
  expect(onNewCapsule).toHaveBeenCalledWith(expect.objectContaining({ id: "empty" }));
});

it("puts start, settings, and delete on each Project row", async () => {
  const { onNewCapsule, onDeleteProject } = show();
  await userEvent.click(screen.getByRole("button", { name: "New Capsule in busy-project" }));
  expect(onNewCapsule).toHaveBeenCalledWith(expect.objectContaining({ id: "busy" }));
  expect(screen.getByRole("link", { name: "busy-project settings" })).toHaveAttribute("href", "/ui/projects/busy");
  await userEvent.click(screen.getByRole("button", { name: "Delete old-project" }));
  expect(onDeleteProject).toHaveBeenCalledWith(expect.objectContaining({ id: "old" }));
});

it("remembers collapsed Projects and keeps the open Capsule's Project expanded", async () => {
  show();
  await userEvent.click(screen.getByRole("button", { name: /^old-project/, expanded: true }));
  expect(screen.queryByText("spike")).toBeNull();
  expect(JSON.parse(localStorage.getItem("meridian.sidebar.collapsedProjects") ?? "[]")).toEqual(["old"]);
  cleanup();
  show();
  expect(screen.getByRole("button", { name: /^old-project/ })).toHaveAttribute("aria-expanded", "false");
  cleanup();
  show({ activeProjectId: "old" });
  expect(screen.getByText("spike")).toBeInTheDocument();
});

it("works when browser storage refuses access", async () => {
  const getItem = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
    throw new Error("denied");
  });
  const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new Error("denied");
  });
  try {
    show();
    await userEvent.click(screen.getByRole("button", { name: /^old-project/, expanded: true }));
    expect(screen.queryByText("spike")).toBeNull();
  } finally {
    getItem.mockRestore();
    setItem.mockRestore();
  }
});

it("filters by Project or Capsule name and shows failed deletes on the row", () => {
  show({
    filter: "busy",
    items: [item("fix-login", "busy", "2026-11-10T10:00:00Z")],
    projects: [
      project("busy", "busy-project", "2026-11-03T00:00:00Z", { state: "failed", failure: "container in use" }),
      project("old", "old-project", "2026-11-02T00:00:00Z"),
    ],
  });
  expect(screen.queryByText("old-project")).toBeNull();
  expect(screen.getByRole("status")).toHaveTextContent("Deletion stopped: container in use");
});
