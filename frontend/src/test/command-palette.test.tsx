import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type {
  Capabilities,
  Capsule,
  Project,
  Thread,
} from "../api/generated/types.gen";
import { App } from "../App";
import { AppShell } from "../shell/AppShell";
import { fuzzyScore, type PaletteCommand } from "../shell/paletteCommands";

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

const other: Capsule = {
  ...capsule,
  id: "capsule-2",
  timelineId: "timeline-2",
  name: "Fix flaky tests",
  harness: "claude",
};

const thread: Thread = {
  id: "thread-1",
  capsuleId: capsule.id,
  state: "active",
  harness: "claude",
  encryptedAtRest: true,
  messageCount: 2,
  encryptedBytes: 64,
  createdAt: "2026-08-26T00:02:00Z",
  updatedAt: "2026-08-26T00:03:00Z",
  resourceVersion: 1,
};

const capabilities: Capabilities = {
  providerVersion: "fake/v1",
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

function mockAPI(overrides: Partial<Capabilities> = {}, threads: Thread[] = []) {
  vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
  vi.spyOn(api, "capsules").mockResolvedValue({ items: [capsule, other] });
  vi.spyOn(api, "capsule").mockImplementation(async (id: string) =>
    id === other.id ? other : capsule,
  );
  vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
  vi.spyOn(api, "threads").mockImplementation(async (id: string) => ({
    items: threads.filter((item) => item.capsuleId === id),
  }));
  vi.spyOn(api, "capabilities").mockResolvedValue({ ...capabilities, ...overrides });
  vi.spyOn(api, "moments").mockResolvedValue({ items: [] });
  vi.spyOn(api, "harnessProfiles").mockResolvedValue({ items: [] });
  vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: false });
  vi.spyOn(api, "projectHarnessSetup").mockResolvedValue({ setup: "" });
  vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
}

function Where() {
  const location = useLocation();
  return <p data-testid="location">{`${location.pathname}${location.search}`}</p>;
}

function renderShell(initial = "/ui/") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[initial]}>
        <Routes>
          <Route path="/ui" element={<AppShell />}>
            <Route index element={<p>Activity home</p>} />
            <Route path="threads" element={<h1>Thread fleet</h1>} />
            <Route
              path="capsules/:capsuleId"
              element={
                <div className="terminal-host">
                  <textarea aria-label="Terminal input" />
                </div>
              }
            />
            <Route path="*" element={<p>Elsewhere</p>} />
          </Route>
        </Routes>
        <Where />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function renderApp(initial: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[initial]}>
        <App />
        <Where />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const onPlatform = (platform: string) =>
  vi.spyOn(navigator, "platform", "get").mockReturnValue(platform);

const openPalette = async () => {
  await userEvent.keyboard("{Control>}k{/Control}");
  return screen.findByRole("dialog", { name: "Command palette" });
};

