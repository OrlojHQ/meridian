import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Capsule, Project, Routine, RoutineFire, Thread } from "../api/generated/types.gen";
import { ProjectRoutines, Routines } from "../components/Routines";
import {
  buildCadence,
  describeCadence,
  fireKeptCapsule,
  fireSummary,
  parseCadence,
} from "../components/routineText";
import { describeCapsule } from "../shell/capsuleActivity";

const fire = (overrides: Partial<RoutineFire>): RoutineFire => ({
  id: "fire-1",
  routineId: "routine-1",
  trigger: "clock",
  scheduledFor: "2026-11-23T12:00:00Z",
  state: "finished",
  capsuleDeleted: false,
  createdAt: "2026-11-23T12:00:00Z",
  ...overrides,
});

describe("routine cadence text", () => {
  it("round-trips the shapes the form writes", () => {
    for (const expression of ["15 * * * *", "0 6 * * *", "30 9 * * 1-5", "0 6 * * 1"]) {
      expect(buildCadence(parseCadence(expression))).toBe(expression);
    }
    expect(parseCadence("0 6 * * 7").weekday).toBe(0);
  });

  it("keeps anything else as custom", () => {
    expect(parseCadence("0 6 1 * *").mode).toBe("custom");
    expect(parseCadence("*/15 * * * *").mode).toBe("custom");
    expect(describeCadence("0 6 1 * *")).toBe("Cron 0 6 1 * *");
  });

  it("describes common cadences in words", () => {
    expect(describeCadence("0 * * * *")).toBe("Every hour");
    expect(describeCadence("15 * * * *")).toBe("Every hour at :15");
    expect(describeCadence("0 6 * * *")).toBe("Every day at 06:00");
    expect(describeCadence("30 9 * * 1-5")).toBe("Weekdays at 09:30");
    expect(describeCadence("0 6 * * 1")).toBe("Mondays at 06:00");
    expect(describeCadence("0 */4 * * *")).toBe("Every 4 hours");
    expect(describeCadence("15 */2 * * *")).toBe("Every 2 hours at :15");
  });
});

describe("routine fire text", () => {
  it("names outcomes and reasons without prompt text", () => {
    expect(fireSummary(fire({ outcome: "no_changes", capsuleDeleted: true })).label).toBe("No changes. Capsule deleted");
    expect(fireSummary(fire({ outcome: "changed" })).label).toBe("Changes to review");
    expect(fireSummary(fire({ outcome: "failed", reason: "model_unavailable" })).label).toBe(
      "Failed: the model is not offered",
    );
    expect(fireSummary(fire({ state: "skipped", reason: "backlog" })).label).toBe("Skipped: runs are waiting for you");
    expect(fireSummary(fire({ state: "running", outcome: "waiting" })).label).toBe("Waiting on you");
  });

  it("offers a Capsule only when one was kept", () => {
    expect(fireKeptCapsule(fire({ capsuleId: "c", outcome: "changed" }))).toBe(true);
    expect(fireKeptCapsule(fire({ capsuleId: "c", outcome: "no_changes", capsuleDeleted: true }))).toBe(false);
    expect(fireKeptCapsule(fire({ capsuleId: "c", state: "cleaning" }))).toBe(false);
  });
});

describe("routine Capsule activity", () => {
  const capsule: Capsule = {
    id: "capsule-sched",
    projectId: "project-1",
    timelineId: "timeline-1",
    name: "deps-weekly 2026-11-23 06:00",
    harness: "claude",
    state: "Paused",
    desiredState: "Paused",
    restoreComplete: true,
    createdAt: "2026-11-23T12:00:00Z",
    updatedAt: "2026-11-23T12:30:00Z",
    resourceVersion: 4,
    routine: { routineId: "s", routineName: "deps-weekly", fireId: "f", state: "finished", outcome: "changed" },
  };

  it("asks for the operator when a run kept changes, even after idle pause", () => {
    const activity = describeCapsule(capsule);
    expect(activity.group).toBe("attention");
    expect(activity.label).toBe("Routine changed files");
  });

  it("defers to a live session", () => {
    const working = { id: "t", state: "active", turn: "working" } as unknown as Thread;
    expect(describeCapsule({ ...capsule, state: "Ready", desiredState: "Ready" }, [], [working]).label)
      .not.toBe("Routine changed files");
  });

  it("stays quiet while a run is still going", () => {
    const running = { ...capsule, routine: { ...capsule.routine!, state: "running" as const, outcome: undefined } };
    expect(describeCapsule(running).group).not.toBe("attention");
  });
});

const project: Project = {
  id: "project-1",
  name: "meridian",
  repositoryUrl: "https://github.com/OrlojHQ/meridian",
  harnessImages: [{ name: "claude", imageReference: "capsule-claude" }],
  state: "active",
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  resourceVersion: 1,
};

const other: Project = { ...project, id: "project-2", name: "docs-site" };

const routine: Routine = {
  id: "routine-1",
  projectId: project.id,
  name: "deps-weekly",
  harness: "claude",
  model: "opus",
  prompt: "Update dependencies",
  cadence: "0 6 * * 1",
  timeZone: "America/Chicago",
  maxRunSeconds: 7200,
  catchUpSeconds: 3600,
  maxKept: 5,
  enabled: true,
  includeNotes: false,
  nextFireAt: "2026-11-23T12:00:00Z",
  keptCapsules: 1,
  lastFire: fire({ capsuleId: "capsule-kept", outcome: "changed" }),
  resourceVersion: 3,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
};

