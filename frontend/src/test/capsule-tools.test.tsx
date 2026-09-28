import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import type {
  Capabilities,
  Capsule,
  WorkspaceFileEntry,
} from "../api/generated/types.gen";
import { WorkspaceContext } from "../shell/WorkspaceContext";

const capsule: Capsule = {
  id: "capsule-1",
  projectId: "project-1",
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
};

const entry = (
  name: string,
  type: WorkspaceFileEntry["type"],
  size = 12,
): WorkspaceFileEntry => ({ name, type, size, executable: false });

const listings: Record<string, WorkspaceFileEntry[]> = {
  "": [
    entry("README.md", "file"),
    entry("src", "directory"),
    entry("link", "symlink"),
    entry("big.bin", "file", 2 * 1_048_576),
    entry("docs", "directory"),
  ],
  src: [entry("main.ts", "file"), entry("lib", "directory")],
  "src/lib": [entry("util.ts", "file")],
  docs: [],
};

function renderTools(
  overrides: Partial<Capabilities>,
  pathname = `/ui/capsules/${capsule.id}`,
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[pathname]}>
        <WorkspaceContext
          capsuleId={capsule.id}
          pathname={pathname}
          capabilities={{ ...capabilities, ...overrides }}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const listedPaths = (spy: { mock: { calls: unknown[][] } }) =>
  spy.mock.calls.map((call) => call[1]);

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Files tree", () => {
  it("lists a directory only when it is expanded and opens files with the keyboard", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    const files = vi
      .spyOn(api, "workspaceFiles")
      .mockImplementation(async (_capsuleId, path = "") => ({
        items: listings[path] ?? [],
      }));
    const file = vi
      .spyOn(api, "workspaceFile")
      .mockImplementation(async (_capsuleId, path) => ({
        path,
        content: btoa("export const answer = 42;\n"),
        size: 26,
        executable: false,
      }));
    const user = userEvent.setup();
    renderTools({ browse: true });

    await user.click(await screen.findByRole("tab", { name: "Files" }));
    const tree = await screen.findByRole("tree", { name: "Workspace files" });
    const names = within(tree)
      .getAllByRole("treeitem")
      .map((item) => item.querySelector(".file-tree-name")?.textContent);
    expect(names).toEqual(["src", "docs", "README.md", "link", "big.bin"]);
    expect(listedPaths(files)).toEqual([""]);
    const src = screen.getByRole("treeitem", { name: /^src/ });
    expect(src).toHaveAttribute("tabindex", "0");

    const link = screen.getByRole("treeitem", { name: /^link/ });
    expect(link).toHaveAttribute("aria-disabled", "true");
    expect(link).toHaveTextContent("Symlink, unsupported");
    const big = screen.getByRole("treeitem", { name: /^big\.bin/ });
    expect(big).toHaveAttribute("aria-disabled", "true");
    expect(big).toHaveTextContent("Too large");
    await user.click(big);
    expect(file).not.toHaveBeenCalled();
    expect(big).toHaveAttribute("tabindex", "0");
    expect(src).toHaveAttribute("tabindex", "-1");

    expect(src).toHaveAttribute("aria-expanded", "false");
    act(() => src.focus());

    await user.keyboard("{ArrowRight}");
    expect(src).toHaveAttribute("aria-expanded", "true");
    const lib = await screen.findByRole("treeitem", { name: /^lib/ });
    expect(lib).toHaveAttribute("aria-level", "2");
    expect(screen.getByRole("treeitem", { name: /^main\.ts/ })).toBeInTheDocument();
    expect(listedPaths(files)).toEqual(["", "src"]);

    await user.keyboard("{ArrowRight}");
    expect(lib).toHaveFocus();
    expect(listedPaths(files)).toEqual(["", "src"]);

    await user.keyboard("{ArrowDown}");
    const main = screen.getByRole("treeitem", { name: /^main\.ts/ });
    expect(main).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(
      await screen.findByLabelText("Contents of src/main.ts"),
    ).toHaveTextContent("export const answer = 42;");
    expect(file).toHaveBeenCalledWith(capsule.id, "src/main.ts", expect.anything());
    expect(main).toHaveAttribute("aria-selected", "true");

    await user.keyboard("{ArrowLeft}");
    expect(src).toHaveFocus();
    await user.keyboard("{ArrowLeft}");
    expect(src).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("treeitem", { name: /^main\.ts/ })).not.toBeInTheDocument();

    await user.keyboard("{End}");
    expect(big).toHaveFocus();
    await user.keyboard("{Home}");
    expect(src).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    const docs = screen.getByRole("treeitem", { name: /^docs/ });
    expect(docs).toHaveFocus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(docs).toHaveTextContent("Empty"));
    expect(listedPaths(files)).toEqual(["", "src", "docs"]);
  });

  it("does not browse a Capsule that is not Ready", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue({ ...capsule, state: "Paused" });
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    const files = vi.spyOn(api, "workspaceFiles");
    const user = userEvent.setup();
    renderTools({ browse: true });

    await user.click(await screen.findByRole("tab", { name: "Files" }));
    expect(
      screen.getByText("Files are available while the Capsule is Ready."),
    ).toBeInTheDocument();
    expect(files).not.toHaveBeenCalled();
  });
});