const options = () => screen.getAllByRole("option").map((option) => option.textContent ?? "");

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("command palette", () => {
  it("opens with Ctrl+K, closes with Escape, and returns focus", async () => {
    onPlatform("Linux x86_64");
    mockAPI();
    renderShell();
    const trigger = await screen.findByRole("button", { name: /New project/ });
    trigger.focus();

    await openPalette();
    const input = screen.getByRole("combobox", { name: "Search commands" });
    expect(input).toHaveFocus();
    expect(input).toHaveAttribute("aria-expanded", "true");
    expect(input).toHaveAttribute("aria-controls", screen.getByRole("listbox").id);

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "Command palette" })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();

    await openPalette();
    await userEvent.keyboard("{Control>}k{/Control}");
    expect(screen.queryByRole("dialog", { name: "Command palette" })).not.toBeInTheDocument();
  });

  it("leaves Ctrl+K to a focused terminal but takes ⌘K on macOS", async () => {
    onPlatform("Linux x86_64");
    mockAPI();
    const view = renderShell("/ui/capsules/capsule-1");
    const terminal = screen.getByRole("textbox", { name: "Terminal input" });
    terminal.focus();
    await userEvent.keyboard("{Control>}k{/Control}");
    expect(screen.queryByRole("dialog", { name: "Command palette" })).not.toBeInTheDocument();
    expect(terminal).toHaveFocus();
    // A single-key shortcut is ignored in the terminal too.
    await userEvent.keyboard("?");
    expect(screen.queryByRole("dialog", { name: "Keyboard shortcuts" })).not.toBeInTheDocument();
    view.unmount();

    onPlatform("MacIntel");
    renderShell("/ui/capsules/capsule-1");
    screen.getByRole("textbox", { name: "Terminal input" }).focus();
    await userEvent.keyboard("{Control>}k{/Control}");
    expect(screen.queryByRole("dialog", { name: "Command palette" })).not.toBeInTheDocument();
    await userEvent.keyboard("{Meta>}k{/Meta}");
    expect(screen.getByRole("dialog", { name: "Command palette" })).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(screen.getByRole("textbox", { name: "Terminal input" })).toHaveFocus();
  });

  it("filters with fuzzy matching and groups results", async () => {
    onPlatform("Linux x86_64");
    mockAPI();
    renderShell();
    await screen.findByRole("link", { name: /Review the launcher/ });
    await openPalette();

    const groups = screen.getAllByRole("group").map((group) => group.textContent ?? "");
    expect(groups[0]).toMatch(/^Capsules/);
    expect(screen.getByRole("group", { name: "Create" })).toHaveTextContent("New Capsule in Meridian");
    expect(screen.getByRole("group", { name: "Go to" })).toHaveTextContent("Harness settings");

    await userEvent.type(screen.getByRole("combobox"), "rvw lnch");
    expect(options()).toHaveLength(1);
    expect(options()[0]).toContain("Review the launcher");
    expect(options()[0]).toContain("Meridian · No agent running");

    await userEvent.clear(screen.getByRole("combobox"));
    await userEvent.type(screen.getByRole("combobox"), "claude");
    expect(options()[0]).toContain("Fix flaky tests");

    await userEvent.clear(screen.getByRole("combobox"));
    await userEvent.type(screen.getByRole("combobox"), "zzzz");
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(
      within(screen.getByRole("dialog", { name: "Command palette" })).getByRole("status"),
    ).toHaveTextContent("No matching commands");
  });

  it("moves the active option with the arrow keys, runs it with Enter, and remembers it", async () => {
    onPlatform("Linux x86_64");
    mockAPI();
    renderShell();
    await screen.findByRole("link", { name: /Fix flaky tests/ });
    await openPalette();
    const input = screen.getByRole("combobox");
    const all = screen.getAllByRole("option");
    expect(input).toHaveAttribute("aria-activedescendant", all[0].id);
    expect(all[0]).toHaveAttribute("aria-selected", "true");

    await userEvent.keyboard("{ArrowDown}");
    expect(input).toHaveAttribute("aria-activedescendant", all[1].id);
    expect(all[1]).toHaveTextContent("Fix flaky tests");
    await userEvent.keyboard("{ArrowUp}{ArrowUp}");
    expect(input).toHaveAttribute("aria-activedescendant", all[all.length - 1].id);
    await userEvent.keyboard("{ArrowDown}{ArrowDown}{Enter}");

    expect(screen.getByTestId("location")).toHaveTextContent("/ui/capsules/capsule-2");
    expect(screen.queryByRole("dialog", { name: "Command palette" })).not.toBeInTheDocument();

    screen.getByRole("button", { name: /New project/ }).focus();
    await openPalette();
    const recent = screen.getByRole("group", { name: "Recent" });
    expect(within(recent).getAllByRole("option")).toHaveLength(1);
    expect(recent).toHaveTextContent("Fix flaky tests");
    expect(screen.getAllByRole("group")[0]).toBe(recent);
    expect(screen.getByRole("group", { name: "Capsules" })).not.toHaveTextContent("Fix flaky tests");
  });

  it("navigates, opens dialogs, and shows shortcut hints", async () => {
    onPlatform("Linux x86_64");
    mockAPI();
    renderShell();
    await screen.findByRole("link", { name: /Review the launcher/ });

    await openPalette();
    await userEvent.type(screen.getByRole("combobox"), "threads");
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("heading", { name: "Thread fleet" })).toBeInTheDocument();

    await openPalette();
    const newProject = screen.getByRole("option", { name: /New project/ });
    expect(newProject).toHaveAttribute("aria-keyshortcuts", "N");
    expect(within(newProject).getByText("N")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("option", { name: /New Capsule in Meridian/ }));
    expect(screen.getByRole("dialog", { name: "Start work in Meridian" })).toBeInTheDocument();
  });

  it("lists keyboard shortcuts from ? and from the palette", async () => {
    onPlatform("MacIntel");
    mockAPI();
    renderShell();
    await screen.findByRole("link", { name: /Review the launcher/ });
    await userEvent.keyboard("?");
    const dialog = screen.getByRole("dialog", { name: "Keyboard shortcuts" });
    expect(dialog).toHaveTextContent("⌘ KOpen the command palette");
    expect(dialog).toHaveTextContent("Previous Capsule");
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "Keyboard shortcuts" })).not.toBeInTheDocument();

    await userEvent.keyboard("{Meta>}k{/Meta}");
    await userEvent.type(screen.getByRole("combobox"), "shortcuts");
    await userEvent.keyboard("{Enter}");
    expect(screen.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeInTheDocument();
  });

  it("lists sessions of multi-session Capsules and jumps to them", async () => {
    onPlatform("Linux x86_64");
    mockAPI({ structured: true }, [thread]);
    renderShell();
    await screen.findByRole("link", { name: /Review the launcher/ });
    await openPalette();
    const sessions = await screen.findByRole("group", { name: "Sessions" });
    const labels = within(sessions).getAllByRole("option").map((option) => option.textContent);
    expect(labels).toEqual([
      expect.stringContaining("OpenCodeTerminal · Review the launcher"),
      expect.stringContaining("Claude CodeIdle · Review the launcher"),
    ]);
    await userEvent.click(within(sessions).getByRole("option", { name: /Claude Code/ }));
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/ui/capsules/capsule-1?session=thread-1",
    );
  });

  it("gates current Capsule commands on capabilities", async () => {
    onPlatform("Linux x86_64");
    mockAPI();
    const view = renderShell("/ui/capsules/capsule-1");
    await screen.findByRole("link", { name: /Review the launcher/ });
    await openPalette();
    let current = screen.getByRole("group", { name: "Current Capsule" });
    expect(within(current).getByRole("option", { name: /Open lineage/ })).toBeInTheDocument();
    expect(within(current).getByRole("option", { name: /Delete…/ })).toBeInTheDocument();
    expect(within(current).queryByRole("option", { name: /Open full diff/ })).not.toBeInTheDocument();
    expect(within(current).queryByRole("option", { name: /Pause/ })).not.toBeInTheDocument();
    expect(within(current).queryByRole("option", { name: /Seal/ })).not.toBeInTheDocument();
    view.unmount();

    vi.restoreAllMocks();
    onPlatform("Linux x86_64");
    mockAPI({ git: true, pause: true, snapshot: true });
    renderShell("/ui/capsules/capsule-1");
    await screen.findByRole("link", { name: /Review the launcher/ });
    await openPalette();
    current = await screen.findByRole("group", { name: "Current Capsule" });
    await waitFor(() =>
      expect(within(current).getByRole("option", { name: /Open full diff/ })).toBeInTheDocument(),
    );
    expect(within(current).getByRole("option", { name: /Pause/ })).toBeInTheDocument();
    expect(within(current).queryByRole("option", { name: /Resume/ })).not.toBeInTheDocument();
    expect(within(current).getByRole("option", { name: /Seal…/ })).toBeInTheDocument();
    await userEvent.click(within(current).getByRole("option", { name: /Open full diff/ }));
    expect(screen.getByTestId("location")).toHaveTextContent("/ui/capsules/capsule-1/diff");
  });

  it("opens the confirmation for Delete instead of deleting", async () => {
    onPlatform("Linux x86_64");
    mockAPI({ snapshot: true });
    const lifecycle = vi.spyOn(api, "lifecycle");
    const seal = vi.spyOn(api, "seal");
    renderApp("/ui/capsules/capsule-1");
    await screen.findByRole("heading", { name: "Review the launcher" });
    await screen.findByRole("link", { name: /Fix flaky tests/ });

    await openPalette();
    await userEvent.type(screen.getByRole("combobox"), "delete");
    await userEvent.keyboard("{Enter}");
    const confirm = await screen.findByRole("alertdialog", { name: "Delete Review the launcher?" });
    expect(within(confirm).getByRole("button", { name: "Keep Capsule" })).toHaveFocus();
    expect(lifecycle).not.toHaveBeenCalled();
    await userEvent.click(within(confirm).getByRole("button", { name: "Keep Capsule" }));

    await openPalette();
    await userEvent.type(screen.getByRole("combobox"), "seal");
    await userEvent.keyboard("{Enter}");
    expect(
      await screen.findByRole("alertdialog", { name: "Seal Review the launcher?" }),
    ).toBeInTheDocument();
    expect(seal).not.toHaveBeenCalled();
    expect(lifecycle).not.toHaveBeenCalled();
  });

  it("pauses the current Capsule through its actions menu", async () => {
    onPlatform("Linux x86_64");
    mockAPI({ pause: true });
    const lifecycle = vi
      .spyOn(api, "lifecycle")
      .mockResolvedValue({ ...capsule, desiredState: "Paused" });
    renderApp("/ui/capsules/capsule-1/diff");
    await screen.findByRole("link", { name: /Review the launcher/ });

    await openPalette();
    await userEvent.type(screen.getByRole("combobox"), "pause");
    await userEvent.keyboard("{Enter}");
    await waitFor(() =>
      expect(lifecycle).toHaveBeenCalledWith("pause", capsule.id, capsule.resourceVersion),
    );
    expect(lifecycle).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("location")).toHaveTextContent(/^\/ui\/capsules\/capsule-1$/);
  });
});

describe("fuzzy matching", () => {
  const command = (label: string, detail?: string): PaletteCommand => ({
    id: label,
    group: "Capsules",
    label,
    detail,
    run: () => undefined,
  });

  it("prefers label word starts over scattered subsequences", () => {
    const exact = fuzzyScore("pau", command("Pause"));
    const scattered = fuzzyScore("pau", command("Pick a unit"));
    expect(exact).toBeDefined();
    expect(scattered).toBeDefined();
    expect(exact!).toBeGreaterThan(scattered!);
    expect(fuzzyScore("xyz", command("Pause"))).toBeUndefined();
    expect(fuzzyScore("meridian", command("Fix tests", "Meridian · Idle"))).toBeDefined();
    expect(fuzzyScore("rev", command("Harness settings", "providers setups"))).toBeUndefined();
  });
});
