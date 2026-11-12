import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";

import {
  AuthenticationState,
  CapsuleDetail,
  DiffReview,
  Lineage,
  PreviewCard,
  ShipPanel,
} from "../App";
import { MeridianAPIError, api } from "../api/client";
import type {
  Capsule,
  Project,
  Run,
  TimelineView,
} from "../api/generated/types.gen";
import { NativeLauncher } from "../components/NativeLauncher";
import { NewWorkspaceDialog } from "../components/NewWorkspaceDialog";
import { StructuredLauncher } from "../components/StructuredLauncher";
import { WorkspaceFiles } from "../components/WorkspaceFiles";

const project = {
  id: "project-1",
  name: "Project",
  createdAt: "2026-08-23T00:00:00Z",
  updatedAt: "2026-08-23T00:00:00Z",
  resourceVersion: 1,
};

const launcherProject: Project = {
  ...project,
  harnessImages: [
    {
      name: "opencode",
      imageReference: "ghcr.io/orlojhq/meridian-capsule-opencode:v1",
    },
    {
      name: "pi",
      imageReference: "ghcr.io/orlojhq/meridian-capsule-pi:v1",
    },
  ],
};

const capsule: Capsule = {
  id: "capsule-1",
  projectId: project.id,
  timelineId: "timeline-1",
  name: "Workspace",
  state: "Ready",
  desiredState: "Ready",
  restoreComplete: true,
  createdAt: "2026-08-23T00:00:00Z",
  updatedAt: "2026-08-23T00:00:00Z",
  resourceVersion: 2,
};