function respond(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), {
    status, headers: { "content-type": "application/json" },
  }));
}

function renderRoutines(
  requests: { method: string; path: string; body?: unknown }[],
  page: "all" | "project" = "all",
  entry = "/ui/routines",
) {
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const request = input instanceof Request ? input : new Request(String(input), init);
    const url = new URL(request.url, window.location.origin);
    const method = request.method.toUpperCase();
    return request.clone().text().then((text) => {
      requests.push({ method, path: url.pathname, body: text ? JSON.parse(text) : undefined });
      if (url.pathname === "/capabilities") return respond({ structured: true, harnessImages: [] });
      if (url.pathname === "/projects") return respond({ items: [project, other] });
      if (url.pathname === "/routines" && method === "GET") {
        return respond({ items: [routine, { ...routine, id: "routine-3", projectId: other.id, name: "docs-links", cadence: "0 3 * * *", keptCapsules: 0, lastFire: undefined }] });
      }
      if (url.pathname.endsWith("/reported-models")) {
        return respond({ models: [{ id: "opus", label: "Opus" }], efforts: [] });
      }
      if (url.pathname === `/projects/${project.id}/routines` && method === "POST") {
        return respond({ ...routine, id: "routine-2", name: "nightly" }, 201);
      }
      if (url.pathname === `/routines/${routine.id}/run`) {
        return respond(fire({ id: "fire-2", state: "starting", trigger: "manual" }), 202);
      }
      return respond({ error: { code: "not_found", message: "missing" } }, 404);
    });
  }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        {page === "all" ? <Routines /> : <ProjectRoutines project={project} />}
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Routines page", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("lists a routine with its last run and kept Capsule", async () => {
    renderRoutines([]);
    expect(await screen.findByText("deps-weekly")).toBeInTheDocument();
    expect(screen.getByText("Mondays at 06:00 · America/Chicago")).toBeInTheDocument();
    expect(screen.getByText("Changes to review")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open Capsule" })).toHaveAttribute("href", "/ui/capsules/capsule-kept");
    expect(screen.getByText("1 of 5 kept")).toBeInTheDocument();
  });

  it("creates a routine with a model from the reported list", async () => {
    const requests: { method: string; path: string; body?: unknown }[] = [];
    renderRoutines(requests);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "New routine" }));
    await user.type(screen.getByPlaceholderText("deps-weekly"), "nightly");
    await waitFor(() => expect(screen.getByRole("option", { name: "Opus" })).toBeInTheDocument());
    await user.selectOptions(screen.getByLabelText("Model"), "opus");
    await user.type(screen.getByLabelText("Prompt"), "Triage flaky tests");
    await user.selectOptions(screen.getByLabelText("Repeat"), "daily");
    await user.click(screen.getByRole("button", { name: "Create routine" }));
    await waitFor(() => expect(requests.some((item) => item.method === "POST" && item.path.endsWith("/routines"))).toBe(true));
    const created = requests.find((item) => item.method === "POST" && item.path.endsWith("/routines"));
    expect(created?.body).toMatchObject({
      name: "nightly", harness: "claude", model: "opus", prompt: "Triage flaky tests", cadence: "0 6 * * *",
    });
  });

  it("opts a routine in to Project notes only when asked", async () => {
    const requests: { method: string; path: string; body?: unknown }[] = [];
    renderRoutines(requests);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "New routine" }));
    const include = screen.getByRole("switch", { name: "Include Project notes" });
    expect(include).not.toBeChecked();
    await user.type(screen.getByPlaceholderText("deps-weekly"), "nightly");
    await user.type(screen.getByLabelText("Prompt"), "Triage flaky tests");
    await user.click(include);
    await user.click(screen.getByRole("button", { name: "Create routine" }));
    await waitFor(() => expect(requests.some((item) => item.method === "POST" && item.path.endsWith("/routines"))).toBe(true));
    const created = requests.find((item) => item.method === "POST" && item.path.endsWith("/routines"));
    expect(created?.body).toMatchObject({ includeNotes: true });
  });

  it("groups routines by Project", async () => {
    renderRoutines([]);
    expect(await screen.findByRole("heading", { name: "meridian" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "docs-site" })).toBeInTheDocument();
    expect(screen.getByText("docs-links")).toBeInTheDocument();
  });

  it("filters to one Project from a Project link", async () => {
    renderRoutines([], "all", `/ui/routines?project=${other.id}`);
    expect(await screen.findByText("docs-links")).toBeInTheDocument();
    expect(screen.queryByText("deps-weekly")).not.toBeInTheDocument();
    expect(screen.getByText(/Showing routines for/)).toBeInTheDocument();
  });

  it("points a Project page at its routines", async () => {
    renderRoutines([], "project");
    expect(await screen.findByText("1 routine on this Project.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Manage routines" }))
      .toHaveAttribute("href", `/ui/routines?project=${project.id}`);
  });

  it("fires a routine now", async () => {
    const requests: { method: string; path: string }[] = [];
    renderRoutines(requests);
    const user = userEvent.setup();
    await user.click((await screen.findAllByRole("button", { name: "Run now" }))[0]);
    await waitFor(() => expect(requests.some((item) => item.path === `/routines/${routine.id}/run`)).toBe(true));
  });
});
