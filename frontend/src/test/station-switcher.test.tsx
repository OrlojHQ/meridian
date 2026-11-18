import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { api } from "../api/client";
import type { Capabilities, Project } from "../api/generated/types.gen";
import { SetupChangeCheck } from "../components/WorkstationProfile";
import { AppShell } from "../shell/AppShell";
import { clearComposerDrafts, trackComposerDraft } from "../shell/composerDraft";
import { buildPaletteCommands, type PaletteActions } from "../shell/paletteCommands";
import { isStationShortcut, parseStations } from "../shell/stationSwitch";

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

const stations = [
  { id: "local", name: "This Mac", host: "This computer", local: true, current: false },
  { id: "https://spark.local:8080", name: "Spark", host: "spark.local:8080", local: false, current: true },
  { id: "https://10.0.0.5:8080", name: "Work", host: "10.0.0.5:8080", local: false, current: false },
];

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

  it("lists saved Stations, switches, and warns about an unsent draft", async () => {
    const switchStation = vi.fn().mockResolvedValue(undefined);
    const openAddStation = vi.fn().mockResolvedValue(undefined);
    const openGatewaySettings = vi.fn();
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      openGatewaySettings,
      openAddStation,
      stations: vi.fn().mockResolvedValue(stations),
      switchStation,
      stationStatus: vi.fn().mockResolvedValue([
        { id: "local", online: true },
        { id: "https://spark.local:8080", online: true },
        { id: "https://10.0.0.5:8080", online: false },
      ]),
    };
    renderShell();
    const opener = await screen.findByRole("button", { name: /Spark/ });
    await userEvent.click(opener);
    const menu = screen.getByRole("menu", { name: "Stations" });
    expect(menu).toHaveTextContent("This Mac");
    expect(menu).toHaveTextContent("spark.local:8080");
    expect(menu).toHaveTextContent("Offline");
    await userEvent.click(screen.getByRole("menuitem", { name: /Work/ }));
    await waitFor(() => expect(switchStation).toHaveBeenCalledWith("https://10.0.0.5:8080"));

    const token = Symbol("draft");
    trackComposerDraft(token, true);
    await userEvent.click(await screen.findByRole("button", { name: /Spark/ }));
    await userEvent.click(screen.getByRole("menuitem", { name: /This Mac/ }));
    expect(screen.getByRole("dialog", { name: "Switch Station?" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Stay" }));
    expect(switchStation).toHaveBeenCalledTimes(1);
    await userEvent.click(await screen.findByRole("button", { name: /Spark/ }));
    await userEvent.click(screen.getByRole("menuitem", { name: /This Mac/ }));
    await userEvent.click(screen.getByRole("button", { name: "Switch" }));
    expect(switchStation).toHaveBeenCalledWith("local");

    await userEvent.click(screen.getByRole("button", { name: /Commands/ }));
    await userEvent.type(screen.getByRole("combobox"), "switch");
    expect(screen.getByRole("option", { name: /Switch to Spark/ })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /Switch to Work/ })).toHaveTextContent("10.0.0.5:8080");
  });

  it("rejects a station list that is not names and origins", () => {
    expect(parseStations([{ id: "local", name: "This Mac", host: "This computer", local: true, current: true }])).toHaveLength(1);
    expect(parseStations([{ id: "x", name: "", host: "h", local: true, current: false }])).toEqual([]);
    // The shell allows 60 characters, counted as Unicode scalar values. Emoji
    // are two UTF-16 units each and must not push a valid name over.
    const emoji = "🛰️".replace("\uFE0F", "").repeat(60);
    expect(parseStations([{ id: "local", name: emoji, host: "This computer", local: true, current: true }])).toHaveLength(1);
    expect(
      parseStations([{ id: "local", name: `${emoji}x`, host: "This computer", local: true, current: true }]),
    ).toEqual([]);
    // An address at the shell's 300-byte limit is accepted.
    const longOrigin = `https://${"a".repeat(292)}`;
    expect(longOrigin).toHaveLength(300);
    expect(
      parseStations([{ id: longOrigin, name: "Long", host: longOrigin.slice(8), local: false, current: false }]),
    ).toHaveLength(1);
    // One bad entry is skipped without hiding the rest.
    expect(
      parseStations([
        { id: "local", name: "This Mac", host: "This computer", local: true, current: true },
        { id: "bad", name: 7, host: "h", local: false, current: false },
        { id: "https://spark.local:8080", name: "Spark", host: "spark.local:8080", local: false, current: false },
      ])?.map((station) => station.id),
    ).toEqual(["local", "https://spark.local:8080"]);
    expect(parseStations("nope")).toBeUndefined();
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
      openLauncher: () => undefined,
      openDeleteProject: () => undefined,
      openShortcuts: () => undefined,
      switchStation: () => undefined,
      openAddStation: () => undefined,
      manageStations: () => undefined,
    } satisfies PaletteActions;
    const commands = buildPaletteCommands({
      items: [],
      projects: [],
      capsuleId: "",
      actions,
      stations: stations.map((station) => ({
        id: station.id,
        name: station.name,
        host: station.host,
        current: station.current,
      })),
    });
    expect(commands.find((command) => command.label === "Switch to Spark")?.shortcut?.[1]).toBe("2");
    expect(commands.find((command) => command.label === "Switch to Spark")?.detail).toContain("Current");
    expect(commands.some((command) => command.label === "Add Station…")).toBe(true);
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
