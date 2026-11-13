import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api/client";
import type { ProviderConnection } from "../api/generated/types.gen";
import { ProviderConnections, connectionKind, connectionStatus } from "../components/ProviderConnections";

const createdAt = "2026-11-09T00:00:00Z";
const connection = (patch: Partial<ProviderConnection> & Pick<ProviderConnection, "id" | "provider" | "name">): ProviderConnection => ({
  createdAt,
  resourceVersion: 1,
  revoked: false,
  ...patch,
});

function show() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <ProviderConnections />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("lists each connection with its mark, what it is, and its status", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({
    enabled: true,
    items: [
      connection({ id: "claude", provider: "anthropic", name: "Claude", credentialKind: "oauth" }),
      connection({ id: "vllm", provider: "openai-compatible", name: "Home vLLM", baseUrl: "https://models.example.com", defaultModel: "qwen" }),
      connection({ id: "old", provider: "openai", name: "Old key", revoked: true }),
    ],
  });
  show();
  const list = await screen.findByRole("list", { name: "Saved connections" });
  const [claude, vllm, old] = within(list).getAllByRole("listitem");

  expect(claude.querySelector(".provider-mark-claude img")).not.toBeNull();
  expect(claude).toHaveTextContent("Claude account");
  expect(within(claude).getByText("Signed in")).toHaveClass("connection-status", "ok");
  expect(within(claude).getByRole("button", { name: "Clear login" })).toBeInTheDocument();

  expect(vllm.querySelector(".provider-mark-endpoint svg")).not.toBeNull();
  expect(vllm).toHaveTextContent("OpenAI-compatible endpoint · https://models.example.com");
  expect(vllm).toHaveTextContent("qwen");
  expect(within(vllm).getByText("Saved")).toBeInTheDocument();

  expect(old.querySelector(".provider-mark-openai svg")).not.toBeNull();
  expect(within(old).getByText("Revoked")).toHaveClass("connection-status", "muted");
  expect(within(old).queryByRole("button")).toBeNull();
});

it("shows the chosen provider's mark in the provider select and titles the form", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, items: [] });
  show();
  expect(await screen.findByRole("heading", { name: "Add a connection" })).toBeInTheDocument();
  const select = screen.getByLabelText("Provider");
  const field = select.closest(".select-field")!;
  expect(field.querySelector(".provider-mark-openai")).not.toBeNull();
  await userEvent.selectOptions(select, "anthropic");
  expect(field.querySelector(".provider-mark-claude")).not.toBeNull();
  expect(screen.getByRole("option", { name: "Claude" })).toBeInTheDocument();
});

it("describes connections in words", () => {
  expect(connectionKind(connection({ id: "a", provider: "anthropic", name: "a" }))).toBe("Anthropic API key");
  expect(connectionKind(connection({ id: "c", provider: "chatgpt", name: "c", account: "dev@example.com" }))).toBe("ChatGPT plan · dev@example.com");
  expect(connectionStatus(connection({ id: "c", provider: "chatgpt", name: "c", needsSignIn: true }))).toEqual({ label: "Sign-in expired", tone: "warn" });
  expect(connectionStatus(connection({ id: "o", provider: "openai", name: "o" }))).toEqual({ label: "Saved", tone: "ok" });
});
