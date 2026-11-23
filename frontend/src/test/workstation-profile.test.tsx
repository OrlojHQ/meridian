import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";

import { api, normalizeAPIError } from "../api/client";
import type { Profile, ProfileDiagnostic } from "../api/generated/types.gen";
import { CapsuleProfilePanel } from "../components/CapsuleProfilePanel";
import { FirstRun, WhatCapsulesInherit } from "../components/WorkstationProfile";

const at = "2026-11-14T00:00:00Z";
const setup = { id: "setup", harness: "codex", name: "My codex setup", revision: "rev-2", default: true, deleted: false, createdAt: at, resourceVersion: 2 };
const empty: Profile = { gitName: "", gitEmail: "", capsuleGitHubAccess: true, defaultConnections: {}, setups: [], resourceVersion: 0 };
const saved: Profile = { gitName: "Ada Lovelace", gitEmail: "ada@example.com", capsuleGitHubAccess: true, defaultConnections: {}, setups: [setup], resourceVersion: 3, updatedAt: at };
const connection = { id: "conn", provider: "openai" as const, name: "My OpenAI", createdAt: at, resourceVersion: 1, revoked: false };

function show(ui: React.ReactNode) {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  delete window.__MERIDIAN_DESKTOP__;
});

it("points a browser at meridian import and never claims to have found anything", async () => {
  vi.spyOn(api, "profile").mockResolvedValue(empty);
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: true });
  show(<FirstRun openNewProject={() => undefined} />);
  expect(await screen.findByText("meridian import")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "choose a folder" })).toHaveAttribute("href", "/ui/settings/harnesses#import-setup");
  expect(screen.queryByText(/We found/)).toBeNull();
  expect(screen.getByRole("link", { name: "Connect a provider" })).toBeInTheDocument();
});

it("shows what the profile holds once it is saved", async () => {
  vi.spyOn(api, "profile").mockResolvedValue(saved);
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [connection], enabled: true });
  show(<FirstRun />);
  expect(await screen.findByText("Your profile has Codex.")).toBeInTheDocument();
  expect(screen.getByText("A provider connection is ready.")).toBeInTheDocument();
});

it("asks the desktop app to open its own import window, with no arguments", async () => {
  const openImport = vi.fn().mockResolvedValue(true);
  window.__MERIDIAN_DESKTOP__ = { notify: vi.fn(), setAttention: vi.fn(), openImport };
  vi.spyOn(api, "profile").mockResolvedValue(empty);
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: true });
  show(<FirstRun />);
  await userEvent.click(await screen.findByRole("button", { name: "Review this computer's setup" }));
  expect(openImport).toHaveBeenCalledTimes(1);
  expect(openImport).toHaveBeenCalledWith();
  expect(await screen.findByText("Review and save in the window that opened.")).toBeInTheDocument();
  expect(screen.queryByText("meridian import")).toBeNull();
  expect(screen.queryByText(/We found/)).toBeNull();
});

it("edits the default connection and git identity with the profile's version", async () => {
  vi.spyOn(api, "profile").mockResolvedValue(saved);
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [connection], enabled: true });
  vi.spyOn(api, "projects").mockResolvedValue({ items: [] });
  vi.spyOn(api, "authenticationMode").mockResolvedValue({ mode: "single_user", signIn: false, claimable: false } as never);
  const update = vi.spyOn(api, "updateProfile").mockImplementation(async (body) => ({ ...saved, ...body, resourceVersion: 4 }) as Profile);
  show(<WhatCapsulesInherit />);
  expect(await screen.findByText(/code that runs inside the Capsule/)).toBeInTheDocument();
  await userEvent.selectOptions(screen.getByLabelText("Default connection for Codex"), "conn");
  await waitFor(() =>
    expect(update).toHaveBeenCalledWith({
      gitName: "Ada Lovelace", gitEmail: "ada@example.com", capsuleGitHubAccess: true, defaultConnections: { codex: "conn" }, expectedResourceVersion: 3,
    }),
  );
  const name = screen.getByLabelText("Name");
  await userEvent.clear(name);
  await userEvent.type(name, "Ada King");
  await userEvent.click(screen.getByRole("button", { name: "Save identity" }));
  await waitFor(() => expect(update).toHaveBeenLastCalledWith(expect.objectContaining({ gitName: "Ada King", expectedResourceVersion: 4 })));
  await userEvent.click(screen.getByRole("checkbox", { name: "Give Capsules access to my GitHub account" }));
  await waitFor(() => expect(update).toHaveBeenLastCalledWith(expect.objectContaining({ capsuleGitHubAccess: false, expectedResourceVersion: 4 })));
});

