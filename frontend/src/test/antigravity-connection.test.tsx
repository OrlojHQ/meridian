import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ProviderConnections, connectionSupports, googleCode } from "../components/ProviderConnections";
import { choose, optionLabels } from "./richSelect";

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
  vi.unstubAllGlobals();
});

const code = "4/0AVGzR1Example_code-123";

async function reachCodeStep(readText: () => Promise<string>) {
  vi.stubGlobal("navigator", { ...navigator, clipboard: { readText: vi.fn(readText) } });
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, antigravityAvailable: true, items: [] });
  vi.spyOn(api, "startAntigravitySignIn").mockResolvedValue({ id: "attempt", state: "awaiting_code", expiresAt, authorizationUrl });
  vi.spyOn(api, "antigravitySignIn").mockResolvedValue({ id: "attempt", state: "awaiting_code", expiresAt, authorizationUrl });
  const send = vi.spyOn(api, "sendAntigravitySignInCode").mockResolvedValue({ id: "attempt", state: "completed", expiresAt, connectionId: "google", account: "dev@example.com" });
  show();
  await choose(await screen.findByLabelText("Provider"), "Google account (Antigravity)");
  await userEvent.click(screen.getByLabelText(/I understand/));
  await userEvent.click(screen.getByRole("button", { name: "Sign in with Google" }));
  return { send, paste: await screen.findByRole("button", { name: "Paste from clipboard" }) };
}

it("asks for consent, relays agy's link and the pasted code, and never offers a key form", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, antigravityAvailable: true, items: [] });
  const start = vi.spyOn(api, "startAntigravitySignIn").mockResolvedValue({ id: "attempt", state: "starting", expiresAt });
  vi.spyOn(api, "antigravitySignIn").mockResolvedValue({ id: "attempt", state: "awaiting_code", expiresAt, authorizationUrl });
  const send = vi.spyOn(api, "sendAntigravitySignInCode").mockResolvedValue({ id: "attempt", state: "completed", expiresAt, connectionId: "google", account: "dev@example.com" });
  show();
  await choose(await screen.findByLabelText("Provider"), "Google account (Antigravity)");
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

it("shows a spinner while the sign-in Capsule starts", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, antigravityAvailable: true, items: [] });
  vi.spyOn(api, "startAntigravitySignIn").mockResolvedValue({ id: "attempt", state: "starting", expiresAt });
  vi.spyOn(api, "antigravitySignIn").mockReturnValue(new Promise(() => {}));
  show();
  await choose(await screen.findByLabelText("Provider"), "Google account (Antigravity)");
  await userEvent.click(screen.getByLabelText(/I understand/));
  await userEvent.click(screen.getByRole("button", { name: "Sign in with Google" }));
  const progress = await screen.findByText(/Starting a sign-in Capsule/);
  expect(progress).toHaveAttribute("role", "status");
  expect(progress.querySelector(".spinner")).not.toBeNull();
  expect(screen.queryByRole("link", { name: "Open Google sign-in" })).toBeNull();
});

it("finishes in one click with the code Google's page copied", async () => {
  const { send, paste } = await reachCodeStep(async () => ` ${code}\n`);
  await userEvent.click(paste);
  expect(send).toHaveBeenCalledWith("attempt", code);
});

