import { cleanup, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  terminalDispose: vi.fn(),
  inputDispose: vi.fn(),
  fit: vi.fn(),
  socketSend: vi.fn(),
  socketClose: vi.fn(),
  observerDisconnect: vi.fn(),
  observerCallback: undefined as ResizeObserverCallback | undefined,
}));

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    loadAddon() {}
    open() {}
    onData() {
      return { dispose: mocks.inputDispose };
    }
    write() {}
    dispose() {
      mocks.terminalDispose();
    }
  },
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit() {
      mocks.fit();
    }
  },
}));

import { api } from "../api/client";
import {
  TerminalView,
  terminalSocketURL,
  ticketExpired,
} from "../components/Terminal";

class FakeResizeObserver {
  constructor(callback: ResizeObserverCallback) {
    mocks.observerCallback = callback;
  }
  observe() {}
  unobserve() {}
  disconnect() {
    mocks.observerDisconnect();
  }
}

class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSING = 2;
  readonly CLOSED = 3;
  readyState = FakeWebSocket.OPEN;
  binaryType: BinaryType = "blob";
  listeners = new Map<string, Array<EventListener>>();

  constructor(_url: string | URL) {}

  addEventListener(name: string, listener: EventListener) {
    const values = this.listeners.get(name) ?? [];
    values.push(listener);
    this.listeners.set(name, values);
    if (name === "open") queueMicrotask(() => listener(new Event("open")));
  }
  removeEventListener() {}
  dispatchEvent() {
    return true;
  }
  send(value: string | ArrayBufferLike | Blob | ArrayBufferView) {
    mocks.socketSend(value);
  }
  close() {
    this.readyState = FakeWebSocket.CLOSED;
    mocks.socketClose();
  }
}

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", FakeResizeObserver);
  vi.stubGlobal("WebSocket", FakeWebSocket);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("terminal ticket scope", () => {
  it("detects expiry and rejects cross-origin or cross-Run paths", () => {
    expect(
      ticketExpired({
        ticket: "secret",
        expiresAt: "2026-08-23T00:00:00Z",
        webSocketPath: "/runs/run-1/attach",
      }, Date.parse("2026-08-23T00:00:01Z")),
    ).toBe(true);
    expect(() =>
      terminalSocketURL(
        {
          ticket: "secret",
          expiresAt: "2099-01-01T00:00:00Z",
          webSocketPath: "https://attacker.example/runs/run-1/attach",
        },
        "run-1",
      ),
    ).toThrow(/invalid WebSocket path/);
    expect(() =>
      terminalSocketURL(
        {
          ticket: "secret",
          expiresAt: "2099-01-01T00:00:00Z",
          webSocketPath: "/runs/run-2/attach",
        },
        "run-1",
      ),
    ).toThrow(/invalid WebSocket path/);
  });
});

it("fits, resizes, and disposes every terminal resource", async () => {
  vi.spyOn(api, "attachTicket").mockResolvedValue({
    ticket: "a".repeat(43),
    expiresAt: "2099-01-01T00:00:00Z",
    webSocketPath: "/runs/run-1/attach",
  });
  const view = render(
    <TerminalView runId="run-1" reconnect={true} supported={true} />,
  );
  await waitFor(() => expect(mocks.socketSend).toHaveBeenCalled());
  mocks.observerCallback?.([], {} as ResizeObserver);
  expect(mocks.fit).toHaveBeenCalledTimes(2);
  expect(mocks.socketSend).toHaveBeenCalledWith(
    JSON.stringify({ type: "resize", columns: 80, rows: 24 }),
  );
  view.unmount();
  expect(mocks.observerDisconnect).toHaveBeenCalledOnce();
  expect(mocks.inputDispose).toHaveBeenCalledOnce();
  expect(mocks.socketClose).toHaveBeenCalledOnce();
  expect(mocks.terminalDispose).toHaveBeenCalledOnce();
});