const timeline: TimelineView = {
  timeline: {
    id: "timeline-1",
    projectId: project.id,
    capsuleId: capsule.id,
    reason: "rewind",
    forkedFromMomentId: "moment-1",
    createdAt: "2026-08-23T00:00:00Z",
  },
  ancestry: [
    {
      id: "timeline-root",
      projectId: project.id,
      capsuleId: "capsule-root",
      reason: "root",
      createdAt: "2026-08-22T00:00:00Z",
    },
  ],
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

describe("Browser authentication", () => {
  it("exchanges and clears the token without browser storage", async () => {
    const storage = vi.spyOn(Storage.prototype, "setItem");
    const authenticate = vi
      .spyOn(api, "authenticateBrowser")
      .mockRejectedValue(new MeridianAPIError("Invalid token", "unauthorized", 401));
    wrapper(<AuthenticationState />);
    const input = screen.getByLabelText("Installation token");
    await userEvent.type(input, "installation-secret");
    await userEvent.click(screen.getByRole("button", { name: "Authenticate" }));
    await waitFor(() => expect(authenticate).toHaveBeenCalledWith("installation-secret"));
    expect(input).toHaveValue("");
    expect(storage).not.toHaveBeenCalled();
  });
});

describe("Capsule list states", () => {
  it("starts a project Thread in a fresh Capsule", async () => {
    const structuredProject: Project = {
      ...project,
      harnessImages: [
        {
          name: "mock",
          imageReference: "ghcr.io/orlojhq/meridian-capsule-mock:v1",
        },
      ],
    };
    const spawn = vi.spyOn(api, "createProjectThread").mockResolvedValue({
      id: "intent-1",
      projectId: project.id,
      capsuleId: "capsule-new",
      capsuleName: "new-session",
      threadId: "thread-new",
      runId: "run-new",
      messageId: "message-new",
      harness: "mock",
      state: "provisioning",
      createdAt: "2026-08-24T12:00:00Z",
      updatedAt: "2026-08-24T12:00:00Z",
      resourceVersion: 1,
    });
    wrapper(<StructuredLauncher project={structuredProject} harness="mock" />);
    await userEvent.type(screen.getByLabelText(/Capsule name/), "new-session");
    await userEvent.type(screen.getByLabelText("Task"), "inspect");
    await userEvent.click(screen.getByRole("button", { name: "Start task" }));
    await waitFor(() =>
      expect(spawn).toHaveBeenCalledWith(
        project.id,
        "mock",
        "inspect",
        "new-session",
        undefined,
      ),
    );
    expect(await screen.findByRole("status")).toHaveTextContent("Preparing your session");
    expect(screen.queryByRole("link", { name: "Open Thread" })).not.toBeInTheDocument();
  });
});

describe("Native harness launcher", () => {
 beforeEach(()=>{
  vi.spyOn(api,"harnessSetups").mockResolvedValue({items:[]});
  vi.spyOn(api,"providerConnections").mockResolvedValue({items:[],enabled:false});
  vi.spyOn(api,"projectConnection").mockResolvedValue({connectionId:""});
  vi.spyOn(api,"projectHarnessSetup").mockResolvedValue({setup:""});
 });
  it("creates an allowlisted launcher Capsule and opens its first Run", async () => {
    const launchedCapsule: Capsule = {
      ...capsule,
      id: "capsule-native",
      name: "native-work",
      harness: "opencode",
      state: "Creating",
    };
    const launchedRun: Run = {
      id: "run-native",
      capsuleId: launchedCapsule.id,
      harness: "opencode",
      state: "Running",
      createdAt: "2026-08-26T12:00:00Z",
      startedAt: "2026-08-26T12:00:01Z",
      updatedAt: "2026-08-26T12:00:01Z",
      resourceVersion: 2,
    };
    const create = vi
      .spyOn(api, "createCapsule")
      .mockResolvedValue(launchedCapsule);
    vi.spyOn(api, "capsule").mockResolvedValue({
      ...launchedCapsule,
      state: "Ready",
    });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [launchedRun] });

    wrapper(
      <Routes>
        <Route
          path="/ui"
          element={<NativeLauncher project={launcherProject} harness="opencode" />}
        />
        <Route
          path="/ui/runs/:runId/terminal"
          element={<p>Native terminal opened</p>}
        />
      </Routes>,
    );

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Start OpenCode" })).toBeEnabled(),
    );
    await userEvent.click(screen.getByText("Customize"));
    await userEvent.type(screen.getByLabelText("Capsule name"), "native-work");
    await userEvent.click(
      screen.getByRole("button", {
        name: "Start OpenCode",
      }),
    );
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        launcherProject.id,
        "native-work",
        "opencode",
        undefined,
        "",
      ),
    );
    expect(await screen.findByText("Native terminal opened")).toBeInTheDocument();
  });

  it("adds an installed agent to a Project without leaving the launcher", async () => {
    const opencode = launcherProject.harnessImages![0]!;
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "docker/v1",
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
      harnessImages: launcherProject.harnessImages,
    });
    const apply = vi.spyOn(api, "applyProjectHarness").mockResolvedValue({
      ...project,
      resourceVersion: 2,
      harnessImages: [opencode],
    });
    wrapper(<NewWorkspaceDialog open project={project} onClose={() => undefined} />);

    expect(await screen.findByText(/Project has no agents yet/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Start / })).not.toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: "Add OpenCode" }));
    expect(apply).toHaveBeenCalledWith(project.id, opencode);
    expect(await screen.findByRole("radio", { name: "OpenCode" })).toBeChecked();
    expect(screen.getByRole("button", { name: "Start OpenCode" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Give it a task" })).not.toBeInTheDocument();
  });

  const claudePack = {
    name: "claude",
    imageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v1",
  };
  const mockPack = {
    name: "mock",
    imageReference: "ghcr.io/orlojhq/meridian-capsule:v1",
  };
  const structuredCapabilities = {
    providerVersion: "docker/v2",
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
    harnessImages: [
      { ...claudePack, interactionModes: ["native" as const] },
      mockPack,
    ],
  };

  it("offers a task only for a pack that can run a structured session", async () => {
    localStorage.clear();
    vi.spyOn(api, "capabilities").mockResolvedValue(structuredCapabilities);
    wrapper(
      <NewWorkspaceDialog
        open
        project={{ ...project, id: "project-modes", harnessImages: [claudePack, mockPack] }}
        onClose={() => undefined}
      />,
    );

    expect(await screen.findByRole("button", { name: "Start Claude Code" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Claude Code" })).toBeChecked();
    expect(screen.queryByRole("tablist", { name: "How to start" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "mock" }));
    const modes = await screen.findByRole("tablist", { name: "How to start" });
    expect(within(modes).getByRole("tab", { name: "Open terminal" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await userEvent.click(within(modes).getByRole("tab", { name: "Give it a task" }));
    expect(screen.getByLabelText("Task")).toBeInTheDocument();

    // Switching to a terminal-only pack never leaves its task form showing.
    await userEvent.click(screen.getByRole("radio", { name: "Claude Code" }));
    expect(screen.queryByRole("tablist", { name: "How to start" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Task")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start Claude Code" })).toBeInTheDocument();
  });

  it("offers only a task for a pack without a terminal session", async () => {
    localStorage.clear();
    const dshPack = { name: "dsh", imageReference: "ghcr.io/orlojhq/meridian-capsule-dsh:v1" };
    vi.spyOn(api, "capabilities").mockResolvedValue({
      ...structuredCapabilities,
      harnessImages: [{ ...dshPack, interactionModes: ["structured" as const] }, mockPack],
    });
    wrapper(
      <NewWorkspaceDialog
        open
        project={{ ...project, id: "project-structured-only", harnessImages: [dshPack, mockPack] }}
        onClose={() => undefined}
      />,
    );

    expect(await screen.findByLabelText("Task")).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "DeepSeek Harness" })).toBeChecked();
    expect(screen.queryByRole("tablist", { name: "How to start" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start DeepSeek Harness" })).not.toBeInTheDocument();
  });

  it("leaves the task choice to the Capsule for a pack outside the catalog", async () => {
    localStorage.clear();
    vi.spyOn(api, "capabilities").mockResolvedValue(structuredCapabilities);
    wrapper(
      <NewWorkspaceDialog
        open
        project={{
          ...project,
          id: "project-custom",
          harnessImages: [{ name: "claude", imageReference: "example.test/claude-structured:1" }],
        }}
        onClose={() => undefined}
      />,
    );

    const modes = await screen.findByRole("tablist", { name: "How to start" });
    expect(within(modes).getByRole("tab", { name: "Give it a task" })).toBeInTheDocument();
  });

  it("shows only the terminal launcher when the provider has no structured sessions", async () => {
    localStorage.clear();
    vi.spyOn(api, "capabilities").mockResolvedValue({
      ...structuredCapabilities,
      structured: false,
    });
    wrapper(
      <NewWorkspaceDialog
        open
        project={{ ...project, id: "project-native", harnessImages: [mockPack] }}
        onClose={() => undefined}
      />,
    );

    expect(await screen.findByRole("button", { name: "Start mock" })).toBeInTheDocument();
    expect(screen.queryByRole("tablist", { name: "How to start" })).not.toBeInTheDocument();
  });

  it("closes the dialog as soon as a task starts", async () => {
    localStorage.clear();
    vi.spyOn(api, "capabilities").mockResolvedValue(structuredCapabilities);
    const spawn = vi.spyOn(api, "createProjectThread").mockResolvedValue({
      id: "intent-1",
      projectId: "project-start-task",
      capsuleId: "capsule-new",
      capsuleName: "new-session",
      threadId: "thread-new",
      runId: "run-new",
      messageId: "message-new",
      harness: "mock",
      state: "provisioning",
      createdAt: "2026-08-24T12:00:00Z",
      updatedAt: "2026-08-24T12:00:00Z",
      resourceVersion: 1,
    });
    const onClose = vi.fn();
    wrapper(
      <NewWorkspaceDialog
        open
        project={{ ...project, id: "project-start-task", harnessImages: [mockPack] }}
        onClose={onClose}
      />,
    );

    await userEvent.click(await screen.findByRole("tab", { name: "Give it a task" }));
    await userEvent.type(screen.getByLabelText("Task"), "inspect");
    await userEvent.click(screen.getByRole("button", { name: "Start task" }));
    await waitFor(() =>
      expect(spawn).toHaveBeenCalledWith(
        "project-start-task",
        "mock",
        "inspect",
        undefined,
        undefined,
      ),
    );
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("closes the dialog as soon as a Capsule is created", async () => {
    localStorage.clear();
    vi.spyOn(api, "capabilities").mockResolvedValue({
      ...structuredCapabilities,
      structured: false,
    });
    const launched: Capsule = {
      ...capsule,
      id: "capsule-native",
      name: "native-work",
      harness: "opencode",
      state: "Creating",
    };
    const create = vi.spyOn(api, "createCapsule").mockResolvedValue(launched);
    vi.spyOn(api, "capsule").mockResolvedValue(launched);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    const onClose = vi.fn();
    wrapper(
      <NewWorkspaceDialog open project={launcherProject} onClose={onClose} />,
    );

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Start OpenCode" })).toBeEnabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Start OpenCode" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        launcherProject.id,
        expect.any(String),
        "opencode",
        undefined,
        "",
      ),
    );
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("keeps the dialog open when starting a task fails", async () => {
    localStorage.clear();
    vi.spyOn(api, "capabilities").mockResolvedValue(structuredCapabilities);
    vi.spyOn(api, "createProjectThread").mockRejectedValue(
      new MeridianAPIError("Capsule quota exceeded", "resource_exhausted", 429),
    );
    const onClose = vi.fn();
    wrapper(
      <NewWorkspaceDialog
        open
        project={{ ...project, id: "project-start-fail", harnessImages: [mockPack] }}
        onClose={onClose}
      />,
    );

    await userEvent.click(await screen.findByRole("tab", { name: "Give it a task" }));
    await userEvent.type(screen.getByLabelText("Task"), "inspect");
    await userEvent.click(screen.getByRole("button", { name: "Start task" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Capsule quota exceeded");
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Start work in Project" })).toBeInTheDocument();
  });

  it("keeps the dialog open when Capsule creation fails", async () => {
    localStorage.clear();
    vi.spyOn(api, "capabilities").mockResolvedValue({
      ...structuredCapabilities,
      structured: false,
    });
    vi.spyOn(api, "createCapsule").mockRejectedValue(
      new MeridianAPIError("Capsule quota exceeded", "resource_exhausted", 429),
    );
    const onClose = vi.fn();
    wrapper(
      <NewWorkspaceDialog open project={launcherProject} onClose={onClose} />,
    );

    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Start OpenCode" })).toBeEnabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Start OpenCode" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Capsule quota exceeded");
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Start work in Project" })).toBeInTheDocument();
  });

  it("keeps the dialog open when the task is empty", async () => {
    localStorage.clear();
    vi.spyOn(api, "capabilities").mockResolvedValue(structuredCapabilities);
    const spawn = vi.spyOn(api, "createProjectThread");
    const onClose = vi.fn();
    wrapper(
      <NewWorkspaceDialog
        open
        project={{ ...project, id: "project-empty-task", harnessImages: [mockPack] }}
        onClose={onClose}
      />,
    );

    await userEvent.click(await screen.findByRole("tab", { name: "Give it a task" }));
    await userEvent.click(screen.getByRole("button", { name: "Start task" }));
    expect(spawn).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog", { name: "Start work in Project" })).toBeInTheDocument();
  });
});

describe("Capsule detail states", () => {
  it("shows loading and errors", async () => {
    vi.spyOn(api, "capsule").mockReturnValue(new Promise(() => undefined));
    const loading = wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      "/ui/capsules/capsule-1",
    );
    expect(screen.getByRole("status")).toHaveTextContent("Loading Capsule");
    loading.unmount();

    vi.spyOn(api, "capsule").mockRejectedValue(
      new MeridianAPIError("Capsule missing", "not_found"),
    );
    wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      "/ui/capsules/capsule-1",
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("Capsule missing");
  });

  it("opens a Capsule without sessions on a new session", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "threads").mockResolvedValue({ items: [] });
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "harnessProfiles").mockResolvedValue({
      items: [{ name: "opencode-structured", structured: true, pty: false }],
    });
    const moments = vi.spyOn(api, "moments").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "docker/v1",
      attach: false,
      run: false,
      structured: true,
      git: false,
      pause: true,
      snapshot: false,
      clone: false,
      browse: false,
      delivery: false,
      preview: false,
      resourceMetrics: false,
    });
    wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      "/ui/capsules/capsule-1",
    );
    expect(await screen.findByRole("heading", { name: "Start a session" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "New session" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText(/Project · updated/)).toBeInTheDocument();
    expect(screen.queryByText(/Edits in Settings apply to the next Capsule/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Capsule actions" }));
    expect(screen.getByRole("menuitem", { name: "Pause" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete…" }));
    expect(
      screen.getByRole("alertdialog", { name: `Delete ${capsule.name}?` }),
    ).toBeInTheDocument();
    expect(moments).not.toHaveBeenCalled();
  });

  it("waits for a requested Thread that is not listed yet instead of offering a new session", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "threads").mockResolvedValue({ items: [] });
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "harnessProfiles").mockResolvedValue({
      items: [{ name: "opencode-structured", structured: true, pty: false }],
    });
    vi.spyOn(api, "moments").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "docker/v1",
      attach: false,
      run: false,
      structured: true,
      git: false,
      pause: true,
      snapshot: false,
      clone: false,
      browse: false,
      delivery: false,
      preview: false,
      resourceMetrics: false,
    });
    wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      "/ui/capsules/capsule-1?session=thread-pending",
    );
    expect(await screen.findByText("Starting the session")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Start a session" })).not.toBeInTheDocument();
  });

  it("shows the pinned harness setup and that Settings edits wait for the next Capsule", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue({
      ...capsule,
      pinnedHarnessSetups: [{
        harness: "codex",
        setupId: "setup",
        name: "Codex home",
        revision: "rev-1",
        revisedAt: "2026-11-09T14:05:00Z",
      }],
    });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "threads").mockResolvedValue({ items: [] });
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "harnessProfiles").mockResolvedValue({
      items: [{ name: "opencode-structured", structured: true, pty: false }],
    });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "docker/v1",
      attach: false,
      run: false,
      structured: true,
      git: false,
      pause: false,
      snapshot: false,
      clone: false,
      browse: false,
      delivery: false,
      preview: false,
      resourceMetrics: false,
    });
    wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      "/ui/capsules/capsule-1",
    );
    expect(
      await screen.findByText(
        "Codex home · revised Nov 9, 14:05 UTC. Edits in Settings apply to the next Capsule.",
      ),
    ).toBeInTheDocument();
  });

  it("marks a Capsule built on an older harness release without offering to change it", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue({
      ...capsule,
      harness: "codex",
      imageReference: "ghcr.io/orlojhq/meridian-capsule-codex:v0.4.0",
    });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "threads").mockResolvedValue({ items: [] });
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "harnessProfiles").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "docker/v1",
      attach: false,
      run: false,
      structured: false,
      git: false,
      pause: false,
      snapshot: false,
      clone: false,
      browse: false,
      delivery: false,
      preview: false,
      resourceMetrics: false,
      harnessImages: [{
        name: "codex",
        imageReference: "ghcr.io/orlojhq/meridian-capsule-codex:v0.4.1",
        interactionModes: ["native", "structured"],
      }],
    });
    wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      "/ui/capsules/capsule-1",
    );
    const pill = await screen.findByText(/Built on v0\.4\.0/);
    expect(pill).toHaveAttribute("title", expect.stringContaining("New Codex Capsules use v0.4.1"));
    expect(pill).toHaveTextContent("new Codex Capsules use v0.4.1");
    expect(screen.queryByRole("button", { name: /upgrade/i })).toBeNull();
  });

  it("offers no new session when the Capsule's harness is terminal-only", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue({ ...capsule, harness: "claude" });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "threads").mockResolvedValue({ items: [] });
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    const profiles = vi.spyOn(api, "harnessProfiles").mockResolvedValue({
      items: [{ name: "claude", structured: false, pty: true }],
    });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "docker/v2",
      attach: false,
      run: false,
      structured: true,
      git: false,
      pause: false,
      snapshot: false,
      clone: false,
      browse: false,
      delivery: false,
      preview: false,
      resourceMetrics: false,
    });
    wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      "/ui/capsules/capsule-1?session=new",
    );
    expect(await screen.findByRole("tab", { name: /Claude Code/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    await waitFor(() => expect(profiles).toHaveBeenCalled());
    expect(screen.queryByRole("tab", { name: "New session" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Start a session" })).not.toBeInTheDocument();
  });

  it("stops running sessions before deleting instead of failing", async () => {
    const nativeCapsule: Capsule = { ...capsule, harness: "claude" };
    const running: Run = {
      id: "run-live",
      capsuleId: nativeCapsule.id,
      harness: "claude",
      state: "Running",
      createdAt: "2026-08-26T00:00:00Z",
      startedAt: "2026-08-26T00:00:01Z",
      updatedAt: "2026-08-26T00:00:01Z",
      resourceVersion: 4,
    };
    const threadRun: Run = { ...running, id: "run-thread", harness: "opencode", resourceVersion: 7 };
    vi.spyOn(api, "capsule").mockResolvedValue(nativeCapsule);
    const runs = vi.spyOn(api, "runs").mockResolvedValue({ items: [running, threadRun] });
    vi.spyOn(api, "threads").mockResolvedValue({
      items: [
        {
          id: "thread-1",
          capsuleId: nativeCapsule.id,
          state: "active",
          harness: "opencode",
          currentRunId: threadRun.id,
          currentRunState: "Running",
          encryptedAtRest: true,
          messageCount: 1,
          encryptedBytes: 64,
          createdAt: "2026-08-26T00:00:00Z",
          updatedAt: "2026-08-26T00:00:02Z",
          resourceVersion: 3,
        },
      ],
    });
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "harnessProfiles").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "docker/v2",
      attach: false,
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
    });
    const cancelRun = vi.spyOn(api, "cancelRun").mockResolvedValue({ ...running, state: "Cancelling" });
    const threadSession = vi.spyOn(api, "threadSession").mockResolvedValue({
      thread: {
        id: "thread-1",
        capsuleId: nativeCapsule.id,
        state: "active",
        harness: "opencode",
        encryptedAtRest: true,
        messageCount: 1,
        encryptedBytes: 64,
        createdAt: "2026-08-26T00:00:00Z",
        updatedAt: "2026-08-26T00:00:03Z",
        resourceVersion: 4,
      },
    });
    const lifecycle = vi.spyOn(api, "lifecycle");
    wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      "/ui/capsules/capsule-1",
    );

    await userEvent.click(await screen.findByRole("button", { name: "Capsule actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete…" }));
    const dialog = screen.getByRole("alertdialog", { name: `Delete ${capsule.name}?` });
    expect(dialog).toHaveTextContent("before deleting");
    const confirm = screen.getByRole("button", { name: "Confirm delete" });
    expect(confirm).toBeDisabled();

    const sessions = within(screen.getByRole("list", { name: "Running sessions" }));
    const [stopTerminal, stopTask] = sessions.getAllByRole("button", { name: "Stop" });
    await userEvent.click(stopTerminal!);
    await waitFor(() => expect(cancelRun).toHaveBeenCalledWith(running.id, running.resourceVersion));
    await userEvent.click(stopTask!);
    await waitFor(() =>
      expect(threadSession).toHaveBeenCalledWith("cancel", "thread-1", 3),
    );
    expect(lifecycle).not.toHaveBeenCalled();

    runs.mockResolvedValue({
      items: [
        { ...running, state: "Cancelled" },
        { ...threadRun, state: "Cancelled" },
      ],
    });
    await waitFor(() => expect(confirm).toBeEnabled(), { timeout: 5_000 });
  });

  it("stops the native terminal from its heading after confirmation", async () => {
    const nativeCapsule: Capsule = { ...capsule, harness: "claude" };
    const running: Run = {
      id: "run-live",
      capsuleId: nativeCapsule.id,
      harness: "claude",
      state: "Running",
      createdAt: "2026-08-26T00:00:00Z",
      startedAt: "2026-08-26T00:00:01Z",
      updatedAt: "2026-08-26T00:00:01Z",
      resourceVersion: 4,
    };
    vi.spyOn(api, "capsule").mockResolvedValue(nativeCapsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [running] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "docker/v2",
      attach: false,
      run: true,
      git: false,
      pause: false,
      snapshot: false,
      clone: false,
      browse: false,
      delivery: false,
      preview: false,
      resourceMetrics: false,
    });
    const cancelRun = vi.spyOn(api, "cancelRun").mockResolvedValue({ ...running, state: "Cancelling" });
    wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      "/ui/capsules/capsule-1",
    );
    await userEvent.click(await screen.findByRole("button", { name: "Stop" }));
    expect(cancelRun).not.toHaveBeenCalled();
    const group = within(screen.getByRole("group", { name: "Confirm stop" }));
    expect(group.getByText("Stop Claude Code?")).toBeInTheDocument();
    await userEvent.click(group.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(cancelRun).toHaveBeenCalledWith(running.id, running.resourceVersion));
  });

  it("explains a failed native Run and starts another one", async () => {
    const nativeCapsule: Capsule = {
      ...capsule,
      harness: "opencode",
    };
    const failedRun: Run = {
      id: "run-failed",
      capsuleId: nativeCapsule.id,
      harness: "opencode",
      state: "Failed",
      exitStatus: -1,
      failure: "run timed out",
      createdAt: "2026-08-26T00:00:00Z",
      startedAt: "2026-08-26T00:00:01Z",
      finishedAt: "2026-08-26T02:00:01Z",
      updatedAt: "2026-08-26T02:00:01Z",
      resourceVersion: 3,
    };
    vi.spyOn(api, "capsule").mockResolvedValue(nativeCapsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [failedRun] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "docker/v1",
      attach: true,
      run: true,
      git: false,
      pause: false,
      snapshot: false,
      clone: false,
      browse: false,
      delivery: false,
      preview: false,
      resourceMetrics: false,
    });
    const start = vi
      .spyOn(api, "startRun")
      .mockReturnValue(new Promise(() => undefined));

    wrapper(
      <Routes>
        <Route path="/ui/capsules/:capsuleId" element={<CapsuleDetail />} />
      </Routes>,
      "/ui/capsules/capsule-1",
    );

    expect(await screen.findByRole("alert")).toHaveTextContent("run timed out");
    await userEvent.click(
      screen.getByRole("button", { name: "Start opencode" }),
    );
    expect(start).toHaveBeenCalledWith(nativeCapsule.id, "opencode");
  });
});

