import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { Capsule, CapsuleStartup } from "../api/generated/types.gen";
import { CapsuleStartup as StartupTracker } from "../components/CapsuleStartup";
import { formatBytes, pullSummary } from "../components/startup";
import { ActivityDot, ProgressBar, Skeleton } from "../components/ui/loading";

const timestamp = "2026-11-10T12:00:00Z";

const capsule = (startup: CapsuleStartup, overrides: Partial<Capsule> = {}): Capsule => ({
  id: "capsule-1",
  projectId: "project-1",
  timelineId: "timeline-1",
  name: "codex",
  harness: "codex",
  state: "Creating",
  desiredState: "Ready",
  restoreComplete: false,
  createdAt: timestamp,
  updatedAt: timestamp,
  resourceVersion: 3,
  startup,
  ...overrides,
});

const phases: CapsuleStartup["phases"] = [
  "pulling_image",
  "creating_capsule",
  "starting_harness",
  "connecting",
];

function show(node: React.ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(<QueryClientProvider client={client}>{node}</QueryClientProvider>);
}

function mockMotion(matches: boolean) {
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: (query: string) => ({
      matches: query.includes("reduce") ? matches : false,
      media: query,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      dispatchEvent: () => false,
      onchange: null,
      addListener: () => undefined,
      removeListener: () => undefined,
    }),
  });
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("startup progress", () => {
  it("formats pull totals without inventing precision", () => {
    expect(formatBytes(312_000_000)).toBe("312 MB");
    expect(formatBytes(540_000_000)).toBe("540 MB");
    expect(pullSummary(312_000_000, 540_000_000)).toBe("312 MB / 540 MB · 57%");
    expect(pullSummary(12)).toBe("12 B");
  });

  it("tracks reported phases and a determinate pull", () => {
    show(
      <StartupTracker
        capsule={capsule({
          phase: "pulling_image",
          phases,
          pull: { currentBytes: 312_000_000, totalBytes: 540_000_000 },
          updatedAt: timestamp,
        })}
      />,
    );
    const items = screen.getAllByRole("listitem");
    expect(items.map((item) => item.textContent)).toEqual([
      "Pulling image",
      "Creating Capsule",
      "Starting harness",
      "Connecting",
    ]);
    expect(items[0]).toHaveClass("is-active");
    expect(items[1]).toHaveClass("is-upcoming");
    expect(items[0].querySelector(".load-dot")).toBeTruthy();
    expect(items[1].querySelector(".load-dot-upcoming")).toBeTruthy();
    const bar = screen.getByRole("progressbar", { name: "312 MB / 540 MB · 57%" });
    expect(bar).toHaveAttribute("aria-valuenow", "57");
    expect(bar).toHaveAttribute("aria-valuemax", "100");
    expect(bar).not.toHaveClass("is-indeterminate");
    expect(document.querySelector(".startup-bytes")).toHaveTextContent("312 MB / 540 MB · 57%");
  });

  it("marks startup phases done once preparation is the reported stage", () => {
    show(
      <StartupTracker
        capsule={capsule(
          {
            phase: "connecting",
            phases,
            updatedAt: timestamp,
          },
          {
            state: "Preparing",
            preparation: { stage: "checkout", reused: false, updatedAt: timestamp },
          },
        )}
      />,
    );
    const items = screen.getAllByRole("listitem");
    expect(items[0]).toHaveClass("is-done");
    expect(items[3]).toHaveClass("is-done");
    expect(items[4]).toHaveClass("is-active");
    expect(items[4]).toHaveTextContent("Checking out");
    expect(screen.getByRole("progressbar", { name: "Checking out" })).toHaveClass("is-indeterminate");
  });

  it("uses an indeterminate bar when the registry omits a total", () => {
    show(
      <StartupTracker
        capsule={capsule({
          phase: "pulling_image",
          phases: ["pulling_image"],
          pull: { currentBytes: 20_000_000 },
          updatedAt: timestamp,
        })}
      />,
    );
    const bar = screen.getByRole("progressbar", { name: "20 MB" });
    expect(bar).toHaveClass("is-indeterminate");
    expect(bar).not.toHaveAttribute("aria-valuenow");
    expect(bar.querySelector(".progress-fill")).not.toHaveAttribute("style");
  });

  it("respects reduced motion on the bar, dot, and skeleton", () => {
    mockMotion(true);
    show(
      <>
        <ProgressBar label="Downloading" />
        <ActivityDot />
        <Skeleton />
      </>,
    );
    expect(screen.getByRole("progressbar")).toHaveAttribute("data-reduced-motion", "true");
    expect(screen.getByRole("progressbar")).toHaveClass("is-static");
    expect(screen.getByRole("progressbar")).not.toHaveClass("is-pulsing");
    expect(document.querySelector(".load-dot")).toHaveAttribute("data-reduced-motion", "true");
    expect(document.querySelector(".load-dot")).not.toHaveClass("is-pulsing");
    expect(document.querySelector(".skeleton")).toHaveClass("is-static");
  });

  it("animates when motion is allowed", () => {
    mockMotion(false);
    show(
      <>
        <ProgressBar label="Downloading" />
        <ActivityDot />
      </>,
    );
    expect(screen.getByRole("progressbar")).toHaveAttribute("data-reduced-motion", "false");
    expect(screen.getByRole("progressbar")).toHaveClass("is-indeterminate");
    expect(screen.getByRole("progressbar")).not.toHaveClass("is-static");
    expect(document.querySelector(".load-dot")).toHaveClass("is-pulsing");
  });

  it("shows the failed pull step, the specific reason, and retry", async () => {
    const retry = vi.spyOn(api, "lifecycle").mockResolvedValue(
      capsule(
        {
          phase: "pulling_image",
          phases,
          updatedAt: timestamp,
        },
        { state: "Creating", failure: undefined, failureCode: undefined },
      ),
    );
    show(
      <StartupTracker
        capsule={capsule(
          { phase: "pulling_image", phases, updatedAt: timestamp },
          {
            state: "Failed",
            failureCode: "image_not_found",
            failure: "The Capsule image was not found.",
          },
        )}
      />,
    );
    expect(screen.getAllByRole("listitem")[0]).toHaveClass("is-failed");
    expect(screen.getAllByRole("listitem")[0]).toHaveTextContent("Pulling image");
    expect(screen.getByRole("alert")).toHaveTextContent("The Capsule image was not found.");
    expect(screen.getByRole("heading", { name: "Image not found" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalledWith("retry", "capsule-1", 3);
  });
});
