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

it("offers a discovered host login and a device code", async () => {
  vi.spyOn(api, "githubConnection").mockResolvedValue({ connected: false, deviceFlow: true });
  vi.spyOn(api, "hostGitHubLogin").mockResolvedValue({ available: true, login: "octocat" });
  const begin = vi.spyOn(api, "beginGitHubDevice").mockResolvedValue({
    userCode: "ABCD-EFGH",
    verificationUri: "https://github.com/login/device",
    expiresAt: "2026-11-16T00:15:00Z",
    intervalSeconds: 60,
  });
  show(<GitHubConnect />);
  expect(await screen.findByText("Found your GitHub login (@octocat). Use it?")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Connect with a device code" }));
  expect(await screen.findByLabelText("GitHub device code")).toHaveTextContent("ABCD-EFGH");
  expect(screen.getByRole("link", { name: "Open github.com/login/device" })).toHaveAttribute(
    "href",
    "https://github.com/login/device",
  );
  expect(begin).toHaveBeenCalledWith(0);
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
