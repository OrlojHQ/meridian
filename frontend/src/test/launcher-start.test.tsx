import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api/client";
import type { ProviderConnection } from "../api/generated/types.gen";
import { NativeLauncher } from "../components/NativeLauncher";
import { StructuredLauncher } from "../components/StructuredLauncher";

const timestamp = "2026-09-06T00:00:00Z";
const project = {
  id: "project",
  name: "Project",
  createdAt: timestamp,
  updatedAt: timestamp,
  resourceVersion: 1,
  harnessImages: [{ name: "codex", imageReference: "example/codex:v1" }],
};
const setup = {
  id: "setup",
  harness: "codex",
  name: "My Codex setup",
  revision: "rev",
  default: true,
  deleted: false,
  createdAt: timestamp,
  resourceVersion: 1,
};
function connection(partial: Partial<ProviderConnection> & Pick<ProviderConnection, "id" | "provider" | "name">): ProviderConnection {
  return { createdAt: timestamp, resourceVersion: 1, revoked: false, ...partial };
}
function show(ui: React.ReactNode) {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}
function ready() {
  vi.spyOn(api, "projectHarnessSetup").mockResolvedValue({ setup: "" });
  vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
}
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("leads with the missing local settings and import when nothing is saved", async () => {
  ready();
  vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: false });
  show(<NativeLauncher project={project} harness="codex" />);
  expect(await screen.findByText("This Capsule will not have your local Codex settings.")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Import my setup" })).toBeEnabled();
  expect(screen.queryByText(/Using harness defaults/)).toBeNull();
  cleanup();
  show(<NativeLauncher project={{ ...project, harnessImages: [{ name: "claude", imageReference: "example/claude:v1" }] }} harness="claude" />);
  expect(await screen.findByText("This Capsule will not have your local Claude settings.")).toBeInTheDocument();
});

it("keeps the saved setup line when a setup is already saved", async () => {
  ready();
  vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [setup] });
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: false });
  show(<NativeLauncher project={project} harness="codex" />);
  expect(await screen.findByText(/Using My Codex setup/)).toBeInTheDocument();
  expect(screen.queryByText(/will not have your local/)).toBeNull();
  expect(screen.queryByRole("button", { name: "Import my setup" })).toBeNull();
});

it("preselects the only live connection instead of asking to sign in again", async () => {
  ready();
  vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [setup] });
  vi.spyOn(api, "providerConnections").mockResolvedValue({
    enabled: true,
    items: [
      connection({ id: "chatgpt", provider: "chatgpt", name: "ChatGPT" }),
      connection({ id: "expired", provider: "chatgpt", name: "Expired", needsSignIn: true }),
    ],
  });
  show(<NativeLauncher project={project} harness="codex" />);
  const select = await screen.findByLabelText("Provider connection");
  await waitFor(() => expect(select).toHaveValue("chatgpt"));
  expect(screen.getByRole("option", { name: "Sign in inside the harness" })).toBeInTheDocument();
  expect(screen.getByText("The harness will choose")).toBeInTheDocument();
  cleanup();
  vi.spyOn(api, "providerConnections").mockResolvedValue({
    enabled: true,
    items: [
      connection({ id: "chatgpt", provider: "chatgpt", name: "ChatGPT" }),
      connection({ id: "api", provider: "openai", name: "API key" }),
    ],
  });
  show(<NativeLauncher project={project} harness="codex" />);
  await waitFor(() => expect(screen.getByLabelText("Provider connection")).toHaveValue(""));
});

