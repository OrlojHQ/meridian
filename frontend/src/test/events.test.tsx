import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";

import { EventActivity, RunDetail } from "../App";
import { api } from "../api/client";

vi.mock("../api/events", () => ({
  followRunEvents: vi.fn(
    async (
      _runId: string,
      _after: number,
      signal: AbortSignal,
      onEvent: (event: unknown) => void,
      onStatus: (status: string) => void,
    ) => {
      onStatus("reconnecting");
      onEvent({
        sequence: 3,
        type: "completed",
        timestamp: "2026-08-23T00:00:03Z",
      });
      await new Promise<void>((resolve) =>
        signal.addEventListener("abort", () => resolve(), { once: true }),
      );
    },
  ),
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("orders streamed events and surfaces reconnect gaps", async () => {
  vi.spyOn(api, "events").mockResolvedValue({
    items: [
      {
        sequence: 1,
        type: "started",
        timestamp: "2026-08-23T00:00:01Z",
      },
    ],
    nextCursor: 1,
    more: false,
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <EventActivity runId="run-1" />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  expect(await screen.findByText(/Replay gap: expected sequence 2, received 3/)).toBeInTheDocument();
  const events = screen.getAllByRole("listitem");
  expect(events[0]).toHaveTextContent("#1");
  expect(events[1]).toHaveTextContent("#3");
  expect(screen.getByRole("status")).toHaveTextContent("reconnecting");
});

it("returns from a Run inspector to its Capsule workspace", async () => {
  vi.spyOn(api, "run").mockResolvedValue({
    id: "run-1",
    capsuleId: "capsule-1",
    harness: "opencode",
    state: "Failed",
    exitStatus: -1,
    failure: "run timed out",
    createdAt: "2026-08-23T00:00:00Z",
    startedAt: "2026-08-23T00:00:01Z",
    finishedAt: "2026-08-23T02:00:01Z",
    updatedAt: "2026-08-23T02:00:01Z",
    resourceVersion: 5,
  });
  vi.spyOn(api, "capabilities").mockResolvedValue({
    providerVersion: "fake/v1",
    attach: true,
    run: true,
    structured: false,
    git: false,
    pause: false,
    snapshot: false,
    clone: false,
    preview: false,
    browse: false,
    delivery: false,
    resourceMetrics: false,
  });
  vi.spyOn(api, "events").mockResolvedValue({
    items: [],
    nextCursor: 0,
    more: false,
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/ui/runs/run-1"]}>
        <Routes>
          <Route path="/ui/runs/:runId" element={<RunDetail />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );

  expect(
    await screen.findByRole("link", { name: "Back to workspace" }),
  ).toHaveAttribute("href", "/ui/capsules/capsule-1");
});
