import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

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
import type { Capsule, TimelineView } from "../api/generated/types.gen";

const project = {
  id: "project-1",
  name: "Project",
  createdAt: "2026-08-23T00:00:00Z",
  updatedAt: "2026-08-23T00:00:00Z",
  resourceVersion: 1,
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
    expect(await screen.findByText(/No projects exist yet/)).toBeInTheDocument();
    loading.unmount();

    vi.spyOn(api, "projects").mockRejectedValue(
      new MeridianAPIError("API unavailable", "network_error"),
    );
    wrapper(<CapsuleList />);
    expect(await screen.findByRole("alert")).toHaveTextContent("API unavailable");
  });

  it("renders status and unavailable provider metrics", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [capsule] });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "moments").mockResolvedValue({ items: [] });
    wrapper(<CapsuleList />);
    expect(await screen.findByRole("heading", { name: "Workspace" })).toBeInTheDocument();
    expect(screen.getByText("Provider metrics").nextSibling).toHaveTextContent(
      "Unavailable",
    );
    expect(screen.getByText("Ready", { selector: ".badge" })).toBeInTheDocument();
  });

  it("starts a project Thread in a fresh Capsule", async () => {
    vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
    vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
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
    wrapper(<CapsuleList />);
    await userEvent.type(await screen.findByLabelText("Harness"), "mock");
    await userEvent.type(screen.getByLabelText("Capsule name (optional)"), "new-session");
    await userEvent.type(screen.getByLabelText("First prompt"), "inspect");
    await userEvent.click(screen.getByRole("button", { name: "Start Thread" }));
    await waitFor(() =>
      expect(spawn).toHaveBeenCalledWith(
        project.id,
        "mock",
        "inspect",
        "new-session",
      ),
    );
    expect(await screen.findByRole("status")).toHaveTextContent("thread-new");
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

  it("shows empty Run and Moment states", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "gitStatus").mockResolvedValue({ content: "", truncated: false });
    vi.spyOn(api, "moments").mockResolvedValue({ items: [] });
    vi.spyOn(api, "timeline").mockResolvedValue(timeline);
    vi.spyOn(api, "capabilities").mockResolvedValue({
      providerVersion: "fake/v1",
      attach: false,
      run: false,
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
    expect(await screen.findByText("No Runs yet.")).toBeInTheDocument();
    expect(screen.getByText("No filesystem Moments.")).toBeInTheDocument();
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
