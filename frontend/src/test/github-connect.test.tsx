import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "../api/client";
import { GitHubConnect, GitHubReconnectBanner } from "../components/GitHubConnect";

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
});

it("confirms the host GitHub login in one step", async () => {
  vi.spyOn(api, "githubConnection").mockResolvedValue({ connected: false, deviceFlow: true });
  vi.spyOn(api, "hostGitHubLogin").mockResolvedValue({
    available: true,
    state: "signed_in",
    login: "octocat",
    path: "/opt/homebrew/bin/gh",
    detail: "The GitHub CLI at /opt/homebrew/bin/gh is signed in as @octocat.",
  });
  const useHost = vi.spyOn(api, "useHostGitHub").mockResolvedValue({
    connected: true,
    deviceFlow: true,
    login: "octocat",
    status: "active",
    resourceVersion: 1,
  });
  show(<GitHubConnect />);
  expect(await screen.findByText("The GitHub CLI at /opt/homebrew/bin/gh is signed in as @octocat.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Connect with a device code" })).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Use your GitHub CLI login (@octocat)" }));
  expect(useHost).toHaveBeenCalledWith(0);
});

it("names a GitHub CLI that is installed but not signed in", async () => {
  vi.spyOn(api, "githubConnection").mockResolvedValue({ connected: false, deviceFlow: true });
  vi.spyOn(api, "hostGitHubLogin").mockResolvedValue({
    available: false,
    state: "not_signed_in",
    path: "/opt/homebrew/bin/gh",
    detail: "The GitHub CLI at /opt/homebrew/bin/gh is not signed in. Run `gh auth login` on this computer, then connect again.",
  });
  const begin = vi.spyOn(api, "beginGitHubDevice").mockResolvedValue({
    userCode: "ABCD-EFGH",
    verificationUri: "https://github.com/login/device",
    expiresAt: "2026-11-16T00:15:00Z",
    intervalSeconds: 60,
  });
  show(<GitHubConnect />);
  expect(await screen.findByText(/\/opt\/homebrew\/bin\/gh is not signed in/)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Connect with a device code" }));
  expect(await screen.findByLabelText("GitHub device code")).toHaveTextContent("ABCD-EFGH");
  expect(screen.getByRole("link", { name: "Open github.com/login/device" })).toHaveAttribute(
    "href",
    "https://github.com/login/device",
  );
  expect(begin).toHaveBeenCalledWith(0);
});

it("names the paths searched when the GitHub CLI is missing and hides a disabled device code", async () => {
  vi.spyOn(api, "githubConnection").mockResolvedValue({ connected: false, deviceFlow: false });
  vi.spyOn(api, "hostGitHubLogin").mockResolvedValue({
    available: false,
    state: "not_found",
    searched: ["PATH", "/opt/homebrew/bin/gh", "/usr/local/bin/gh"],
    detail: "The GitHub CLI was not found. Searched PATH, /opt/homebrew/bin/gh, /usr/local/bin/gh.",
  });
  show(<GitHubConnect />);
  expect(await screen.findByText(/Searched PATH, \/opt\/homebrew\/bin\/gh, \/usr\/local\/bin\/gh/)).toBeInTheDocument();
  expect(screen.getByText(/no GitHub OAuth client id/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Connect with a device code" })).not.toBeInTheDocument();
});

it("says when the GitHub CLI token cannot be read", async () => {
  vi.spyOn(api, "githubConnection").mockResolvedValue({ connected: false, deviceFlow: false });
  vi.spyOn(api, "hostGitHubLogin").mockResolvedValue({
    available: false,
    state: "token_unreadable",
    login: "octocat",
    path: "/opt/homebrew/bin/gh",
    detail:
      "The GitHub CLI at /opt/homebrew/bin/gh is signed in as @octocat, but its token could not be read. Run `meridian github connect` in a terminal on this computer.",
  });
  show(<GitHubConnect />);
  expect(await screen.findByText(/its token could not be read/)).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Use your GitHub CLI login (@octocat)" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Connect with a device code" })).not.toBeInTheDocument();
});

it("offers the host login again when a connection needs reconnect", async () => {
  vi.spyOn(api, "githubConnection").mockResolvedValue({
    connected: true,
    deviceFlow: true,
    login: "octocat",
    status: "needs_reconnect",
    reason: "expired",
    resourceVersion: 4,
  });
  vi.spyOn(api, "hostGitHubLogin").mockResolvedValue({
    available: true,
    state: "signed_in",
    login: "octocat",
    path: "/usr/bin/gh",
  });
  show(<GitHubConnect />);
  expect(await screen.findByRole("button", { name: "Use your GitHub CLI login (@octocat)" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Connect with a device code" })).not.toBeInTheDocument();
});

it("shows a reconnect banner when the login needs attention", async () => {
  vi.spyOn(api, "githubConnection").mockResolvedValue({
    connected: true,
    deviceFlow: true,
    login: "octocat",
    status: "needs_reconnect",
    reason: "expired",
    resourceVersion: 3,
  });
  vi.spyOn(api, "checkGitHubConnection").mockResolvedValue({
    connected: true,
    deviceFlow: true,
    login: "octocat",
    status: "needs_reconnect",
    reason: "expired",
    resourceVersion: 3,
  });
  show(<GitHubReconnectBanner />);
  expect(await screen.findByText(/The GitHub login expired/)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Reconnect GitHub" })).toHaveAttribute("href", "/ui/settings/account#github");
});
