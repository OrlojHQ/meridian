import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Capabilities, Capsule, Project } from "../api/generated";
import { AppShell } from "../shell/AppShell";

const WIDTH_KEY = "meridian.contextPanel.width";

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

const originalWidth = window.innerWidth;

function renderShell() {
  vi.spyOn(window, "matchMedia").mockImplementation(
    () =>
      ({
        matches: false,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      }) as unknown as MediaQueryList,
  );
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
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/threads/thread-1"]}>
        <Routes>
          <Route path="/ui" element={<AppShell />}>
            <Route path="threads/:threadId" element={<p>Conversation</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function shellWidth(container: HTMLElement) {
  return (container.querySelector(".app-shell") as HTMLElement).style.getPropertyValue("--context-width");
}

describe("Capsule tools resizing", () => {
  beforeEach(() => {
    // 1600px wide with the 18rem sidebar and 25rem Thread floor gives a
    // 384px to 912px range and a 672px (42vw) default.
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1600 });
    window.localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    window.localStorage.clear();
    Object.defineProperty(window, "innerWidth", { configurable: true, value: originalWidth });
    document.documentElement.classList.remove("resizing-context");
  });

  it("resizes with the keyboard, remembers the width, and resets on double-click", async () => {
    const user = userEvent.setup();
    const { container } = renderShell();
    const handle = await screen.findByRole("separator", { name: "Resize Capsule tools" });
    expect(handle).toHaveAttribute("aria-valuenow", "672");
    expect(handle).toHaveAttribute("aria-valuemin", "384");
    expect(handle).toHaveAttribute("aria-valuemax", "912");
    expect(shellWidth(container)).toBe("");

    handle.focus();
    await user.keyboard("{ArrowLeft}");
    expect(handle).toHaveAttribute("aria-valuenow", "688");
    expect(shellWidth(container)).toContain("688px");
    expect(window.localStorage.getItem(WIDTH_KEY)).toBe("688");

    await user.keyboard("{Shift>}{ArrowRight}{/Shift}");
    expect(handle).toHaveAttribute("aria-valuenow", "624");
    await user.keyboard("{Home}");
    expect(handle).toHaveAttribute("aria-valuenow", "384");
    await user.keyboard("{End}");
    expect(handle).toHaveAttribute("aria-valuenow", "912");

    await user.dblClick(handle);
    expect(handle).toHaveAttribute("aria-valuenow", "672");
    expect(shellWidth(container)).toBe("");
    expect(window.localStorage.getItem(WIDTH_KEY)).toBeNull();
  });

  it("restores a saved width and keeps it across hiding the panel", async () => {
    window.localStorage.setItem(WIDTH_KEY, "700");
    const user = userEvent.setup();
    const { container } = renderShell();
    const handle = await screen.findByRole("separator", { name: "Resize Capsule tools" });
    expect(handle).toHaveAttribute("aria-valuenow", "700");
    expect(shellWidth(container)).toContain("700px");

    await user.click(screen.getByRole("button", { name: "Hide Capsule tools" }));
    expect(screen.queryByRole("separator", { name: "Resize Capsule tools" })).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Show Capsule tools" }));
    expect(await screen.findByRole("separator", { name: "Resize Capsule tools" })).toHaveAttribute(
      "aria-valuenow",
      "700",
    );
  });

  it("ignores a saved width that is not a number and clamps one out of range", async () => {
    window.localStorage.setItem(WIDTH_KEY, "wide");
    const first = renderShell();
    expect(await screen.findByRole("separator", { name: "Resize Capsule tools" })).toHaveAttribute(
      "aria-valuenow",
      "672",
    );
    expect(shellWidth(first.container)).toBe("");
    first.unmount();

    window.localStorage.setItem(WIDTH_KEY, "5000");
    renderShell();
    expect(await screen.findByRole("separator", { name: "Resize Capsule tools" })).toHaveAttribute(
      "aria-valuenow",
      "912",
    );
  });

  it("resizes by dragging the panel edge", async () => {
    renderShell();
    const handle = await screen.findByRole("separator", { name: "Resize Capsule tools" });
    fireEvent.pointerDown(handle, { button: 0, pointerId: 1, clientX: 928 });
    expect(document.documentElement).toHaveClass("resizing-context");
    fireEvent.pointerMove(handle, { pointerId: 1, clientX: 900 });
    fireEvent.pointerUp(handle, { pointerId: 1, clientX: 900 });
    expect(document.documentElement).not.toHaveClass("resizing-context");
    expect(handle).toHaveAttribute("aria-valuenow", "700");
    expect(window.localStorage.getItem(WIDTH_KEY)).toBe("700");
  });
});