it("never sends a clipboard that doesn't hold Google's code", async () => {
  const { send, paste } = await reachCodeStep(async () => "git push origin main");
  await userEvent.click(paste);
  expect(await screen.findByRole("alert")).toHaveTextContent(/doesn't hold the code/);
  expect(send).not.toHaveBeenCalled();
});

it("falls back to the field when the browser refuses the clipboard", async () => {
  const { send, paste } = await reachCodeStep(async () => { throw new DOMException("denied", "NotAllowedError"); });
  await userEvent.click(paste);
  expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't read the clipboard/);
  expect(send).not.toHaveBeenCalled();
});

it("accepts only Google's code or its Antigravity page address", () => {
  expect(googleCode(code)).toBe(code);
  expect(googleCode(`https://antigravity.google/oauth-callback?code=${encodeURIComponent(code)}&scope=email`)).toBe(code);
  for (const text of ["", "4/0", "hello", `${code} extra`, `https://evil.example/oauth-callback?code=${code}`, `http://antigravity.google/oauth-callback?code=${code}`, `https://antigravity.google/other?code=${code}`, `4/${"a".repeat(2050)}`]) {
    expect(googleCode(text)).toBeUndefined();
  }
});

it("hides Sign in with Google where the installation cannot offer it", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, items: [claude] });
  show();
  const provider = await screen.findByLabelText("Provider");
  expect(await optionLabels(provider)).not.toContain("Google account (Antigravity)");
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

it("still offers Sign in with Google when the provider gateway is off", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: false, antigravityAvailable: true, items: [] });
  show();
  const provider = await screen.findByLabelText("Provider");
  expect(provider).toHaveTextContent("Google account (Antigravity)");
  expect(await optionLabels(provider)).toEqual(["Google account (Antigravity)"]);
  expect(screen.getByRole("button", { name: "Sign in with Google" })).toBeInTheDocument();
});

it("shows completion immediately even if a status poll returned completing", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, antigravityAvailable: true, items: [] });
  vi.spyOn(api, "startAntigravitySignIn").mockResolvedValue({ id: "attempt", state: "awaiting_code", expiresAt, authorizationUrl });
  let polls = 0;
  vi.spyOn(api, "antigravitySignIn").mockImplementation(async () => {
    polls++;
    return polls === 1
      ? { id: "attempt", state: "awaiting_code", expiresAt, authorizationUrl }
      : { id: "attempt", state: "completing", expiresAt, authorizationUrl };
  });
  vi.spyOn(api, "sendAntigravitySignInCode").mockResolvedValue({ id: "attempt", state: "completed", expiresAt, connectionId: "google", account: "dev@example.com" });
  show();
  await choose(await screen.findByLabelText("Provider"), "Google account (Antigravity)");
  await userEvent.click(screen.getByLabelText(/I understand/));
  await userEvent.click(screen.getByRole("button", { name: "Sign in with Google" }));
  await userEvent.type(await screen.findByLabelText("Paste that code here"), code);
  await userEvent.click(screen.getByRole("button", { name: "Finish sign-in" }));
  expect(await screen.findByText(/dev@example\.com/)).toBeInTheDocument();
});

it("does not reopen the same sign-in after Done", async () => {
  const { paste } = await reachCodeStep(async () => code);
  await userEvent.click(paste);
  await userEvent.click(await screen.findByRole("button", { name: "Done" }));
  expect(screen.queryByRole("button", { name: "Sign in with Google" })).toBeNull();
  expect(screen.queryByText(/Your Google sign-in goes into each agy Capsule/)).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "Sign in with another account" }));
  expect(screen.getByRole("button", { name: "Sign in with Google" })).toBeDisabled();
  expect(screen.getByLabelText(/I understand/)).not.toBeChecked();
});

it("offers the sign-in again after a cancel", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, antigravityAvailable: true, items: [] });
  vi.spyOn(api, "startAntigravitySignIn").mockResolvedValue({ id: "attempt", state: "starting", expiresAt });
  vi.spyOn(api, "antigravitySignIn").mockResolvedValue({ id: "attempt", state: "starting", expiresAt });
  const cancel = vi.spyOn(api, "cancelAntigravitySignIn").mockResolvedValue(undefined);
  show();
  await choose(await screen.findByLabelText("Provider"), "Google account (Antigravity)");
  await userEvent.click(screen.getByLabelText(/I understand/));
  await userEvent.click(screen.getByRole("button", { name: "Sign in with Google" }));
  await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));
  expect(cancel).toHaveBeenCalledWith("attempt");
  expect(await screen.findByRole("button", { name: "Sign in with Google" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Sign in with another account" })).toBeNull();
});