describe("Untrusted review content", () => {
  it("renders diffs as escaped text and marks truncation", () => {
    const { container } = wrapper(
      <DiffReview
        result={{
          content:
            "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n+<script>steal()</script>\n",
          truncated: true,
        }}
      />,
    );
    expect(screen.getByLabelText("Unified diff")).toHaveTextContent(
      "<script>steal()</script>",
    );
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByRole("alert")).toHaveTextContent("truncated");
  });

  it("does not pretend binary changes are renderable", () => {
    wrapper(
      <DiffReview
        result={{
          content:
            "diff --git a/image.png b/image.png\nBinary files a/image.png and b/image.png differ\n",
          truncated: false,
        }}
      />,
    );
    expect(screen.getByText(/Binary changes cannot be displayed/)).toBeInTheDocument();
  });
});

describe("Workspace browsing and shipping", () => {
  it("renders safe text without interpreting HTML and labels binary content", async () => {
    vi.spyOn(api, "workspaceFiles").mockResolvedValue({
      items: [
        { name: "review.txt", type: "file", size: 25, executable: false },
        { name: "image.bin", type: "file", size: 3, executable: false },
      ],
    });
    vi.spyOn(api, "workspaceFile").mockImplementation(async (_capsuleId, path) => ({
      path,
      content: path === "review.txt" ? btoa("<script>unsafe()</script>") : btoa("\0\u0001\u0002"),
      size: path === "review.txt" ? 25 : 3,
      executable: false,
    }));
    const { container } = wrapper(<WorkspaceFiles capsuleId={capsule.id} />);
    await userEvent.click(await screen.findByRole("treeitem", { name: "review.txt" }));
    expect(await screen.findByLabelText("Contents of review.txt")).toHaveTextContent(
      "<script>unsafe()</script>",
    );
    expect(container.querySelector("script")).toBeNull();
    await userEvent.click(screen.getByRole("treeitem", { name: "image.bin" }));
    expect(await screen.findByText("Binary content is unsupported.")).toBeInTheDocument();
  });

  it("requires typed ship approval and binds inspected Git objects", async () => {
    vi.spyOn(api, "deliveryInspection").mockResolvedValue({
      capsuleResourceVersion: capsule.resourceVersion,
      head: "a".repeat(40),
      tree: "b".repeat(40),
      branch: "feature/local",
      defaultBranch: "main",
      dirty: true,
    });
    const delivery = vi.spyOn(api, "createDelivery").mockResolvedValue({
      id: "delivery-1",
      capsuleId: capsule.id,
      projectId: project.id,
      state: "succeeded",
      action: "push",
      proposedBy: "operator",
      approved: true,
      approvedAt: "2026-08-24T00:00:00Z",
      expectedResourceVersion: capsule.resourceVersion,
      expectedHead: "a".repeat(40),
      expectedTree: "b".repeat(40),
      remoteBranch: "feature/remote",
      destinationRef: "refs/heads/feature/remote",
      resultCommitSha: "c".repeat(40),
      createdAt: "2026-08-24T00:00:00Z",
      updatedAt: "2026-08-24T00:00:00Z",
      resourceVersion: 4,
    });
    wrapper(<ShipPanel capsule={capsule} />);
    const ship = await screen.findByRole("button", { name: "Ship" });
    expect(ship).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Destination branch"), "feature/remote");
    await userEvent.type(screen.getByLabelText("Commit message"), "Ship reviewed work");
    await userEvent.type(screen.getByLabelText("Ship confirmation"), "ship");
    expect(ship).toBeEnabled();
    await userEvent.click(ship);
    await waitFor(() =>
      expect(delivery).toHaveBeenCalledWith(
        capsule.id,
        expect.objectContaining({
          approved: true,
          expectedResourceVersion: capsule.resourceVersion,
          expectedHead: "a".repeat(40),
          expectedTree: "b".repeat(40),
          remoteBranch: "feature/remote",
        }),
      ),
    );
  });
});

