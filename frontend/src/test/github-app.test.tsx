import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { GitHubApp } from "../api/generated/types.gen";
import { GitHubApp as GitHubAppSettings, GitHubAppNotice } from "../components/GitHubApp";

const missing: GitHubApp = {
  configured: false,
  canManage: true,
  resourceVersion: 0,
  webhook: false,
  installations: [],
};

const installed: GitHubApp = {
  configured: true,
  canManage: true,
  resourceVersion: 2,
  slug: "meridian",
  name: "Meridian",
  htmlUrl: "https://github.com/apps/meridian",
  ownerLogin: "octocat",
  webhook: false,
  installUrl: "https://github.com/apps/meridian/installations/new",
  installations: [
    { id: 5, accountLogin: "octocat", accountType: "User", repositorySelection: "all", suspended: false },
  ],
};

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

it("explains that Capsules have no GitHub token until an App is created", async () => {
  vi.spyOn(api, "githubApp").mockResolvedValue(missing);
  show(<GitHubAppSettings />);
  expect(await screen.findByText(/no GitHub App/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Create GitHub App" })).toBeInTheDocument();
});

it("posts the manifest to GitHub and does not show a key", async () => {
  vi.spyOn(api, "githubApp").mockResolvedValue(missing);
  const begin = vi.spyOn(api, "beginGitHubAppSetup").mockResolvedValue({
    actionUrl: "https://github.com/settings/apps/new",
    manifest: JSON.stringify({ name: "Meridian", default_permissions: { contents: "write" } }),
  });
  const submit = vi.spyOn(HTMLFormElement.prototype, "submit").mockImplementation(() => undefined);
  show(<GitHubAppSettings />);
  await userEvent.click(await screen.findByRole("button", { name: "Create GitHub App" }));
  expect(begin).toHaveBeenCalledWith("");
  expect(submit).toHaveBeenCalled();
  expect(screen.queryByText(/PRIVATE KEY/)).not.toBeInTheDocument();
});

it("says to install the App and choose all repositories", async () => {
  vi.spyOn(api, "githubApp").mockResolvedValue({ ...installed, installations: [] });
  vi.spyOn(api, "syncGitHubAppInstallations").mockResolvedValue({ ...installed, installations: [] });
  show(<GitHubAppSettings />);
  expect(await screen.findByText(/All repositories/)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Install on GitHub" })).toHaveAttribute("href", installed.installUrl);
});

it("names an installation that covers every repository", async () => {
  vi.spyOn(api, "githubApp").mockResolvedValue(installed);
  vi.spyOn(api, "syncGitHubAppInstallations").mockResolvedValue(installed);
  show(<GitHubAppSettings />);
  expect(await screen.findByRole("listitem")).toHaveTextContent("all repositories");
  expect(screen.getByText(/keeps polling checks/)).toBeInTheDocument();
});

it("tells a Project when the App is not installed on its repository", async () => {
  vi.spyOn(api, "githubAppAccess").mockResolvedValue({
    state: "not_installed",
    message: "The GitHub App is not installed on octocat/hello.",
    installUrl: installed.installUrl,
    appSlug: "meridian",
  });
  show(<GitHubAppNotice projectId="prj" />);
  const status = await screen.findByRole("status");
  expect(status).toHaveTextContent("octocat/hello");
  expect(screen.getByRole("link", { name: "GitHub App settings" })).toHaveAttribute("href", "/ui/settings/account#github-app");
  expect(screen.getByRole("link", { name: "Install on GitHub" })).toHaveAttribute("href", installed.installUrl);
});

it("stays quiet when the App covers the repository", async () => {
  const access = vi.spyOn(api, "githubAppAccess").mockResolvedValue({ state: "ready", appSlug: "meridian" });
  show(<GitHubAppNotice projectId="prj" />);
  await waitFor(() => expect(access).toHaveBeenCalled());
  expect(screen.queryByRole("status")).not.toBeInTheDocument();
});
