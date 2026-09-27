import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Outlet, Route, Routes } from "react-router-dom";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";

import {
  AuthenticationState,
  CapsuleDetail,
  CapsuleList,
  DiffReview,
  Lineage,
  PreviewCard,
  ShipPanel,
  WorkspaceBrowser,
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
  it("renders loading, empty, and API errors clearly", async () => {
    let resolveProjects: ((value: { items: typeof project[] }) => void) | undefined;
    vi.spyOn(api, "projects").mockReturnValue(
      new Promise((resolve) => {
        resolveProjects = resolve;
      }),
    );
    const loading = wrapper(<CapsuleList />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading Capsules");
    resolveProjects?.({ items: [] });
    expect(await screen.findByRole("heading", { name: "No projects yet" })).toBeInTheDocument();
    loading.unmount();

    vi.spyOn(api, "projects").mockRejectedValue(
      new MeridianAPIError("API unavailable", "network_error"),
    );
    wrapper(<CapsuleList />);
    expect(await screen.findByRole("alert")).toHaveTextContent("API unavailable");
  });

  it("puts what needs the operator first with direct actions", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "fake/v1",
      attach: false,
      run: false,
      structured: true,
      git: true,
      pause: false,
      snapshot: false,
      clone: false,
      preview: false,
      browse: false,
      delivery: false,
      resourceMetrics: false,
    });
    vi.spyOn(api, "capsules").mockResolvedValue({
      items: [
        { ...capsule, harness: "claude" },
        { ...capsule, id: "capsule-2", name: "Broken", state: "Failed", failure: "clone failed" },
        { ...capsule, id: "capsule-3", name: "Parked", state: "Paused", desiredState: "Paused" },
        { ...capsule, id: "capsule-4", name: "Asking", harness: "claude" },
        { ...capsule, id: "capsule-5", name: "Done", harness: "codex" },
        { ...capsule, id: "capsule-6", name: "Crashed", harness: "pi" },
      ],
    });
    const run = (capsuleId: string, id: string, state: Run["state"]): Run => ({
      id,
      capsuleId,
      harness: "claude",
      state,
      createdAt: "2026-08-23T02:00:00Z",
      startedAt: "2026-08-23T02:00:01Z",
      updatedAt: "2026-08-23T02:00:01Z",
      finishedAt: state === "Running" ? undefined : "2026-08-23T02:30:00Z",
      failure: state === "Failed" ? "harness exited with status 1" : undefined,
      resourceVersion: 2,
    });
    vi.spyOn(api, "runs").mockImplementation(async (capsuleId) => ({
      items:
        capsuleId === capsule.id
          ? [run(capsuleId, "run-new", "Running")]
          : capsuleId === "capsule-5"
            ? [run(capsuleId, "run-done", "Succeeded")]
            : capsuleId === "capsule-6"
              ? [run(capsuleId, "run-crashed", "Failed")]
              : [],
    }));
    vi.spyOn(api, "threads").mockImplementation(async (capsuleId) => ({
      items:
        capsuleId === "capsule-4"
          ? [
              {
                id: "thread-waiting",
                capsuleId,
                state: "active",
                harness: "claude",
                encryptedAtRest: true,
                messageCount: 2,
                encryptedBytes: 128,
                awaiting: { kind: "permission", since: "2026-08-23T03:00:00Z" },
                createdAt: "2026-08-23T02:00:00Z",
                updatedAt: "2026-08-23T03:00:00Z",
                resourceVersion: 2,
              },
            ]
          : [],
    }));
    const moments = vi.spyOn(api, "moments");
    const gitStatus = vi.spyOn(api, "gitStatus");
    const gitDiff = vi.spyOn(api, "gitDiff");
    const files = vi.spyOn(api, "workspaceFiles");
    const blocks = vi.spyOn(api, "threadBlocks");
    wrapper(<CapsuleList />);

    expect(await screen.findByRole("heading", { name: "3 need attention" })).toBeInTheDocument();
    const attention = screen.getByRole("region", { name: /Needs attention/ });
    expect(attention).toHaveTextContent("Broken");
    expect(attention).toHaveTextContent("Capsule failed");
    expect(attention).toHaveTextContent("clone failed");
    expect(attention).toHaveTextContent("harness exited with status 1");
    const [broken, crashed] = ["Broken", "Crashed"].map((name) =>
      screen.getByText(name).closest("li"),
    );
    expect(
      within(broken as HTMLElement).getByRole("link", { name: "Open Capsule" }),
    ).toHaveAttribute("href", "/ui/capsules/capsule-2");
    expect(
      within(crashed as HTMLElement).getByRole("link", { name: "Open Capsule" }),
    ).toHaveAttribute("href", "/ui/capsules/capsule-6?session=terminal");
    expect(
      await within(attention).findByRole("link", { name: "Review permission" }),
    ).toHaveAttribute("href", "/ui/capsules/capsule-4?session=thread-waiting");

    const review = screen.getByRole("region", { name: /Ready for review/ });
    expect(review).toHaveTextContent("Done");
    expect(review).toHaveTextContent("Codex finished");
    expect(within(review).getByRole("link", { name: "Open diff" })).toHaveAttribute(
      "href",
      "/ui/capsules/capsule-5/diff",
    );

    expect(screen.getByRole("region", { name: /Working/ })).toHaveTextContent(
      "Claude Code working",
    );
    const quiet = screen.getByText("1 paused").closest("details");
    expect(quiet).not.toHaveAttribute("open");
    expect(quiet).toHaveTextContent("Parked");
    expect(screen.queryByText("Desired state")).not.toBeInTheDocument();
    expect(screen.queryByText(capsule.id)).not.toBeInTheDocument();
    expect(moments).not.toHaveBeenCalled();
    expect(gitStatus).not.toHaveBeenCalled();
    expect(gitDiff).not.toHaveBeenCalled();
    expect(files).not.toHaveBeenCalled();
    expect(blocks).not.toHaveBeenCalled();
  });

  it("offers the diff only when the provider supports Git review", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "fake/v1",
      attach: false,
      run: false,
      git: false,
      pause: false,
      snapshot: false,
      clone: false,
      preview: false,
      browse: false,
      delivery: false,
      resourceMetrics: false,
    });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [capsule] });
    vi.spyOn(api, "runs").mockResolvedValue({
      items: [
        {
          id: "run-done",
          capsuleId: capsule.id,
          harness: "opencode",
          state: "Succeeded",
          createdAt: "2026-08-23T02:00:00Z",
          updatedAt: "2026-08-23T02:30:00Z",
          resourceVersion: 2,
        },
      ],
    });
    wrapper(<CapsuleList />);

    expect(await screen.findByRole("heading", { name: "1 ready for review" })).toBeInTheDocument();
    const review = screen.getByRole("region", { name: /Ready for review/ });
    expect(within(review).getByRole("link", { name: "Open Capsule" })).toHaveAttribute(
      "href",
      "/ui/capsules/capsule-1",
    );
    expect(screen.queryByRole("link", { name: "Open diff" })).not.toBeInTheDocument();
  });

  it("says when everything is caught up and still offers new work", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({
      items: [capsule, { ...capsule, id: "capsule-2", name: "Parked", state: "Sealed" }],
    });
    vi.spyOn(api, "runs").mockImplementation(async (capsuleId) => ({
      items:
        capsuleId === capsule.id
          ? [
              {
                id: "run-1",
                capsuleId,
                harness: "opencode",
                state: "Running",
                createdAt: "2026-08-23T02:00:00Z",
                startedAt: "2026-08-23T02:00:01Z",
                updatedAt: "2026-08-23T02:00:01Z",
                resourceVersion: 1,
              },
            ]
          : [],
    }));
    const openLauncher = vi.fn();
    const openNewProject = vi.fn();
    wrapper(
      <Routes>
        <Route
          path="/ui"
          element={<Outlet context={{ openLauncher, openNewProject }} />}
        >
          <Route index element={<CapsuleList />} />
        </Route>
      </Routes>,
    );

    expect(await screen.findByRole("heading", { name: "All caught up" })).toBeInTheDocument();
    expect(await screen.findByText(/1 agent is still working/)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: /Needs attention/ })).not.toBeInTheDocument();
    expect(screen.getByText("1 sealed")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Start work in Project" }));
    expect(openLauncher).toHaveBeenCalledWith(project);
    await userEvent.click(screen.getByRole("button", { name: "New project" }));
    expect(openNewProject).toHaveBeenCalled();
  });

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
      providerVersion: "fake/v1",
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
    vi.spyOn(api, "harnessProfiles").mockResolvedValue({ items: [] });
    const moments = vi.spyOn(api, "moments").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "fake/v1",
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
    expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Capsule actions" }));
    expect(screen.getByRole("menuitem", { name: "Pause" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: "Delete…" }));
    expect(
      screen.getByRole("alertdialog", { name: `Delete ${capsule.name}?` }),
    ).toBeInTheDocument();
    expect(moments).not.toHaveBeenCalled();
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
      providerVersion: "fake/v1",
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
    const { container } = wrapper(<WorkspaceBrowser capsuleId={capsule.id} />);
    await userEvent.click(await screen.findByRole("button", { name: "review.txt" }));
    expect(await screen.findByLabelText("Contents of review.txt")).toHaveTextContent(
      "<script>unsafe()</script>",
    );
    expect(container.querySelector("script")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "image.bin" }));
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
