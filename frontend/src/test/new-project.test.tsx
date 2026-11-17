import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type { HarnessImage, Project } from "../api/generated/types.gen";
import { NewProjectDialog } from "../components/NewProjectDialog";
import { isGitHubRepositoryInput } from "../components/repositoryUrl";

// Same order as domain.InstallationHarnessImages.
const catalog: HarnessImage[] = [
  { name: "opencode", imageReference: "ghcr.io/orlojhq/meridian-capsule-opencode:v0.1.1" },
  { name: "pi", imageReference: "ghcr.io/orlojhq/meridian-capsule-pi:v0.1.1" },
  { name: "claude", imageReference: "ghcr.io/orlojhq/meridian-capsule-claude:v0.1.1" },
  { name: "codex", imageReference: "ghcr.io/orlojhq/meridian-capsule-codex:v0.1.1" },
  { name: "dsh", imageReference: "ghcr.io/orlojhq/meridian-capsule-dsh:v0.1.1" },
];

const preferenceKey = "meridian.newProjectHarness";

const project: Project = {
  id: "project-1",
  name: "Meridian",
  createdAt: "2026-09-27T00:00:00Z",
  updatedAt: "2026-09-27T00:00:00Z",
  resourceVersion: 1,
};

function renderDialog(harnessImages = catalog) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const onClose = vi.fn();
  const view = render(
    <QueryClientProvider client={client}>
      <NewProjectDialog open harnessImages={harnessImages} onClose={onClose} />
    </QueryClientProvider>,
  );
  return { ...view, onClose };
}

const harnessSelect = () => screen.getByLabelText(/Initial harness pack/);

