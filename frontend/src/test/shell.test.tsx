import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { MeridianAPIError, api } from "../api/client";
import type {
  Capabilities,
  Capsule,
  Project,
  Thread,
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
  providerVersion: "docker/v1",
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
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  document.title = "";
});

async function openMeridianLauncher() {
  await userEvent.click(await screen.findByRole("button", { name: /Commands/ }));
  await userEvent.click(await screen.findByRole("option", { name: "New Capsule in Meridian" }));
}

describe("application shell", () => {
  it.each(["/ui/", "/ui/settings/harnesses"])("closes the launcher when navigating to provider settings from %s", async (initial) => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
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
    await openMeridianLauncher();
    expect(screen.getByRole("dialog", { name: "Start work in Meridian" })).toBeInTheDocument();
    await userEvent.click(await screen.findByRole("link", { name: "Connect provider" }));
    expect(await screen.findByRole("heading", { name: "Import from your computer" })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await openMeridianLauncher();
    expect(screen.getByRole("dialog", { name: "Start work in Meridian" })).toBeInTheDocument();
  });

  it("closes the launcher as soon as a task starts", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({ ...capabilities, structured: true });
    vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
    vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: false });
    vi.spyOn(api, "projectHarnessSetup").mockResolvedValue({ setup: "" });
    vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
    const spawn = vi.spyOn(api, "createProjectThread").mockResolvedValue({
      id: "intent-1",
      projectId: project.id,
      capsuleId: "capsule-new",
      capsuleName: "new-session",
      threadId: "thread-new",
      runId: "run-new",
      messageId: "message-new",
      harness: "opencode",
      state: "provisioning",
      createdAt: "2026-08-26T12:00:00Z",
      updatedAt: "2026-08-26T12:00:00Z",
      resourceVersion: 1,
    });
    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
          <Route path="capsules/:capsuleId" element={<p>Capsule workspace</p>} />
        </Route>
      </Routes>,
    );
    await openMeridianLauncher();
    expect(screen.getByRole("dialog", { name: "Start work in Meridian" })).toBeInTheDocument();
    await userEvent.click(await screen.findByRole("tab", { name: "Give it a task" }));
    await userEvent.type(screen.getByLabelText("Task"), "inspect");
    await userEvent.click(screen.getByRole("button", { name: "Start task" }));
    await waitFor(() =>
      expect(spawn).toHaveBeenCalledWith(project.id, "opencode", "inspect", undefined, undefined),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Start work in Meridian" }),
      ).not.toBeInTheDocument(),
    );
    expect(await screen.findByText("Capsule workspace")).toBeInTheDocument();
  });

  it("closes the launcher as soon as a Capsule is created", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
    vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
    vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: false });
    vi.spyOn(api, "projectHarnessSetup").mockResolvedValue({ setup: "" });
    vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
    const launched: Capsule = {
      ...capsule,
      id: "capsule-native",
      name: "native-work",
      harness: "opencode",
      state: "Creating",
    };
    const create = vi.spyOn(api, "createCapsule").mockResolvedValue(launched);
    vi.spyOn(api, "capsule").mockResolvedValue(launched);
    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
          <Route path="capsules/:capsuleId" element={<p>Capsule workspace</p>} />
        </Route>
      </Routes>,
    );
    await openMeridianLauncher();
    expect(screen.getByRole("dialog", { name: "Start work in Meridian" })).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Start OpenCode" })).toBeEnabled(),
    );
    await userEvent.click(screen.getByRole("button", { name: "Start OpenCode" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(project.id, expect.any(String), "opencode", undefined, ""),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Start work in Meridian" }),
      ).not.toBeInTheDocument(),
    );
    expect(await screen.findByText("Capsule workspace")).toBeInTheDocument();
  });

  it("keeps the launcher open when starting a task fails", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({ ...capabilities, structured: true });
    vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
    vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: false });
    vi.spyOn(api, "projectHarnessSetup").mockResolvedValue({ setup: "" });
    vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
    vi.spyOn(api, "createProjectThread").mockRejectedValue(
      new MeridianAPIError("Capsule quota exceeded", "resource_exhausted", 429),
    );
    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );
    await openMeridianLauncher();
    await userEvent.click(await screen.findByRole("tab", { name: "Give it a task" }));
    await userEvent.type(screen.getByLabelText("Task"), "inspect");
    await userEvent.click(screen.getByRole("button", { name: "Start task" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Capsule quota exceeded");
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
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "threads").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({ ...capabilities, structured: true });
    vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
    vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: false });
    vi.spyOn(api, "projectHarnessSetup").mockResolvedValue({ setup: "" });
    vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
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

    await openMeridianLauncher();
    expect(
      screen.getByRole("dialog", { name: "Start work in Meridian" }),
    ).toBeInTheDocument();
    expect(await screen.findByRole("radio", { name: "OpenCode" })).toBeChecked();
    expect(
      await screen.findByRole("tab", { name: "Open terminal" }),
    ).toHaveAttribute("aria-selected", "true");
    await userEvent.click(
      screen.getByRole("tab", { name: "Give it a task" }),
    );
    expect(screen.getByLabelText("Task")).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Start work in Meridian" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("shows what each Capsule is doing and filters by activity", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({
      items: [
        capsule,
        { ...capsule, id: "capsule-2", name: "Setup broke", state: "Failed" },
      ],
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
                createdAt: "2026-08-26T00:02:00Z",
                startedAt: "2026-08-26T00:02:01Z",
                updatedAt: "2026-08-26T00:02:01Z",
                resourceVersion: 1,
              },
            ]
          : [],
    }));
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
    const gitStatus = vi.spyOn(api, "gitStatus");

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );

    const working = await screen.findByRole("link", { name: /Review the launcher/ });
    await waitFor(() => expect(working).toHaveTextContent("OpenCode working"));
    expect(
      screen.getByRole("link", { name: /Setup broke/ }),
    ).toHaveTextContent("Capsule failed");
    expect(screen.getByLabelText("1 need attention")).toBeInTheDocument();

    await userEvent.type(screen.getByPlaceholderText("Filter work"), "failed");
    expect(screen.queryByRole("link", { name: /Review the launcher/ })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Setup broke/ })).toBeInTheDocument();
    expect(gitStatus).not.toHaveBeenCalled();
  });

  it("lists main Capsules with their subagent rollup and only subagents that need you", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    const lead: Capsule = { ...capsule, id: "capsule-lead", name: "Release lead", role: "main" };
    const leadThread: Thread = {
      id: "thread-lead",
      capsuleId: lead.id,
      state: "active",
      harness: "opencode",
      encryptedAtRest: true,
      messageCount: 1,
      encryptedBytes: 64,
      createdAt: "2026-08-26T00:00:00Z",
      updatedAt: "2026-08-26T00:00:00Z",
      resourceVersion: 1,
    };
    const blocked: Capsule = {
      ...capsule,
      id: "capsule-blocked",
      name: "Blocked subagent",
      role: "sub",
      leadThreadId: leadThread.id,
      linkId: "link-blocked",
    };
    const capsules = vi.spyOn(api, "capsules").mockResolvedValue({ items: [lead] });
    vi.spyOn(api, "board").mockResolvedValue({
      truncated: false,
      items: [
        {
          capsule: lead,
          status: "working",
          since: "2026-08-26T00:00:00Z",
          surfaced: false,
          childRollup: { total: 5, working: 1, idle: 0, done: 3, needsYou: 1, failed: 0, archived: 2 },
        },
        { capsule: blocked, status: "needs_you", needsYou: "permission", since: "2026-08-26T00:00:00Z", surfaced: true },
      ],
    });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    const threads = vi
      .spyOn(api, "threads")
      .mockImplementation(async (capsuleId) => ({ items: capsuleId === lead.id ? [leadThread] : [] }));
    vi.spyOn(api, "capabilities").mockResolvedValue({ ...capabilities, structured: true });

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );

    const leadRow = await screen.findByRole("link", { name: /Release lead/ });
    expect(leadRow).toHaveTextContent("Subagents: 3/5 done, 1 needs you, 1 working, 2 archived");
    expect(await screen.findByRole("link", { name: /Blocked subagent/ })).toBeInTheDocument();
    expect(capsules).toHaveBeenCalledWith(project.id, expect.anything(), "main");
    expect(threads.mock.calls.map(([id]) => id).sort()).toEqual([blocked.id, lead.id]);
  });

  it("places Inbox, Threads, and Harness settings at the top of the sidebar", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({
      items: [{ ...capsule, name: "Setup broke", state: "Failed" }],
    });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );

    const sidebar = screen.getByRole("complementary", { name: "Activity" });
    const views = within(sidebar).getByRole("navigation", { name: "Views" });
    const links = within(views).getAllByRole("link");
    expect(links.map((link) => link.textContent?.replace(/\d+$/, ""))).toEqual([
      "Harness settings",
    ]);
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "/ui/settings/harnesses",
    ]);
    links.forEach((link) => expect(link.querySelector("svg")).not.toBeNull());
    expect(await screen.findByLabelText("1 need attention")).toHaveTextContent("1");

    const filter = within(sidebar).getByPlaceholderText("Filter work");
    const work = within(sidebar).getByRole("navigation", { name: "Capsule activity" });
    expect(filter.compareDocumentPosition(views) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(views.compareDocumentPosition(work) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    const footer = sidebar.querySelector(".sidebar-footer")!;
    expect(within(footer as HTMLElement).queryAllByRole("link")).toHaveLength(0);
    expect(within(footer as HTMLElement).getByRole("button", { name: /Commands/ })).toBeInTheDocument();
    expect(sidebar).not.toHaveTextContent(/Connected|Connecting/);
  });

  it("shows connection, provider, and activity counts in the status bar", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({
      items: [
        capsule,
        { ...capsule, id: "capsule-2", name: "Setup broke", state: "Failed" },
      ],
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
                createdAt: "2026-08-26T00:02:00Z",
                startedAt: "2026-08-26T00:02:01Z",
                updatedAt: "2026-08-26T00:02:01Z",
                resourceVersion: 1,
              },
            ]
          : [],
    }));
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );

    const bar = screen.getByRole("contentinfo", { name: "Status bar" });
    await waitFor(() =>
      expect(within(bar).getByRole("status")).toHaveTextContent("Connected"),
    );
    expect(bar).toHaveTextContent("docker/v1");
    expect(bar).not.toHaveTextContent(/simulated/i);
    await waitFor(() =>
      expect(within(bar).getByRole("link", { name: "Needs attention: 1" })).toHaveAttribute(
        "href",
        "/ui/",
      ),
    );
    expect(within(bar).getByRole("link", { name: "Working: 1" })).toBeInTheDocument();
    expect(within(bar).getByRole("link", { name: "Ready for review: 0" })).toBeInTheDocument();
    expect(bar).not.toHaveTextContent(/cpu|memory/i);
    const sidebar = screen.getByRole("complementary", { name: "Activity" });
    expect(sidebar).not.toHaveTextContent("Connected");
  });

  it("reports an unreachable daemon without a simulated-provider warning", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockRejectedValue(
      new MeridianAPIError("Meridian API is unreachable", "network_error"),
    );

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );

    const bar = screen.getByRole("contentinfo", { name: "Status bar" });
    await waitFor(
      () => expect(within(bar).getByRole("status")).toHaveTextContent("Daemon unreachable"),
      { timeout: 5_000 },
    );
    expect(bar).not.toHaveTextContent("simulated");
  });

  it("marks a non-simulated provider plainly", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({
      ...capabilities,
      providerVersion: "docker/v2",
    });

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );

    const bar = screen.getByRole("contentinfo", { name: "Status bar" });
    expect(await within(bar).findByText("docker/v2")).toBeInTheDocument();
    expect(bar).not.toHaveTextContent(/simulated/i);
  });

  it("flags Capsules whose Thread is waiting for the operator", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [capsule] });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue({ ...capabilities, structured: true });
    const threads = vi.spyOn(api, "threads").mockResolvedValue({
      items: [
        {
          id: "thread-1",
          capsuleId: capsule.id,
          state: "active",
          harness: "opencode",
          encryptedAtRest: true,
          messageCount: 3,
          encryptedBytes: 256,
          awaiting: { kind: "permission", since: "2026-08-26T00:03:00Z" },
          createdAt: "2026-08-26T00:02:00Z",
          updatedAt: "2026-08-26T00:03:00Z",
          resourceVersion: 2,
        },
      ],
    });
    const blocks = vi.spyOn(api, "threadBlocks");

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );

    await waitFor(() =>
      expect(screen.getByRole("link", { name: /Review the launcher/ })).toHaveTextContent(
        "Waiting for permission",
      ),
    );
    expect(screen.getByLabelText("1 need attention")).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("(1) Meridian"));
    expect(document.title).not.toContain("Review the launcher");
    expect(threads).toHaveBeenCalledWith(capsule.id, expect.anything());
    expect(blocks).not.toHaveBeenCalled();
  });

  it("offers content-free browser notifications from the status bar", async () => {
    const posted: { title: string; options?: NotificationOptions }[] = [];
    class FakeNotification {
      static permission: NotificationPermission = "default";
      static requestPermission = vi.fn(async () => {
        FakeNotification.permission = "granted";
        return "granted" as NotificationPermission;
      });
      onclick: (() => void) | null = null;
      constructor(title: string, options?: NotificationOptions) {
        posted.push({ title, options });
      }
      close() {}
    }
    vi.stubGlobal("Notification", FakeNotification);
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [capsule] });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );

    const enable = await screen.findByRole("button", { name: "Enable browser notifications" });
    expect(enable).toHaveAttribute(
      "title",
      expect.stringContaining("Meridian needs you"),
    );
    await userEvent.click(enable);
    expect(FakeNotification.requestPermission).toHaveBeenCalledTimes(1);
    expect(await screen.findByText("Alerts on")).toBeInTheDocument();
    expect(setItem).not.toHaveBeenCalled();
    expect(posted).toHaveLength(0);
  });

  it("nests the selected Capsule's sessions beneath it", async () => {
    const thread = {
      capsuleId: capsule.id,
      state: "active" as const,
      harness: "claude",
      encryptedAtRest: true as const,
      messageCount: 3,
      encryptedBytes: 256,
      createdAt: "2026-08-26T00:02:00Z",
      updatedAt: "2026-08-26T00:03:00Z",
      resourceVersion: 2,
    };
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({
      items: [capsule, { ...capsule, id: "capsule-2", name: "Other work" }],
    });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "capabilities").mockResolvedValue({ ...capabilities, structured: true });
    vi.spyOn(api, "threads").mockImplementation(async (capsuleId) => ({
      items: [
        { ...thread, id: `${capsuleId}-a`, capsuleId },
        {
          ...thread,
          id: `${capsuleId}-b`,
          capsuleId,
          awaiting: { kind: "permission", since: "2026-08-26T00:04:00Z" },
        },
      ],
    }));

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route path="capsules/:capsuleId" element={<p>Capsule page</p>} />
        </Route>
      </Routes>,
      `/ui/capsules/${capsule.id}`,
    );

    const sessions = await screen.findByRole("list", { name: "Sessions" });
    await waitFor(() => expect(sessions.querySelectorAll("li")).toHaveLength(3));
    expect(sessions).toHaveTextContent("Waiting for permission");
    const waiting = screen.getByRole("link", { name: /Claude Code\s*Waiting for permission/ });
    expect(waiting).toHaveAttribute("aria-current", "page");
    expect(waiting).toHaveAttribute(
      "href",
      `/ui/capsules/${capsule.id}?session=${capsule.id}-b`,
    );
    expect(screen.getByLabelText("3 sessions")).toBeInTheDocument();
  });

  it.each([
    { layout: "phone", phone: true },
    { layout: "desktop", phone: false },
  ])("opens a Thread on its conversation with tools matching the $layout layout", async ({ phone }) => {
    const queries: string[] = [];
    vi.spyOn(window, "matchMedia").mockImplementation((query) => {
      queries.push(query);
      return {
        matches: phone && query === "(max-width: 720px)",
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      } as unknown as MediaQueryList;
    });
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [capsule] });
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "threads").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
    vi.spyOn(api, "thread").mockResolvedValue({
      id: "thread-1",
      capsuleId: capsule.id,
      state: "active",
      harness: "opencode",
      encryptedAtRest: true,
      messageCount: 1,
      encryptedBytes: 64,
      createdAt: "2026-08-26T00:02:00Z",
      updatedAt: "2026-08-26T00:03:00Z",
      resourceVersion: 1,
    });

    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route path="threads/:threadId" element={<p>Conversation</p>} />
        </Route>
      </Routes>,
      "/ui/threads/thread-1",
    );

    expect(screen.getByText("Conversation")).toBeInTheDocument();
    expect(queries).toContain("(max-width: 720px)");
    if (phone) {
      const toggle = await screen.findByRole("button", { name: "Show Capsule tools" });
      expect(screen.queryByRole("complementary", { name: "Capsule tools" })).not.toBeInTheDocument();
      await userEvent.click(toggle);
      expect(await screen.findByRole("complementary", { name: "Capsule tools" })).toBeInTheDocument();
    } else {
      expect(await screen.findByRole("button", { name: "Hide Capsule tools" })).toBeInTheDocument();
      expect(await screen.findByRole("complementary", { name: "Capsule tools" })).toBeInTheDocument();
    }
  });

  it("lists Moments in the Activity tool", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "moments").mockResolvedValue({
      items: [
        {
          id: "moment-1",
          projectId: project.id,
          capsuleId: capsule.id,
          timelineId: capsule.timelineId,
          name: "before refactor",
          archiveSha256: "a",
          archiveSize: 1,
          manifestSha256: "b",
          imageDigest: "c",
          projectSetupHash: "d",
          createdAt: "2026-08-26T00:05:00Z",
          final: false,
        },
      ],
    });

    wrapper(
      <WorkspaceContext
        capsuleId={capsule.id}
        pathname={`/ui/capsules/${capsule.id}`}
        capabilities={{ ...capabilities, snapshot: true }}
      />,
    );

    await userEvent.click(await screen.findByRole("tab", { name: "Activity" }));
    expect(await screen.findByRole("link", { name: /before refactor/ })).toHaveAttribute(
      "href",
      "/ui/moments/moment-1",
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

  it("offers import, a provider, and a new Project when nothing exists yet", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
    vi.spyOn(api, "board").mockResolvedValue({ items: [], truncated: false });
    const user = userEvent.setup();
    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
          <Route path="settings/harnesses" element={<h1>Harness settings page</h1>} />
        </Route>
      </Routes>,
    );

    const start = await screen.findByRole("navigation", { name: "Get started" });
    expect(start).not.toHaveTextContent("No Projects");
    expect(within(start).getByRole("link", { name: "Import setup" })).toHaveAttribute(
      "href",
      "/ui/settings/harnesses#import-setup",
    );
    expect(within(start).getByRole("link", { name: "Connect a provider" })).toHaveAttribute(
      "href",
      "/ui/settings/harnesses#provider-connections",
    );
    await user.click(within(start).getByRole("button", { name: "Create a Project" }));
    expect(screen.getByRole("dialog", { name: "Create a Project" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await user.click(within(start).getByRole("link", { name: "Import setup" }));
    expect(await screen.findByRole("heading", { name: "Harness settings page" })).toBeInTheDocument();
  });

  it("keeps the first-run links hidden once a Project exists", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
    vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
    vi.spyOn(api, "board").mockResolvedValue({ items: [], truncated: false });
    wrapper(
      <Routes>
        <Route path="/ui" element={<AppShell />}>
          <Route index element={<p>Activity home</p>} />
        </Route>
      </Routes>,
    );

    expect(await screen.findByRole("button", { name: "New Capsule" })).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Get started" })).not.toBeInTheDocument();
    expect(screen.queryByText("No Projects")).not.toBeInTheDocument();
  });
});
