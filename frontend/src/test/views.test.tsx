import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  CapsuleDetail,
  CapsuleList,
  DiffReview,
  Lineage,
  PreviewCard,
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
