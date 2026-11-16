import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";

import { api } from "../api/client";
import type { Capsule, RemoteCiSummary } from "../api/generated/types.gen";
import { CapsuleChecks } from "../components/CapsuleChecks";

const capsule = (remoteCI: RemoteCiSummary): Capsule => ({
  id: "cap-1",
  projectId: "project-1",
  timelineId: "timeline-1",
  name: "notes",
  state: "Ready",
  desiredState: "Ready",
  restoreComplete: true,
  createdAt: "2026-11-16T00:00:00Z",
  updatedAt: "2026-11-16T00:00:00Z",
  resourceVersion: 1,
  checks: {
    configured: true,
    autoRework: true,
    phase: "passed",
    attempt: 1,
    maxAttempts: 3,
    remoteCI,
  },
});

function show(remoteCI: RemoteCiSummary) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <CapsuleChecks capsule={capsule(remoteCI)} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("remote CI on the Capsule", () => {
  it("shows waiting, a failure sent back, a pass, and a prompt to connect", async () => {
    vi.spyOn(api, "checkRuns").mockResolvedValue({ items: [] });
    vi.spyOn(api, "remoteCI").mockResolvedValue({
      state: "waiting",
      label: "Waiting on CI",
      checks: [{ name: "build", source: "check_run", conclusion: "pending" }],
    });
    show({ state: "waiting", label: "Waiting on CI" });
    expect(await screen.findByText(/Waiting on CI/)).toBeTruthy();
    expect(await screen.findByText(/build/)).toBeTruthy();
    cleanup();

    vi.spyOn(api, "remoteCI").mockResolvedValue({
      state: "failing",
      label: "CI failing (2/3). Review the fix before it can be pushed.",
      checks: [{ name: "build", source: "check_run", conclusion: "failure", output: "FAIL: expected greeting" }],
    });
    show({
      state: "failing",
      label: "CI failing (2/3). Review the fix before it can be pushed.",
      attempt: 2,
      maxAttempts: 3,
      pushReview: true,
    });
    expect(await screen.findByText(/Review the fix before it can be pushed/)).toBeTruthy();
    expect(await screen.findByText("FAIL: expected greeting")).toBeTruthy();
    cleanup();

    vi.spyOn(api, "remoteCI").mockResolvedValue({
      state: "passed",
      label: "CI passed",
      checks: [{ name: "build", source: "check_run", conclusion: "success" }],
    });
    show({ state: "passed", label: "CI passed" });
    expect(await screen.findByText(/CI passed/)).toBeTruthy();
    cleanup();

    vi.spyOn(api, "remoteCI").mockResolvedValue({ state: "connect", label: "Connect GitHub to read CI", checks: [] });
    show({ state: "connect", label: "Connect GitHub to read CI" });
    expect(await screen.findByRole("link", { name: "Connect GitHub" })).toHaveAttribute("href", "/ui/settings/account#github");
    expect(screen.getByText(/Local checks still run/)).toBeTruthy();
  });
});
