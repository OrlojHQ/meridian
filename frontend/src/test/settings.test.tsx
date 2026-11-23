import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Capabilities, Profile, Project } from "../api/generated/types.gen";
import { harnessPreferenceKey } from "../components/NewProjectDialog";
import { readTheme } from "../components/ui/theme";
import { AppShell } from "../shell/AppShell";
import { filterSettings, settingsRegistry, settingsSections } from "../settings/registry";

const project: Project = {
  id: "project-1",
  name: "Meridian",
  harnessImages: [{ name: "opencode", imageReference: "capsule-opencode" }],
  createdAt: "2026-08-26T00:00:00Z",
  updatedAt: "2026-08-26T00:00:00Z",
  resourceVersion: 1,
};

const capabilities: Capabilities = {
  providerVersion: "docker/v1",
  isolation: "container",
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
  harnessImages: project.harnessImages ?? [],
};

const profile: Profile = {
  gitName: "Ada Lovelace",
  gitEmail: "ada@example.com",
  defaultConnections: {},
  setups: [],
  resourceVersion: 1,
  updatedAt: "2026-08-26T00:00:00Z",
};

function renderShell() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/"]}>
        <Routes>
          <Route path="/ui" element={<AppShell />}>
            <Route index element={<p>Activity home</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function mockShell() {
  vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
  vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
  vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
  vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
  vi.spyOn(api, "authenticationMode").mockResolvedValue({ mode: "single_user", signIn: false, claimable: false });
  vi.spyOn(api, "board").mockResolvedValue({ items: [], truncated: false });
  vi.spyOn(api, "profile").mockResolvedValue(profile);
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: true });
  vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
}

describe("settings registry", () => {
  it("keeps every setting in one list with a unique id and a known section", () => {
    const ids = settingsRegistry.map((setting) => setting.id);
    expect(new Set(ids).size).toBe(ids.length);
    const sections = new Set(settingsSections.map((section) => section.id));
    for (const setting of settingsRegistry) {
      expect(sections.has(setting.section)).toBe(true);
      expect(setting.label.trim()).not.toBe("");
    }
    expect(ids).toEqual(expect.arrayContaining([
      "setup-check",
      "git-identity",
      "theme",
      "default-harness",
      "default-connections",
      "routines",
      "account",
    ]));
  });

  it("filters settings by name and description", () => {
    const theme = filterSettings("color theme").map((setting) => setting.id);
    expect(theme).toContain("theme");
    expect(theme).not.toContain("git-identity");
    const git = filterSettings("email").map((setting) => setting.id);
    expect(git).toContain("git-identity");
    expect(filterSettings("")).toHaveLength(settingsRegistry.length);
    expect(filterSettings("zzzz-not-a-setting")).toHaveLength(0);
  });
});

describe("settings window", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });

  it("opens from the gear and from Ctrl+,", async () => {
    mockShell();
    const user = userEvent.setup();
    renderShell();
    await user.click(await screen.findByRole("button", { name: "Settings" }));
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    expect(within(dialog).getByRole("button", { name: "General" })).toHaveAttribute("aria-current", "page");
    expect(within(dialog).getByText("Git commit identity")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Close settings" }));
    expect(screen.queryByRole("dialog", { name: "Settings" })).not.toBeInTheDocument();
    await user.keyboard("{Control>},{/Control}");
    expect(await screen.findByRole("dialog", { name: "Settings" })).toBeInTheDocument();
  });

  it("filters the open window and keeps a theme change", async () => {
    mockShell();
    const user = userEvent.setup();
    renderShell();
    await user.click(await screen.findByRole("button", { name: "Settings" }));
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    await user.type(within(dialog).getByRole("textbox", { name: "Search settings" }), "color");
    expect(within(dialog).getByText("Color theme")).toBeInTheDocument();
    expect(within(dialog).queryByText("Git commit identity")).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("radio", { name: "Dark" }));
    expect(readTheme()).toBe("dark");
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(localStorage.getItem("meridian.theme")).toBe("dark");
    await user.clear(within(dialog).getByRole("textbox", { name: "Search settings" }));
    await user.click(within(dialog).getByRole("button", { name: "General" }));
    const harness = await within(dialog).findByRole("combobox", { name: "Default harness for a new project" });
    await user.selectOptions(harness, "opencode");
    expect(localStorage.getItem(harnessPreferenceKey)).toBe("opencode");
  });
});
