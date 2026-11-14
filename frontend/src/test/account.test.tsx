import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { MeridianAPIError, api } from "../api/client";
import { Account } from "../components/Account";

function renderAccount(initial: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[initial]}>
        <Account />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Account", () => {
  it("offers the claim only while the installation is claimable", async () => {
    vi.spyOn(api, "authenticationMode").mockResolvedValue({ mode: "single_user", signIn: true, claimable: true });
    renderAccount("/ui/settings/account");
    const link = await screen.findByRole("link", { name: "Sign in to claim this installation" });
    expect(link).toHaveAttribute("href", "/auth/oidc/start");
  });

  it("claims with the installation token and keeps it out of storage", async () => {
    vi.spyOn(api, "authenticationMode").mockResolvedValue({ mode: "single_user", signIn: true, claimable: true });
    const storage = vi.spyOn(Storage.prototype, "setItem");
    const claim = vi.spyOn(api, "claimInstallation").mockRejectedValue(new MeridianAPIError("Claim failed", "not_found", 404));
    renderAccount("/ui/settings/account?claim=ready");
    const input = await screen.findByLabelText("Installation token");
    await userEvent.type(input, "installation-secret");
    await userEvent.click(screen.getByRole("button", { name: "Claim and turn on team mode" }));
    await waitFor(() => expect(claim).toHaveBeenCalledWith("installation-secret"));
    expect(input).toHaveValue("");
    expect(storage).not.toHaveBeenCalled();
    expect(await screen.findByRole("alert")).toHaveTextContent("Claim failed");
  });

  it("lists the member's sessions and revokes another one", async () => {
    vi.spyOn(api, "authenticationMode").mockResolvedValue({ mode: "team", signIn: true, claimable: false });
    vi.spyOn(api, "currentMember").mockResolvedValue({
      mode: "team", id: "usr_1", role: "member", displayName: "Alice", email: "alice@example.com",
    });
    vi.spyOn(api, "sessions").mockResolvedValue({
      items: [
        { id: "ses_1", createdAt: "2026-11-14T00:00:00Z", lastUsedAt: "2026-11-14T00:00:00Z", expiresAt: "2026-11-21T00:00:00Z", revoked: false, current: true },
        { id: "ses_2", createdAt: "2026-11-13T00:00:00Z", lastUsedAt: "2026-11-13T00:00:00Z", expiresAt: "2026-11-20T00:00:00Z", revoked: false, current: false },
      ],
    });
    vi.spyOn(api, "personalAccessTokens").mockResolvedValue({ items: [] });
    const revoke = vi.spyOn(api, "revokeSession").mockResolvedValue(undefined);
    renderAccount("/ui/settings/account");
    expect(await screen.findByText("Alice")).toBeInTheDocument();
    const buttons = await screen.findAllByRole("button", { name: "Revoke" });
    expect(buttons).toHaveLength(1);
    await userEvent.click(buttons[0]);
    await waitFor(() => expect(revoke).toHaveBeenCalledWith("ses_2"));
  });

  it("mints a token, shows it once, and keeps it out of storage", async () => {
    vi.spyOn(api, "authenticationMode").mockResolvedValue({ mode: "team", signIn: true, claimable: false });
    vi.spyOn(api, "currentMember").mockResolvedValue({ mode: "team", id: "usr_1", role: "member" });
    vi.spyOn(api, "sessions").mockResolvedValue({ items: [] });
    vi.spyOn(api, "personalAccessTokens").mockResolvedValue({ items: [] });
    const storage = vi.spyOn(Storage.prototype, "setItem");
    const mint = vi.spyOn(api, "mintPersonalAccessToken").mockResolvedValue({
      id: "pat_1", name: "laptop", readOnly: true, createdAt: "2026-11-14T00:00:00Z",
      lastUsedAt: "2026-11-14T00:00:00Z", expiresAt: "2027-02-12T00:00:00Z", revoked: false,
      token: "mrd_pat_secret",
    });
    renderAccount("/ui/settings/account");
    await userEvent.type(await screen.findByLabelText("Name"), "laptop");
    await userEvent.click(screen.getByLabelText("Read-only"));
    await userEvent.click(screen.getByRole("button", { name: "Create token" }));
    await waitFor(() =>
      expect(mint).toHaveBeenCalledWith({ name: "laptop", readOnly: true, expiresInDays: 90 }),
    );
    expect(await screen.findByLabelText("New token")).toHaveTextContent("mrd_pat_secret");
    await userEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByText("mrd_pat_secret")).toBeNull();
    expect(storage).not.toHaveBeenCalled();
  });
});