it("provides a text alternative for Timeline lineage", () => {
  wrapper(<Lineage view={timeline} />);
  expect(
    screen.getByRole("list", { name: "Timeline ancestry text alternative" }),
  ).toHaveTextContent("root Timeline timeline-root");
  expect(screen.getByRole("img", { name: /Timeline lineage/ })).toBeInTheDocument();
});

it("reports preview capability as unavailable", async () => {
  vi.spyOn(api, "capabilities").mockResolvedValue({
    providerVersion: "docker/v1",
    attach: true,
    run: true,
    git: true,
    pause: true,
    snapshot: true,
    clone: true,
      browse: true,
      delivery: true,
    preview: false,
    resourceMetrics: false,
  });
  wrapper(<PreviewCard />);
  await waitFor(() =>
    expect(screen.getByText(/bounded port discovery/)).toBeInTheDocument(),
  );
});

it("discovers previews and exposes expiry and revocation states", async () => {
  vi.spyOn(api, "capabilities").mockResolvedValue({
    providerVersion: "docker/v2",
    attach: true,
    run: true,
    git: true,
    pause: true,
    snapshot: true,
    clone: true,
      browse: true,
      delivery: true,
    preview: true,
    resourceMetrics: false,
  });
  vi.spyOn(api, "previewPorts").mockResolvedValue({ items: [{ port: 3000 }] });
  vi.spyOn(api, "previewTicket").mockResolvedValue({
    url: "http://127.0.0.1:8081/p/token/capsule-1/3000/",
    expiresAt: "2026-08-23T00:00:00Z",
    reusePolicy: "reusable_until_expiry",
  });
  const user = userEvent.setup();
  const active = wrapper(
    <PreviewCard capsuleId="capsule-1" capsuleState="Ready" />,
  );
  await user.click(
    await screen.findByRole("button", { name: "Create preview link" }),
  );
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Preview link for port 3000 expired",
  );
  active.unmount();

  wrapper(<PreviewCard capsuleId="capsule-1" capsuleState="Sealed" />);
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "revoked when a Capsule is sealed",
  );
});
