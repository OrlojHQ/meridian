import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { api } from "../api/client";
import { ProviderConnections, connectionSupports } from "../components/ProviderConnections";

const createdAt = "2026-11-09T00:00:00Z";
const signIn = {
  id: "attempt",
  authorizationUrl: "https://auth.openai.com/api/accounts/authorize?client_id=dynamic_agent_client&state=s",
  redirectUri: "http://127.0.0.1:1455/auth/callback",
  listening: true,
  expiresAt: "2026-11-09T00:10:00Z",
  state: "pending" as const,
};
const chatgpt = { id: "chatgpt", provider: "chatgpt" as const, name: "ChatGPT", account: "dev@example.com", createdAt, resourceVersion: 2, revoked: false };

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

it("signs in with ChatGPT through the daemon and never offers it as a key form", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, chatgptAvailable: true, items: [] });
  const start = vi.spyOn(api, "startChatGPTSignIn").mockResolvedValue(signIn);
  vi.spyOn(api, "chatGPTSignIn")
    .mockResolvedValueOnce(signIn)
    .mockResolvedValue({ ...signIn, state: "completed", connectionId: "chatgpt" });
  show();
  const provider = await screen.findByLabelText("Provider");
  await userEvent.selectOptions(provider, "chatgpt");
  expect(screen.queryByLabelText(/^API key/)).toBeNull();
  expect(screen.queryByRole("button", { name: "Save connection" })).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
  expect(start).toHaveBeenCalledWith(undefined);
  expect(await screen.findByRole("link", { name: "Open ChatGPT sign-in" })).toHaveAttribute("href", signIn.authorizationUrl);
  expect(screen.getByRole("link", { name: "Open ChatGPT sign-in" })).toHaveAttribute("rel", "noopener noreferrer");
  expect(await screen.findByText(/Signed in to ChatGPT/, {}, { timeout: 4000 })).toBeInTheDocument();
});

it("finishes from a pasted redirect when the daemon cannot catch it", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({ enabled: true, chatgptAvailable: true, items: [] });
  vi.spyOn(api, "startChatGPTSignIn").mockResolvedValue({ ...signIn, listening: false });
  vi.spyOn(api, "chatGPTSignIn").mockResolvedValue({ ...signIn, listening: false });
  const complete = vi.spyOn(api, "completeChatGPTSignIn").mockResolvedValue({ ...signIn, state: "completed", connectionId: "chatgpt" });
  show();
  await userEvent.selectOptions(await screen.findByLabelText("Provider"), "chatgpt");
  await userEvent.click(screen.getByRole("button", { name: "Continue with ChatGPT" }));
  const address = "http://127.0.0.1:1455/auth/callback?code=c&state=s";
  await userEvent.type(await screen.findByLabelText(/paste the address/), address);
  await userEvent.click(screen.getByRole("button", { name: "Finish sign-in" }));
  await waitFor(() => expect(complete).toHaveBeenCalledWith("attempt", address));
  expect(await screen.findByText(/Signed in to ChatGPT/)).toBeInTheDocument();
});

it("asks an expired ChatGPT connection to sign in again", async () => {
  vi.spyOn(api, "providerConnections").mockResolvedValue({
    enabled: true,
    chatgptAvailable: true,
    items: [{ ...chatgpt, needsSignIn: true }, { ...chatgpt, id: "old", revoked: true, revocationUnconfirmed: true }],
  });
  const start = vi.spyOn(api, "startChatGPTSignIn").mockResolvedValue(signIn);
  vi.spyOn(api, "chatGPTSignIn").mockResolvedValue(signIn);
  show();
  expect(await screen.findByText(/Sign-in expired/)).toBeInTheDocument();
  expect(screen.getByText(/OpenAI did not confirm the sign-out/)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Sign in again" }));
  await waitFor(() => expect(start).toHaveBeenCalledWith("chatgpt"));
  expect(start).toHaveBeenCalledTimes(1);
});

it("pairs ChatGPT plan connections only with Codex", () => {
  expect(connectionSupports("codex", chatgpt)).toBe(true);
  for (const harness of ["claude", "opencode", "pi", "dsh", "mock"]) {
    expect(connectionSupports(harness, chatgpt)).toBe(false);
  }
});