it("preselects the only live connection on a thread and says it cannot show a browser login", async () => {
  vi.spyOn(api, "capabilities").mockResolvedValue({
    providerVersion: "docker/v2", attach: true, run: true, structured: true, git: false, pause: false,
    snapshot: false, clone: false, browse: false, delivery: false, preview: false, resourceMetrics: false, harnessImages: [],
  });
  vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
  vi.spyOn(api, "providerConnections").mockResolvedValue({
    enabled: true,
    items: [connection({ id: "claude", provider: "anthropic", name: "Claude" })],
  });
  const grant = vi.spyOn(api, "grantConnection").mockResolvedValue({ connectionId: "claude" });
  const spawn = vi.spyOn(api, "createProjectThread").mockResolvedValue({
    id: "intent", projectId: project.id, harness: "claude", state: "provisioning", capsuleId: "capsule",
    capsuleName: "claude-task", threadId: "thread", runId: "run", messageId: "message",
    createdAt: timestamp, updatedAt: timestamp, resourceVersion: 1,
  });
  vi.spyOn(api, "projectThreadIntent").mockResolvedValue({
    id: "intent", projectId: project.id, harness: "claude", state: "provisioning", capsuleId: "capsule",
    capsuleName: "claude-task", threadId: "thread", runId: "run", messageId: "message",
    createdAt: timestamp, updatedAt: timestamp, resourceVersion: 1,
  });
  vi.spyOn(api, "capsule").mockResolvedValue({
    id: "capsule", projectId: project.id, timelineId: "timeline", name: "generated", state: "Creating",
    desiredState: "Ready", restoreComplete: true, createdAt: timestamp, updatedAt: timestamp, resourceVersion: 1,
  });
  vi.spyOn(api, "runs").mockResolvedValue({ items: [] });
  show(<StructuredLauncher project={{ ...project, harnessImages: [{ name: "claude", imageReference: "example/claude:v1" }] }} harness="claude" />);
  const select = await screen.findByLabelText("Provider connection");
  await waitFor(() => expect(select).toHaveValue("claude"));
  expect(screen.getByText("Threads will use this; they cannot show a browser login.")).toBeInTheDocument();
  expect(screen.getByText("The harness will choose")).toBeInTheDocument();
  await userEvent.type(screen.getByLabelText("Task"), "fix the build");
  await userEvent.click(screen.getByRole("button", { name: "Start task" }));
  await waitFor(() => expect(spawn).toHaveBeenCalled());
  expect(grant).toHaveBeenCalledWith("project", "claude", "claude");
  expect(grant.mock.invocationCallOrder[0]).toBeLessThan(spawn.mock.invocationCallOrder[0]);
});

it("shows a custom endpoint's default model on the start form", async () => {
  ready();
  vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [] });
  vi.spyOn(api, "providerConnections").mockResolvedValue({
    enabled: true,
    items: [connection({ id: "dgx", provider: "openai-compatible", name: "DGX", baseUrl: "https://dgx.example", defaultModel: "qwen3-coder" })],
  });
  show(<NativeLauncher project={project} harness="codex" />);
  expect(await screen.findByText("Default model: qwen3-coder")).toBeInTheDocument();
  expect(screen.queryByText("The harness will choose")).toBeNull();
});

it("blocks a thread when the harness has no usable connection", async () => {
  vi.spyOn(api, "capabilities").mockResolvedValue({
    providerVersion: "docker/v2", attach: true, run: true, structured: true, git: false, pause: false,
    snapshot: false, clone: false, browse: false, delivery: false, preview: false, resourceMetrics: false, harnessImages: [],
  });
  vi.spyOn(api, "projectConnection").mockResolvedValue({ connectionId: "" });
  vi.spyOn(api, "providerConnections").mockResolvedValue({
    enabled: true,
    items: [connection({ id: "expired", provider: "openai", name: "OpenAI" })],
  });
  const spawn = vi.spyOn(api, "createProjectThread");
  const dsh = { ...project, harnessImages: [{ name: "dsh", imageReference: "example/dsh:v1" }] };
  show(<StructuredLauncher project={dsh} harness="dsh" />);
  expect(await screen.findByText("Add a provider connection first.")).toBeInTheDocument();
  const start = screen.getByRole("button", { name: "Start task" });
  expect(start).toBeDisabled();
  await userEvent.type(screen.getByLabelText("Task"), "fix the build");
  await userEvent.click(start);
  expect(spawn).not.toHaveBeenCalled();
  cleanup();
  vi.spyOn(api, "providerConnections").mockResolvedValue({
    enabled: true,
    items: [connection({ id: "expired", provider: "chatgpt", name: "ChatGPT", needsSignIn: true })],
  });
  show(<StructuredLauncher project={project} harness="codex" />);
  expect(await screen.findByText("Add a provider connection first.")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Start task" })).toBeDisabled();
});

it("still lets a terminal capsule start with no provider connection", async () => {
  ready();
  vi.spyOn(api, "harnessSetups").mockResolvedValue({ items: [setup] });
  vi.spyOn(api, "providerConnections").mockResolvedValue({ items: [], enabled: true });
  show(<NativeLauncher project={project} harness="codex" />);
  await waitFor(() => expect(screen.getByRole("button", { name: "Start Codex" })).toBeEnabled());
  expect(screen.getByRole("link", { name: "Connect provider" })).toBeInTheDocument();
  expect(screen.queryByText("Add a provider connection first.")).toBeNull();
});