const diff = [
  "diff --git a/src/a.ts b/src/a.ts",
  "--- a/src/a.ts",
  "+++ b/src/a.ts",
  "@@ -1,2 +1,3 @@",
  " keep",
  "-old",
  "+new",
  "+added",
  "diff --git a/src/b.ts b/src/b.ts",
  "new file mode 100644",
  "--- /dev/null",
  "+++ b/src/b.ts",
  "@@ -0,0 +1 @@",
  "+created",
  "",
].join("\n");

describe("Change counts", () => {
  it("labels the Changes tab and header from the loaded diff without extra requests", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    const gitDiff = vi
      .spyOn(api, "gitDiff")
      .mockResolvedValue({ content: diff, truncated: false });
    const gitStatus = vi.spyOn(api, "gitStatus");
    const files = vi.spyOn(api, "workspaceFiles");
    const user = userEvent.setup();
    renderTools({ git: true });

    const tab = await screen.findByRole("tab", {
      name: "Changes, 2 files changed, 3 additions, 1 deletion",
    });
    expect(within(tab).getByText("2")).toBeInTheDocument();
    expect(tab).toHaveTextContent("+3−1");
    const panel = screen.getByRole("tabpanel", { name: "Changes" });
    expect(panel.querySelector(".diff-summary")).toHaveTextContent(
      "2 files changed+3−1",
    );

    await user.click(screen.getByRole("tab", { name: "Preview" }));
    expect(
      screen.getByRole("tab", { name: /^Changes, 2 files changed/ }),
    ).toBeInTheDocument();
    expect(gitDiff).toHaveBeenCalledTimes(1);
    expect(gitStatus).not.toHaveBeenCalled();
    expect(files).not.toHaveBeenCalled();
  });

  it("marks a truncated diff and hides counts for a clean tree", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    const gitDiff = vi
      .spyOn(api, "gitDiff")
      .mockResolvedValue({ content: diff, truncated: true });
    const view = renderTools({ git: true });

    const tab = await screen.findByRole("tab", {
      name: "Changes, 2 files changed, 3 additions, 1 deletion (diff truncated)",
    });
    expect(tab).toHaveTextContent("2+");
    view.unmount();

    gitDiff.mockResolvedValue({ content: "", truncated: false });
    renderTools({ git: true });
    expect(await screen.findByText("Working tree clean")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Changes" })).toBeInTheDocument();
  });

  it("shows no counts until the Changes tool has loaded the diff", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    const gitDiff = vi.spyOn(api, "gitDiff");
    renderTools({ git: true }, "/ui/runs/run-1");

    expect(await screen.findByRole("tab", { name: "Activity" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByRole("tab", { name: "Changes" })).not.toHaveTextContent(/\d/);
    expect(gitDiff).not.toHaveBeenCalled();
  });

  it("shows no counts while the diff is still loading", async () => {
    vi.spyOn(api, "capsule").mockResolvedValue(capsule);
    vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
    vi.spyOn(api, "gitDiff").mockImplementation(() => new Promise(() => {}));
    renderTools({ git: true });

    expect(await screen.findByText("Loading changes…")).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Changes" })).not.toHaveTextContent(/\d/);
  });
});