const behind: ProfileDiagnostic = {
  capsuleId: "capsule",
  nothingPinned: false,
  matchesProfile: false,
  harnesses: [
    {
      harness: "codex",
      pinned: { setupId: "setup", setupName: "My codex setup", revision: "rev-1", savedAt: "2026-11-10T09:00:00Z" },
      current: { setupId: "setup", setupName: "My codex setup", revision: "rev-2", savedAt: "2026-11-12T09:00:00Z" },
      projectOverride: "",
      filesAdded: [".codex/prompts/review.md"],
      filesRemoved: [],
      hasReport: true,
      exclusions: [{ path: ".codex/auth.json", reason: "Authentication, runtime state, or unsupported file excluded" }],
      warnings: [{ path: ".codex/config.toml:mcp_servers:docs", reason: "Absolute path kept" }],
      connection: { granted: false, revoked: false, needsSignIn: false },
    },
  ],
  commitAuthor: { source: "capsule", name: "Ada Lovelace", email: "ada@example.com", profileName: "Ada King", profileEmail: "ada@example.com" },
};

it("explains the difference from the profile with control-plane facts only", async () => {
  vi.spyOn(api, "capsuleProfileDiagnostic").mockResolvedValue(behind);
  const update = vi.spyOn(api, "updateCapsuleProfile").mockRejectedValue(
    normalizeAPIError({ error: { code: "conflict", message: "stop the running session before updating this Capsule" } }),
  );
  show(<CapsuleProfilePanel capsuleId="capsule" />);
  expect(await screen.findByText("This Capsule differs from your current profile:")).toBeInTheDocument();
  expect(screen.getByText(/Your profile has .codex\/prompts\/review.md; this Capsule does not./)).toBeInTheDocument();
  expect(screen.getByText(".codex/auth.json")).toBeInTheDocument();
  expect(screen.getByText(/this Project has no connection for Codex/)).toBeInTheDocument();
  expect(screen.getByText(/Your profile now says Ada King/)).toBeInTheDocument();
  expect(screen.queryByText(/matches your laptop/i)).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "Update this Capsule to my current profile" }));
  expect(update).toHaveBeenCalledWith("capsule");
  expect(await screen.findByRole("alert")).toHaveTextContent(/stop the running session/);
});

it("says a matching Capsule matches the profile, not the laptop", async () => {
  vi.spyOn(api, "capsuleProfileDiagnostic").mockResolvedValue({ ...behind, matchesProfile: true, harnesses: [] });
  show(<CapsuleProfilePanel capsuleId="capsule" />);
  expect(await screen.findByText("This Capsule matches your current profile, with nothing left out.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Update this Capsule to my current profile" })).toBeNull();
});

it("drops the window-opened hint once the import is saved", async () => {
  const openImport = vi.fn().mockResolvedValue(true);
  window.__MERIDIAN_DESKTOP__ = { notify: vi.fn(), setAttention: vi.fn(), openImport };
  const profile = vi.spyOn(api, "profile").mockResolvedValue(empty);
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: true });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <FirstRun />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  await userEvent.click(await screen.findByRole("button", { name: "Review this computer's setup" }));
  expect(await screen.findByText("Review and save in the window that opened.")).toBeInTheDocument();
  profile.mockResolvedValue({ ...saved, resourceVersion: 1 });
  await client.invalidateQueries({ queryKey: ["profile"] });
  expect(await screen.findByText("Your profile has Codex.")).toBeInTheDocument();
  expect(screen.queryByText("Review and save in the window that opened.")).toBeNull();
  expect(screen.getByRole("link", { name: "Connect a provider" }).closest("p")?.textContent).toBe("Connect a provider");
});
