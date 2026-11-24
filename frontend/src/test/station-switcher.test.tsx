import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { api } from "../api/client";
import type { Capabilities, Project } from "../api/generated/types.gen";
import { SetupChangeCheck } from "../components/WorkstationProfile";
import { AppShell } from "../shell/AppShell";
import { clearComposerDrafts, trackComposerDraft } from "../shell/composerDraft";
import { buildPaletteCommands, type PaletteActions } from "../shell/paletteCommands";
import { isStationShortcut } from "../shell/stationSwitch";

const project: Project = {
  id: "project-1",
  name: "Ledger",
  harnessImages: [],
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
  harnessImages: [],
};

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  delete window.__MERIDIAN_DESKTOP__;
  clearComposerDrafts();
});

function renderShell() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.spyOn(api, "projects").mockResolvedValue({ items: [project] });
  vi.spyOn(api, "capsules").mockResolvedValue({ items: [] });
  vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
  vi.spyOn(api, "capabilities").mockResolvedValue(capabilities);
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/"]}>
        <Routes>
          <Route path="/ui" element={<AppShell />}>
            <Route index element={<p>Home</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("saved Stations", () => {
  it("hides the switcher in a browser", async () => {
    renderShell();
    expect(await screen.findByRole("link", { name: "Meridian home" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Spark/ })).not.toBeInTheDocument();
  });

  it("names the open Station and sends switching to the desktop picker", async () => {
    const switchStation = vi.fn().mockResolvedValue(undefined);
    const openAddStation = vi.fn().mockResolvedValue(undefined);
    const openGatewaySettings = vi.fn();
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      openGatewaySettings,
      openAddStation,
      switchStation,
      gateway: vi.fn().mockResolvedValue({ label: "Spark", local: false }),
    };
    renderShell();
    const opener = await screen.findByRole("button", { name: "Station: Spark" });
    await userEvent.click(opener);
    const menu = screen.getByRole("menu", { name: "Stations" });
    // The page never receives the saved list, so the menu cannot show it.
    expect(menu).not.toHaveTextContent("This Mac");
    await userEvent.click(screen.getByRole("menuitem", { name: /Switch or manage Stations/ }));
    expect(openGatewaySettings).toHaveBeenCalledTimes(1);
    await userEvent.click(opener);
    await userEvent.click(screen.getByRole("menuitem", { name: /Add Station/ }));
    expect(openAddStation).toHaveBeenCalledTimes(1);

    // ⌘/Ctrl+digit sends only the slot number.
    const apple = /Mac|iPhone|iPad/.test(navigator.platform);
    fireEvent.keyDown(window, { key: "3", metaKey: apple, ctrlKey: !apple });
    expect(switchStation).toHaveBeenCalledWith(3);

    // An unsent draft asks first, and Escape keeps the page.
    const token = Symbol("draft");
    trackComposerDraft(token, true);
    fireEvent.keyDown(window, { key: "2", metaKey: apple, ctrlKey: !apple });
    const dialog = screen.getByRole("dialog", { name: "Switch Station?" });
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Switch Station?" })).not.toBeInTheDocument();
    expect(switchStation).toHaveBeenCalledTimes(1);
    await userEvent.click(opener);
    await userEvent.click(screen.getByRole("menuitem", { name: /Switch or manage Stations/ }));
    expect(openGatewaySettings).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole("button", { name: "Switch" }));
    expect(openGatewaySettings).toHaveBeenCalledTimes(2);

    await userEvent.click(screen.getByRole("button", { name: /Commands/ }));
    await userEvent.type(screen.getByRole("combobox"), "station");
    expect(screen.getByRole("option", { name: /Switch or manage Stations/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Add Station/ })).toBeInTheDocument();
  });

  it("does not ask about a draft for ⌘1 on the Station on this computer", async () => {
    const switchStation = vi.fn().mockResolvedValue(undefined);
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      openGatewaySettings: vi.fn(),
      switchStation,
      gateway: vi.fn().mockResolvedValue({ label: "This Mac", local: true }),
    };
    renderShell();
    await screen.findByRole("button", { name: "Station: This Mac" });
    trackComposerDraft(Symbol("draft"), true);
    const apple = /Mac|iPhone|iPad/.test(navigator.platform);
    fireEvent.keyDown(window, { key: "1", metaKey: apple, ctrlKey: !apple });
    expect(screen.queryByRole("dialog", { name: "Switch Station?" })).not.toBeInTheDocument();
    expect(switchStation).not.toHaveBeenCalled();
  });

  it("uses ⌘/Ctrl+1–9 and leaves the other shortcuts alone", () => {
    const down = { altKey: false, shiftKey: false, metaKey: false, ctrlKey: false };
    expect(isStationShortcut({ ...down, key: "1", metaKey: true }, true)).toBe(true);
    expect(isStationShortcut({ ...down, key: "9", ctrlKey: true }, false)).toBe(true);
    expect(isStationShortcut({ ...down, key: "9", ctrlKey: true }, false, true)).toBe(false);
    expect(isStationShortcut({ ...down, key: "k", metaKey: true }, true)).toBe(false);
    expect(isStationShortcut({ ...down, key: "n", metaKey: true }, true)).toBe(false);
    expect(isStationShortcut({ ...down, key: "1" }, true)).toBe(false);
    const actions = {
      navigate: () => undefined,
      requestCapsuleAction: () => undefined,
      openNewProject: () => undefined,
      openNewSession: () => undefined,
      openDeleteProject: () => undefined,
      openShortcuts: () => undefined,
      openSettings: () => undefined,
      openAddStation: () => undefined,
      manageStations: () => undefined,
    } satisfies PaletteActions;
    const commands = buildPaletteCommands({ items: [], projects: [], capsuleId: "", actions });
    const stationCommands = commands.filter((command) => command.group === "Stations").map((command) => command.label);
    expect(stationCommands).toEqual(["Switch or manage Stations…", "Add Station…"]);
  });

  it("puts the setup-change check in Settings and leaves it on", async () => {
    const setSetupCheck = vi.fn().mockResolvedValue(undefined);
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      setupCheck: vi.fn().mockResolvedValue(true),
      setSetupCheck,
    };
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <SetupChangeCheck />
      </QueryClientProvider>,
    );
    const box = await screen.findByRole("checkbox", { name: /Check this computer for setup changes/ });
    expect(box).toBeChecked();
    await userEvent.click(box);
    expect(setSetupCheck).toHaveBeenCalledWith(false);
  });
});
