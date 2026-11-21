import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Capsule, Project, Routine, RoutineFire, Thread } from "../api/generated/types.gen";
import { ProjectRoutines, Routines } from "../components/Routines";
import {
  browserTimeZone,
  buildCadence,
  describeCadence,
  filterTimeZones,
  fireKeptCapsule,
  fireSummary,
  nextCadenceFire,
  nextRunHint,
  parseCadence,
  timeZoneChoices,
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

describe("time zones", () => {
  const now = new Date("2026-11-20T18:00:00Z");

  it("lists the browser zone first, with its offset and local time", () => {
    const choices = timeZoneChoices(now);
    expect(choices[0]?.id).toBe(browserTimeZone());
    expect(choices[0]?.local).toBe(true);
    expect(choices.filter((choice) => choice.local)).toHaveLength(1);
    expect(choices.some((choice) => choice.id === "UTC")).toBe(true);
    const chicago = choices.find((choice) => choice.id === "America/Chicago");
    expect(chicago?.offset).toBe("UTC\u221206:00");
    expect(chicago?.time).not.toBe("");
  });

  it("filters by city, region, and zone name", () => {
    const choices = timeZoneChoices(now);
    const ids = (query: string) => filterTimeZones(choices, query).map((choice) => choice.id);
    expect(ids("chicago")).toContain("America/Chicago");
    expect(ids("chicago")).not.toContain("Asia/Tokyo");
    expect(ids("central")).toContain("America/Chicago");
    expect(ids("tokyo")).toEqual(expect.arrayContaining(["Asia/Tokyo"]));
    expect(ids("tokyo")).not.toContain("America/Chicago");
    expect(ids("new york")).toContain("America/New_York");
  });

  it("keeps a saved zone that the runtime does not list", () => {
    const choices = timeZoneChoices(now, "US/Central");
    const saved = choices.find((choice) => choice.id === "US/Central");
    expect(saved?.savedOnly).toBe(true);
    expect(saved?.offset).toBe("UTC\u221206:00");
    expect(choices.filter((choice) => choice.id === "US/Central")).toHaveLength(1);
  });

  it("falls back to a built-in list when zones cannot be listed", () => {
    vi.spyOn(Intl, "supportedValuesOf").mockImplementation(() => {
      throw new Error("unavailable");
    });
    const choices = timeZoneChoices(now);
    expect(choices.map((choice) => choice.id)).toEqual(
      expect.arrayContaining(["UTC", "America/Chicago", "Asia/Tokyo"]),
    );
    expect(choices.length).toBeLessThan(40);
  });

  it("finds the next fire in the routine's zone", () => {
    // Wednesday 18 November 2026, 10:00 in Chicago.
    const from = new Date("2026-11-18T16:00:00Z");
    expect(nextCadenceFire("0 6 * * 1", "America/Chicago", from)?.toISOString()).toBe("2026-11-23T12:00:00.000Z");
    expect(nextCadenceFire("*/30 9-10 * * *", "UTC", new Date("2026-01-01T09:10:00Z"))?.toISOString())
      .toBe("2026-01-01T09:30:00.000Z");
    expect(nextCadenceFire("0 0 29 2 *", "UTC", new Date("2026-03-01T00:00:00Z"))?.toISOString())
      .toBe("2028-02-29T00:00:00.000Z");
    // 02:30 does not exist in New York on 8 March 2026.
    const spring = nextCadenceFire("30 2 * * *", "America/New_York", new Date("2026-03-07T17:00:00Z"));
    expect(spring?.toISOString()).toBe("2026-03-09T06:30:00.000Z");
  });

  it("names the viewer's local time only when the zones differ", () => {
    const from = new Date("2026-11-18T16:00:00Z");
    const local = browserTimeZone();
    expect(nextRunHint("0 6 * * 1", local, from)?.here).toBeUndefined();
    const other = local === "Asia/Tokyo" ? "America/Chicago" : "Asia/Tokyo";
    const hint = nextRunHint("0 6 * * 1", other, from);
    expect(hint?.there).toBeTruthy();
    expect(hint?.here).toBeTruthy();
    expect(hint?.here).not.toBe(hint?.there);
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

  it("defaults a new routine to the browser time zone", async () => {
    const original = Intl.DateTimeFormat.prototype.resolvedOptions;
    vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockImplementation(function mockZone(this: Intl.DateTimeFormat) {
      return { ...original.call(this), timeZone: "Pacific/Auckland" };
    });
    const requests: { method: string; path: string; body?: unknown }[] = [];
    renderRoutines(requests);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "New routine" }));
    expect(screen.getByRole("combobox", { name: "Time zone" })).toHaveValue("Pacific/Auckland");
    await user.type(screen.getByPlaceholderText("deps-weekly"), "nightly");
    await user.type(screen.getByLabelText("Prompt"), "Triage flaky tests");
    await user.click(screen.getByRole("button", { name: "Create routine" }));
    await waitFor(() => expect(requests.some((item) => item.method === "POST" && item.path.endsWith("/routines"))).toBe(true));
    const created = requests.find((item) => item.method === "POST" && item.path.endsWith("/routines"));
    expect(created?.body).toMatchObject({ timeZone: "Pacific/Auckland" });
  });

  it("keeps a saved time zone, including one missing from the list", async () => {
    renderRoutines([]);
    const user = userEvent.setup();
    await user.click((await screen.findAllByRole("button", { name: "Edit" }))[0]);
    expect(screen.getByRole("combobox", { name: "Time zone" })).toHaveValue("America/Chicago");

    cleanup();
    const original = Intl.supportedValuesOf.bind(Intl);
    vi.spyOn(Intl, "supportedValuesOf").mockImplementation((key) => {
      if (key === "timeZone") return ["Europe/London", "Asia/Tokyo"];
      return original(key);
    });
    renderRoutines([]);
    await user.click((await screen.findAllByRole("button", { name: "Edit" }))[0]);
    const zone = screen.getByRole("combobox", { name: "Time zone" });
    expect(zone).toHaveValue("America/Chicago");
    await user.click(zone);
    expect(screen.getByRole("option", { name: /America\/Chicago/ })).toBeInTheDocument();
  });

  it("filters time zones and selects one from the keyboard", async () => {
    renderRoutines([]);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "New routine" }));
    const zone = screen.getByRole("combobox", { name: "Time zone" });
    const local = browserTimeZone();
    expect(zone).toHaveValue(local);
    const hint = screen.getByText(/Runs must be at least 15 minutes apart/);
    expect(hint).toHaveTextContent("Next:");
    expect(hint).not.toHaveTextContent("your time");

    await user.click(zone);
    expect(zone).toHaveAttribute("aria-expanded", "true");
    await user.type(zone, "chicago");
    expect(screen.getByRole("option", { name: /America\/Chicago/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Asia\/Tokyo/ })).not.toBeInTheDocument();

    await user.clear(zone);
    await user.type(zone, "central");
    expect(screen.getByRole("option", { name: /America\/Chicago/ })).toBeInTheDocument();

    await user.clear(zone);
    await user.type(zone, "chi");
    const options = within(screen.getByRole("listbox", { name: "Time zones" })).getAllByRole("option");
    expect(options.length).toBeGreaterThan(1);
    await user.keyboard("{ArrowDown}");
    expect(zone).toHaveAttribute("aria-activedescendant", options[1].id);
    await user.keyboard("{Enter}");
    const picked = options[1].getAttribute("data-zone") ?? "";
    expect(picked).not.toBe("");
    expect(zone).toHaveValue(picked);
    expect(zone).toHaveAttribute("aria-expanded", "false");

    await user.type(zone, "not-a-zone");
    await user.keyboard("{Enter}");
    expect(zone).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(/No time zones match/)).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(zone).toHaveValue(picked);
    expect(zone).toHaveAttribute("aria-expanded", "false");

    const otherQuery = local === "Asia/Tokyo" ? "chicago" : "tokyo";
    const otherZone = local === "Asia/Tokyo" ? "America/Chicago" : "Asia/Tokyo";
    await user.click(zone);
    await user.type(zone, otherQuery);
    await user.keyboard("{Enter}");
    expect(zone).toHaveValue(otherZone);
    expect(hint).toHaveTextContent("your time");
    expect(hint).toHaveTextContent(otherZone);
  });

  it("fires a routine now", async () => {
    const requests: { method: string; path: string }[] = [];
    renderRoutines(requests);
    const user = userEvent.setup();
    await user.click((await screen.findAllByRole("button", { name: "Run now" }))[0]);
    await waitFor(() => expect(requests.some((item) => item.path === `/routines/${routine.id}/run`)).toBe(true));
  });
});