describe("NewProjectDialog", () => {
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    localStorage.clear();
  });

  function disconnected() {
    vi.spyOn(api, "githubConnection").mockResolvedValue({ connected: false, deviceFlow: true });
  }

  it("preselects the first installation pack", () => {
    disconnected();
    renderDialog();
    expect(harnessSelect()).toHaveValue("opencode");
    expect(screen.getByRole("option", { name: "opencode" })).toHaveValue("opencode");
    expect(screen.getByRole("option", { name: "claude" })).toHaveValue("claude");
    expect(screen.queryByRole("option", { name: "mock" })).not.toBeInTheDocument();
  });

  it("preselects the harness remembered from the last Project", () => {
    disconnected();
    localStorage.setItem(preferenceKey, "claude");
    renderDialog();
    expect(harnessSelect()).toHaveValue("claude");
  });

  it("falls back to catalog order when the remembered harness is gone", () => {
    disconnected();
    localStorage.setItem(preferenceKey, "retired-agent");
    renderDialog();
    expect(harnessSelect()).toHaveValue("opencode");
  });

  it("remembers only the harness name after a successful create", async () => {
    disconnected();
    const create = vi.spyOn(api, "createProject").mockResolvedValue(project);
    const user = userEvent.setup();
    const { onClose } = renderDialog();

    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.selectOptions(harnessSelect(), "codex");
    await user.click(screen.getByRole("button", { name: "Create Project" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(create).toHaveBeenCalledWith({
      name: "Meridian",
      harnessImages: [catalog[3]],
    });
    expect(localStorage.getItem(preferenceKey)).toBe("codex");
  });

  it("asks to connect GitHub when the repository cannot be read", async () => {
    disconnected();
    vi.spyOn(api, "hostGitHubLogin").mockResolvedValue({
      available: true,
      state: "signed_in",
      login: "octocat",
      path: "/usr/bin/gh",
    });
    vi.spyOn(api, "createProject").mockRejectedValue({
      error: {
        code: "repository_access",
        message: "Meridian couldn't access octocat/hidden. It may be private.",
      },
    });
    const user = userEvent.setup();
    renderDialog();
    const repository = screen.getByLabelText(/^Repository/);
    await user.type(repository, "https://github.com/octocat/hidden");
    await user.type(screen.getByLabelText("Project name"), "Hidden");
    await user.click(screen.getByRole("button", { name: "Create Project" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Meridian couldn't access octocat/hidden. It may be private.",
    );
    const connects = screen.getAllByRole("button", { name: "Connect GitHub" });
    expect(connects.length).toBeGreaterThan(0);
    expect(screen.queryByRole("link", { name: "Connect GitHub" })).not.toBeInTheDocument();
    await user.click(connects[0]);
    expect(await screen.findByRole("button", { name: "Use your GitHub CLI login (@octocat)" })).toBeInTheDocument();
    expect(repository).toHaveValue("https://github.com/octocat/hidden");
    expect(screen.getByLabelText("Project name")).toHaveValue("Hidden");
    expect(screen.getByRole("button", { name: "Add a token" })).toBeInTheDocument();
    expect(screen.queryByText(/internal server error/i)).not.toBeInTheDocument();
  });

  it("keeps the Project form after GitHub connects", async () => {
    let active = false;
    vi.spyOn(api, "githubConnection").mockImplementation(async () =>
      active
        ? { connected: true, deviceFlow: false, login: "octocat", status: "active", resourceVersion: 1 }
        : { connected: false, deviceFlow: false },
    );
    vi.spyOn(api, "hostGitHubLogin").mockResolvedValue({
      available: true,
      state: "signed_in",
      login: "octocat",
      path: "/usr/bin/gh",
    });
    vi.spyOn(api, "useHostGitHub").mockImplementation(async () => {
      active = true;
      return { connected: true, deviceFlow: false, login: "octocat", status: "active", resourceVersion: 1 };
    });
    vi.spyOn(api, "createProject").mockRejectedValue({
      error: {
        code: "repository_access",
        message: "Meridian couldn't access octocat/hidden. It may be private.",
      },
    });
    const user = userEvent.setup();
    renderDialog();
    await user.type(screen.getByLabelText(/^Repository/), "https://github.com/octocat/hidden");
    await user.type(screen.getByLabelText("Project name"), "Hidden");
    await user.click(screen.getByRole("button", { name: "Create Project" }));
    const connects = await screen.findAllByRole("button", { name: "Connect GitHub" });
    await user.click(connects.at(-1)!);
    await user.click(await screen.findByRole("button", { name: "Use your GitHub CLI login (@octocat)" }));
    expect(await screen.findByText("GitHub is connected. Create the Project again.")).toBeInTheDocument();
    expect(screen.getByLabelText(/^Repository/)).toHaveValue("https://github.com/octocat/hidden");
    expect(screen.getByLabelText("Project name")).toHaveValue("Hidden");
    expect(screen.getByRole("button", { name: "Create Project" })).toBeEnabled();
  });

  it("does not remember a choice when creation fails", async () => {
    disconnected();
    vi.spyOn(api, "createProject").mockRejectedValue(new Error("conflict"));
    const user = userEvent.setup();
    renderDialog();

    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.selectOptions(harnessSelect(), "pi");
    await user.click(screen.getByRole("button", { name: "Create Project" }));

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(localStorage.getItem(preferenceKey)).toBeNull();
  });

  it("works when browser storage throws", async () => {
    disconnected();
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    const create = vi.spyOn(api, "createProject").mockResolvedValue(project);
    const user = userEvent.setup();
    const { onClose } = renderDialog();

    expect(harnessSelect()).toHaveValue("opencode");
    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.click(screen.getByRole("button", { name: "Create Project" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(create).toHaveBeenCalledWith({
      name: "Meridian",
      harnessImages: [catalog[0]],
    });
    expect(setItem).toHaveBeenCalledWith(preferenceKey, "opencode");
  });

  it("recognizes the GitHub forms the server stores as HTTPS", () => {
    expect(isGitHubRepositoryInput("OrlojHQ/meridian")).toBe(true);
    expect(isGitHubRepositoryInput("git@github.com:OrlojHQ/meridian.git")).toBe(true);
    expect(isGitHubRepositoryInput("ssh://git@github.com/OrlojHQ/meridian.git")).toBe(true);
    expect(isGitHubRepositoryInput("https://github.com/OrlojHQ/meridian")).toBe(true);
    expect(isGitHubRepositoryInput("github.com/OrlojHQ/meridian")).toBe(true);
    expect(isGitHubRepositoryInput("https://gitlab.com/OrlojHQ/meridian.git")).toBe(false);
    expect(isGitHubRepositoryInput("ssh://git@gitlab.com/OrlojHQ/meridian.git")).toBe(false);
    expect(isGitHubRepositoryInput("/tmp/fixture")).toBe(false);
    expect(isGitHubRepositoryInput("https://user:token@github.com/OrlojHQ/meridian")).toBe(false);
  });

  it("sends owner/repo and a clone token when the repository is private", async () => {
    disconnected();
    const create = vi.spyOn(api, "createProject").mockResolvedValue(project);
    const secret = {
      id: "secret-1",
      name: "git_token",
      purpose: "git_https" as const,
      createdAt: project.createdAt,
      updatedAt: project.updatedAt,
      resourceVersion: 4,
    };
    const put = vi.spyOn(api, "putGitHTTPSSecret").mockResolvedValue(secret);
    const remove = vi.spyOn(api, "deleteSecret");
    const user = userEvent.setup();
    const { onClose } = renderDialog();

    expect(screen.queryByRole("checkbox", { name: "Private repository" })).not.toBeInTheDocument();
    await user.type(screen.getByLabelText(/^Repository/), "OrlojHQ/meridian");
    expect(screen.getByText("Stored as HTTPS.")).toBeInTheDocument();
    expect(screen.queryByLabelText("GitHub token")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Use a token instead" }));
    await user.click(screen.getByRole("checkbox", { name: "Private repository" }));
    await user.type(screen.getByLabelText("GitHub token"), "ghp_exampletoken");
    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.click(screen.getByRole("button", { name: "Create Project" }));

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(put).toHaveBeenCalledWith(
      expect.stringMatching(/^git_[0-9a-f]{32}$/),
      "ghp_exampletoken",
    );
    expect(create).toHaveBeenCalledWith({
      name: "Meridian",
      repositoryUrl: "OrlojHQ/meridian",
      gitSecretName: put.mock.calls[0]?.[0],
      harnessImages: [catalog[0]],
    });
    expect(JSON.stringify(create.mock.calls)).not.toContain("ghp_exampletoken");
    expect(remove).not.toHaveBeenCalled();
  });

  it("sends a GitHub SSH URL and skips the token until the repository is private", async () => {
    disconnected();
    const create = vi.spyOn(api, "createProject").mockResolvedValue(project);
    const put = vi.spyOn(api, "putGitHTTPSSecret");
    const user = userEvent.setup();
    renderDialog();

    await user.type(
      screen.getByLabelText(/^Repository/),
      "git@github.com:OrlojHQ/meridian.git",
    );
    expect(screen.getByText("Stored as HTTPS.")).toBeInTheDocument();
    expect(screen.queryByRole("checkbox", { name: "Private repository" })).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.click(screen.getByRole("button", { name: "Create Project" }));

    await waitFor(() => expect(create).toHaveBeenCalledWith({
      name: "Meridian",
      repositoryUrl: "git@github.com:OrlojHQ/meridian.git",
      harnessImages: [catalog[0]],
    }));
    expect(put).not.toHaveBeenCalled();
  });

  it("drops the clone token when creating the Project fails", async () => {
    disconnected();
    vi.spyOn(api, "createProject").mockRejectedValue(new Error("conflict"));
    const secret = {
      id: "secret-1",
      name: "git_token",
      purpose: "git_https" as const,
      createdAt: project.createdAt,
      updatedAt: project.updatedAt,
      resourceVersion: 4,
    };
    vi.spyOn(api, "putGitHTTPSSecret").mockResolvedValue(secret);
    const remove = vi.spyOn(api, "deleteSecret").mockResolvedValue(secret);
    const user = userEvent.setup();
    renderDialog();

    await user.type(screen.getByLabelText(/^Repository/), "ssh://git@github.com/OrlojHQ/meridian.git");
    await user.click(screen.getByRole("button", { name: "Use a token instead" }));
    await user.click(screen.getByRole("checkbox", { name: "Private repository" }));
    await user.type(screen.getByLabelText("GitHub token"), "ghp_exampletoken");
    await user.type(screen.getByLabelText("Project name"), "Meridian");
    await user.click(screen.getByRole("button", { name: "Create Project" }));

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(remove).toHaveBeenCalledWith(expect.stringMatching(/^git_[0-9a-f]{32}$/), 4);
    expect(JSON.stringify(remove.mock.calls)).not.toContain("ghp_exampletoken");
  });

  it("fills a private repository from the picker without a token", async () => {
    vi.spyOn(api, "githubConnection").mockResolvedValue({
      connected: true,
      deviceFlow: true,
      login: "octocat",
      status: "active",
      resourceVersion: 2,
    });
    vi.spyOn(api, "searchGitHubRepos").mockResolvedValue({
      items: [{
        fullName: "octocat/hidden",
        private: true,
        description: "Secret notes",
        defaultBranch: "main",
        url: "https://github.com/octocat/hidden",
      }],
    });
    const create = vi.spyOn(api, "createProject").mockResolvedValue(project);
    const put = vi.spyOn(api, "putGitHTTPSSecret");
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByLabelText(/^Repository/));
    await user.click(await screen.findByRole("option", { name: /octocat\/hidden/ }));
    expect(screen.getByLabelText(/^Repository/)).toHaveValue("https://github.com/octocat/hidden");
    expect(screen.queryByLabelText("GitHub token")).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("Project name"), "Hidden");
    await user.click(screen.getByRole("button", { name: "Create Project" }));

    await waitFor(() => expect(create).toHaveBeenCalledWith(expect.objectContaining({
      name: "Hidden",
      repositoryUrl: "https://github.com/octocat/hidden",
    })));
    expect(put).not.toHaveBeenCalled();
    expect(JSON.stringify(create.mock.calls)).not.toContain("gitSecretName");
  });
});
