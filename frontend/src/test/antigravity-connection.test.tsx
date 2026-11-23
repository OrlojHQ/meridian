import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ProviderConnections, connectionSupports } from "../components/ProviderConnections";

const createdAt = "2026-11-22T00:00:00Z";
const expiresAt = "2026-11-22T00:10:00Z";
const authorizationUrl = "https://accounts.google.com/o/oauth2/auth?redirect_uri=https%3A%2F%2Fantigravity.google%2Foauth-callback";
const google = { id: "google", provider: "antigravity" as const, name: "Google", account: "dev@example.com", createdAt, resourceVersion: 1, revoked: false };
const claude = { id: "claude", provider: "anthropic" as const, name: "Claude", createdAt, resourceVersion: 1, revoked: false };

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

it("asks for consent, relays agy's link and the pasted code, and never offers a key form", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, antigravityAvailable: true, items: [] });
  const start = vi.spyOn(api, "startAntigravitySignIn").mockResolvedValue({ id: "attempt", state: "starting", expiresAt });
  vi.spyOn(api, "antigravitySignIn").mockResolvedValue({ id: "attempt", state: "awaiting_code", expiresAt, authorizationUrl });
  const send = vi.spyOn(api, "sendAntigravitySignInCode").mockResolvedValue({ id: "attempt", state: "completed", expiresAt, connectionId: "google", account: "dev@example.com" });
  show();
  await userEvent.selectOptions(await screen.findByLabelText("Provider"), "antigravity");
  expect(screen.queryByLabelText(/^API key/)).toBeNull();
  expect(screen.queryByLabelText("Connection name")).toBeNull();
  expect(screen.getByText(/Your Google sign-in goes into each agy Capsule/)).toBeInTheDocument();
  const button = screen.getByRole("button", { name: "Sign in with Google" });
  expect(button).toBeDisabled();
  await userEvent.click(screen.getByLabelText(/I understand/));
  await userEvent.click(button);
  expect(start).toHaveBeenCalledWith(undefined);
  const link = await screen.findByRole("link", { name: "Open Google sign-in" }, { timeout: 4000 });
  expect(link).toHaveAttribute("href", authorizationUrl);
  expect(link).toHaveAttribute("rel", "noopener noreferrer");
  await userEvent.type(screen.getByLabelText("Paste that code here"), " 4/0code ");
  await userEvent.click(screen.getByRole("button", { name: "Finish sign-in" }));
  expect(send).toHaveBeenCalledWith("attempt", "4/0code");
});

it("hides Sign in with Google where the installation cannot offer it", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, items: [claude] });
  show();
  const provider = await screen.findByLabelText("Provider");
  expect([...(provider as HTMLSelectElement).options].map(option => option.value)).not.toContain("antigravity");
  expect(screen.getByText(/Sign in with ChatGPT or Claude/)).toBeInTheDocument();
});

it("shows the account and how to end the sign-in everywhere", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, antigravityAvailable: true, items: [google] });
  show();
  expect(await screen.findByText("Google account · dev@example.com")).toBeInTheDocument();
  expect(screen.getByText("Signed in")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /Remove Antigravity's access/ })).toHaveAttribute("href", "https://myaccount.google.com/connections");
  expect(screen.getByRole("button", { name: "Sign in again" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Reconnect" })).toBeNull();
});

it("pairs Antigravity connections only with agy", () => {
  expect(connectionSupports("agy", google)).toBe(true);
  expect(connectionSupports("agy", claude)).toBe(false);
  for (const harness of ["claude", "codex", "opencode", "pi", "dsh", "custom"]) {
    expect(connectionSupports(harness, google)).toBe(false);
  }
});
