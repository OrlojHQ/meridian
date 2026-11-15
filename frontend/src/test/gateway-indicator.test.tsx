import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { api } from "../api/client";
import { GatewayIndicator } from "../shell/GatewayIndicator";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  delete window.__MERIDIAN_DESKTOP__;
});

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <GatewayIndicator />
    </QueryClientProvider>,
  );
}

describe("gateway indicator", () => {
  it("renders nothing in a browser", () => {
    const mode = vi.spyOn(api, "authenticationMode");
    const { container } = show();
    expect(container).toBeEmptyDOMElement();
    expect(mode).not.toHaveBeenCalled();
  });

  it("names this computer and opens the picker on click", async () => {
    vi.spyOn(api, "authenticationMode").mockResolvedValue({ mode: "single_user", signIn: false, claimable: false });
    const member = vi.spyOn(api, "currentMember");
    const openGatewaySettings = vi.fn();
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      openGatewaySettings,
      gateway: vi.fn().mockResolvedValue({ label: "This computer", local: true }),
    };
    show();
    const button = await screen.findByRole("button", { name: /Station: This computer\. Switch Station/ });
    expect(button).toHaveTextContent("This computer");
    await userEvent.click(button);
    expect(openGatewaySettings).toHaveBeenCalledOnce();
    expect(member).not.toHaveBeenCalled();
  });

  it("names the remote host and the signed-in member in team mode", async () => {
    vi.spyOn(api, "authenticationMode").mockResolvedValue({ mode: "team", signIn: true, claimable: false });
    vi.spyOn(api, "currentMember").mockResolvedValue({
      mode: "team",
      id: "usr_1",
      role: "member",
      displayName: "Teammate",
      email: "teammate@example.test",
    });
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      openGatewaySettings: vi.fn(),
      gateway: vi.fn().mockResolvedValue({ label: "meridian.example.com", local: false }),
    };
    show();
    expect(await screen.findByText("meridian.example.com · Teammate")).toBeInTheDocument();
  });

  it("ignores a malformed bridge answer and an older shell", async () => {
    vi.spyOn(api, "authenticationMode").mockResolvedValue({ mode: "single_user", signIn: false, claimable: false });
    window.__MERIDIAN_DESKTOP__ = {
      notify: vi.fn(),
      setAttention: vi.fn(),
      gateway: vi.fn().mockResolvedValue({ label: 42 }),
    };
    const { container } = show();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(container).toBeEmptyDOMElement();
    cleanup();

    window.__MERIDIAN_DESKTOP__ = { notify: vi.fn(), setAttention: vi.fn() };
    expect(show().container).toBeEmptyDOMElement();
  });
});
