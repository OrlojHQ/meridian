import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Capabilities, Profile, Project } from "../api/generated/types.gen";
import { harnessPreferenceKey } from "../components/NewProjectDialog";
import { readTheme } from "../components/ui/theme";
import { AppShell } from "../shell/AppShell";
import { UPDATE_LATER_KEY, UPDATE_PROGRESS_EVENT, recordUpdateCheck } from "../shell/desktopUpdate";
import { filterSettings, settingsRegistry, settingsSections } from "../settings/registry";
import { choose } from "./richSelect";

const project: Project = {
  id: "project-1",
  name: "Meridian",
  harnessImages: [
    { name: "opencode", imageReference: "capsule-opencode" },
    { name: "claude", imageReference: "capsule-claude" },
  ],
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
  build: { version: "2.3.0", commit: "abc1234", date: "2026-11-20T00:00:00Z" },
};

const profile: Profile = {
  gitName: "Ada Lovelace",
  gitEmail: "ada@example.com",
  capsuleGitHubAccess: true,
  defaultConnections: {},
  setups: [],
  resourceVersion: 1,
  updatedAt: "2026-08-26T00:00:00Z",
};

function renderShell(initialEntry = "/ui/") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <Routes>
          <Route path="/ui" element={<AppShell />}>
            <Route index element={<p>Activity home</p>} />
            <Route path="projects/:projectId" element={<p>Project page</p>} />
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
    delete window.__MERIDIAN_DESKTOP__;
    recordUpdateCheck(null);
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
    await choose(harness, "Claude Code", user);
    expect(harness).toHaveTextContent("Claude Code");
    expect(localStorage.getItem(harnessPreferenceKey)).toBe("claude");
  });

  it("turns off the Capsule GitHub App token from General and keeps the rest of the profile", async () => {
    mockShell();
    const update = vi.spyOn(api, "updateProfile").mockResolvedValue({ ...profile, capsuleGitHubAccess: false, resourceVersion: 2 });
    const user = userEvent.setup();
    renderShell("/ui/?settings=general");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    const toggle = await within(dialog).findByRole("checkbox", { name: "Give Capsules a GitHub App token" });
    expect(toggle).toBeChecked();
    await user.click(toggle);
    expect(update).toHaveBeenCalledWith({
      gitName: "Ada Lovelace",
      gitEmail: "ada@example.com",
      capsuleGitHubAccess: false,
      defaultConnections: {},
      expectedResourceVersion: 1,
    });
    expect(await within(dialog).findByRole("checkbox", { name: "Give Capsules a GitHub App token" })).not.toBeChecked();
  });

  it("keeps Settings open when clearing a Routines project filter or dismissing a stacked modal", async () => {
    mockShell();
    vi.spyOn(api, "allRoutines").mockResolvedValue({ items: [] });
    const user = userEvent.setup();
    renderShell("/ui/?settings=routines&project=project-1");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    const routinesArticle = within(dialog).getByRole("article", { name: "Routines" });
    expect(routinesArticle).not.toHaveAttribute("aria-labelledby");
    await user.click(await within(dialog).findByRole("button", { name: "Show all" }));
    expect(screen.getByRole("dialog", { name: "Settings" })).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Show all" })).not.toBeInTheDocument();

    await user.keyboard("{Control>}k{/Control}");
    expect(await screen.findByRole("dialog", { name: "Command palette" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "Command palette" })).not.toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Settings" })).toBeInTheDocument();
  });

  it("shows versions, the update the background check found, and inline install progress", async () => {
    mockShell();
    const checkForUpdates = vi.fn().mockResolvedValue({
      available: true,
      version: "2.4.0",
      action: "install",
    });
    const installUpdate = vi.fn().mockImplementation(() => new Promise<void>(() => undefined));
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      gateway: vi.fn().mockResolvedValue({ label: "This computer", local: true }),
      appInfo: vi.fn().mockResolvedValue({ version: "2.3.0", install: "mac-app", selfUpdate: true }),
      checkForUpdates,
      installUpdate,
    };
    const user = userEvent.setup();
    renderShell("/ui/?settings=updates");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    expect(within(dialog).getByRole("heading", { level: 2, name: "Updates" })).toBeInTheDocument();
    expect(await within(dialog).findByText("This computer")).toBeInTheDocument();
    expect(await within(dialog).findByText("macOS app")).toBeInTheDocument();
    expect(within(dialog).getAllByText("2.3.0")).toHaveLength(2);
    expect(within(dialog).queryByText("Differs from the desktop app")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("docker/v1")).not.toBeInTheDocument();

    // The shell's automatic check already found it: no second click needed.
    expect(await within(dialog).findByText("Update available")).toBeInTheDocument();
    expect(within(dialog).getByText("Meridian 2.4.0")).toBeInTheDocument();
    expect(checkForUpdates).toHaveBeenCalledOnce();
    // Release notes follow the offered release.
    expect(within(dialog).getByRole("link", { name: "What’s new in 2.4.0" })).toHaveAttribute(
      "href",
      "https://github.com/OrlojHQ/meridian/releases/tag/v2.4.0",
    );

    await user.click(within(dialog).getByRole("button", { name: "Update & restart" }));
    expect(installUpdate).toHaveBeenCalledOnce();
    act(() => {
      window.dispatchEvent(new CustomEvent(UPDATE_PROGRESS_EVENT, { detail: { phase: "download", percent: 58 } }));
    });
    expect(await within(dialog).findByRole("button", { name: "Downloading… 58%" })).toBeDisabled();
  });

  it("shows a copyable update command on Updates when action is command", async () => {
    mockShell();
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      appInfo: vi.fn().mockResolvedValue({ version: "2.3.0", install: "homebrew", selfUpdate: false }),
      checkForUpdates: vi.fn().mockResolvedValue({
        available: true,
        version: "2.5.0",
        action: "command",
        command: "brew upgrade --cask meridian-desktop",
      }),
    };
    renderShell("/ui/?settings=updates");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    expect(await within(dialog).findByRole("button", { name: /Copy update command/ })).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Update & restart" })).not.toBeInTheDocument();
    expect(await within(dialog).findByText(/This copy is managed by Homebrew/)).toBeInTheDocument();
  });

  it("says when the app is up to date, when it last checked, and when the Station differs", async () => {
    mockShell();
    const checkForUpdates = vi.fn().mockResolvedValue({ available: false });
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      appInfo: vi.fn().mockResolvedValue({ version: "2.4.0", install: "mac-app", selfUpdate: true }),
      checkForUpdates,
    };
    const user = userEvent.setup();
    renderShell("/ui/?settings=updates");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    expect(await within(dialog).findByText("Meridian Desktop 2.4.0")).toBeInTheDocument();
    expect(await within(dialog).findByText("Up to date")).toBeInTheDocument();
    expect(within(dialog).getByText("Checked just now.")).toBeInTheDocument();
    expect(await within(dialog).findByText("Differs from the desktop app")).toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: "What’s new in 2.4.0" })).toHaveAttribute(
      "href",
      "https://github.com/OrlojHQ/meridian/releases/tag/v2.4.0",
    );

    await user.click(within(dialog).getByRole("button", { name: "Check again" }));
    expect(checkForUpdates).toHaveBeenCalledTimes(2);
  });

  it("reports a failed check as a failure, not as up to date", async () => {
    mockShell();
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      checkForUpdates: vi.fn().mockResolvedValue({ available: false, detail: "Couldn't check for updates." }),
    };
    renderShell("/ui/?settings=updates");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    expect(await within(dialog).findByText("Check failed")).toBeInTheDocument();
    expect(within(dialog).queryByText("Up to date")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("snoozes a release from Updates and turns reminders back on", async () => {
    mockShell();
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      checkForUpdates: vi.fn().mockResolvedValue({ available: true, version: "2.4.0", action: "install" }),
      installUpdate: vi.fn(),
    };
    const user = userEvent.setup();
    renderShell("/ui/?settings=updates");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    await user.click(await within(dialog).findByRole("button", { name: "Remind me later" }));
    expect(within(dialog).getByText(/Reminders for 2.4.0 are off until/)).toBeInTheDocument();
    expect(localStorage.getItem(UPDATE_LATER_KEY)).toContain("2.4.0");

    await user.click(within(dialog).getByRole("button", { name: "Turn reminders back on" }));
    expect(localStorage.getItem(UPDATE_LATER_KEY)).toBeNull();
    expect(within(dialog).getByRole("button", { name: "Remind me later" })).toBeInTheDocument();
  });

  it("shows the Station version in the browser, without desktop controls", async () => {
    mockShell();
    renderShell("/ui/?settings=updates");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    expect(within(dialog).getByText("Browser")).toBeInTheDocument();
    expect(await within(dialog).findByText("2.3.0")).toBeInTheDocument();
    expect(within(dialog).queryByText("Desktop app")).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Check for updates" })).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Copy version info" })).toBeInTheDocument();
  });

  it("opens the app's review window from the git identity setting", async () => {
    mockShell();
    const openImport = vi.fn().mockResolvedValue(true);
    window.__MERIDIAN_DESKTOP__ = { notify: () => undefined, setAttention: () => undefined, openImport };
    const user = userEvent.setup();
    renderShell("/ui/?settings=general");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    await user.click(await within(dialog).findByRole("button", { name: "Use this computer's identity…" }));
    expect(openImport).toHaveBeenCalledOnce();
    expect(await within(dialog).findByText("Review and save in the window that opened.")).toBeInTheDocument();
  });

  it("offers no computer identity in the browser", async () => {
    mockShell();
    renderShell("/ui/?settings=general");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    expect(await within(dialog).findByRole("button", { name: "Save identity" })).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Use this computer's identity…" })).not.toBeInTheDocument();
  });

  it("explains disabled lead controls on Agents when not viewing a lead Thread", async () => {
    mockShell();
    renderShell("/ui/?settings=agents");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    expect(within(dialog).getByText("Defaults shown")).toBeInTheDocument();
    expect(within(dialog).getByRole("combobox", { name: "Pause without you" })).toBeDisabled();
    expect(within(dialog).getByRole("checkbox", { name: "Approve each spawn" })).toBeDisabled();
  });

  it("renders embedded project editors directly without duplicate chrome on Projects", async () => {
    mockShell();
    vi.spyOn(api, "projectChecks").mockResolvedValue({
      checks: [],
      autoRework: true,
      maxReworkAttempts: 3,
      resourceVersion: 1,
    });
    vi.spyOn(api, "suggestProjectChecks").mockResolvedValue({ checks: [] });
    vi.spyOn(api, "projectEnvironment").mockResolvedValue({
      enabled: true,
      setup: [],
      generation: 1,
      imageReference: "capsule-base:latest",
      resourceVersion: 1,
    });
    vi.spyOn(api, "projectNotes").mockResolvedValue({
      items: [],
      notesDelivery: "all_threads",
      settingsVersion: 1,
      liveCount: 0,
      totalBytes: 0,
      maxNotes: 64,
      maxBytes: 65536,
      pendingProposals: 0,
    });
    renderShell("/ui/?settings=projects");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    expect(await within(dialog).findByLabelText("Setup script")).toBeInTheDocument();
    expect(within(dialog).queryByRole("heading", { level: 2, name: "Checks" })).not.toBeInTheDocument();
    expect(dialog.querySelector("fieldset.agent-picker")).toBeNull();
  });

  it("links claimable installations from Advanced directly to Account", async () => {
    mockShell();
    vi.spyOn(api, "authenticationMode").mockResolvedValue({ mode: "single_user", signIn: false, claimable: true });
    const user = userEvent.setup();
    renderShell("/ui/?settings=advanced");
    const dialog = await screen.findByRole("dialog", { name: "Settings" });
    await user.click(await within(dialog).findByRole("button", { name: "Claim in Account" }));
    expect(within(dialog).getByRole("button", { name: "Account" })).toHaveAttribute("aria-current", "page");
  });
});
