import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { useEffect, useRef, useState } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { AttachTicket } from "../api/generated/types.gen";

type TerminalStatus =
  | "connecting"
  | "attached"
  | "reconnecting"
  | "closed"
  | "expired"
  | "unavailable"
  | "exited";

const terminalStatusLabel: Record<TerminalStatus, string> = {
  connecting: "Connecting…",
  attached: "Connected",
  reconnecting: "Reconnecting…",
  closed: "Connection closed",
  expired: "Connection expired",
  unavailable: "Terminal unavailable",
  exited: "Shell exited",
};

const permanentCodes = new Set([
  "not_found",
  "attach_unavailable",
  "shell_unavailable",
  "unsupported",
  "illegal_transition",
]);

export function ticketExpired(ticket: AttachTicket, now = Date.now()) {
  return Date.parse(ticket.expiresAt) <= now;
}

export function terminalSocketURL(ticket: AttachTicket, runId?: string) {
  const base = new URL(api.baseUrl);
  const target = new URL(ticket.webSocketPath, base);
  const expectedPath = runId
    ? `/runs/${encodeURIComponent(runId)}/attach`
    : undefined;
  const shell = /^\/capsules\/[^/]+\/shell\/attach$/.test(target.pathname);
  if (
    target.origin !== base.origin ||
    !(target.pathname.startsWith("/runs/") || shell) ||
    (expectedPath !== undefined && !shell && target.pathname !== expectedPath)
  ) {
    throw new Error("Attach ticket returned an invalid WebSocket path");
  }
  target.protocol = target.protocol === "https:" ? "wss:" : "ws:";
  target.searchParams.set("ticket", ticket.ticket);
  return target.toString();
}

interface TerminalViewProps {
  runId?: string;
  reconnect: boolean;
  supported: boolean;
  /** When set, this mints the attach ticket instead of the harness Run. */
  requestTicket?: (signal: AbortSignal) => Promise<AttachTicket>;
  /** After the shell exits, offer another one instead of reconnecting. */
  restartable?: boolean;
  label?: string;
}

export function TerminalView({
  runId,
  reconnect,
  supported,
  requestTicket,
  restartable = false,
  label = "Run terminal",
}: TerminalViewProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const cursorRef = useRef(0);
  const activeRef = useRef(true);
  const socketRef = useRef<WebSocket | null>(null);
  const [status, setStatus] = useState<TerminalStatus>("connecting");
  const [gap, setGap] = useState<number>();
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    if (!supported || !hostRef.current) {
      setStatus("closed");
      return;
    }

    activeRef.current = true;
    const terminal = new Terminal({
      cursorBlink: true,
      convertEol: false,
      customGlyphs: true,
      fontFamily:
        '"SFMono-Regular", "Cascadia Mono", "Roboto Mono", Menlo, Consolas, monospace',
      scrollback: 2_000,
      theme: {
        background: "#111315",
        foreground: "#e7e8ea",
      },
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(hostRef.current);
    let webgl: WebglAddon | undefined;
    try {
      webgl = new WebglAddon();
      terminal.loadAddon(webgl);
      webgl.onContextLoss(() => {
        webgl?.dispose();
        webgl = undefined;
      });
    } catch {
      webgl?.dispose();
      webgl = undefined;
    }
    fit.fit();

    let timer: number | undefined;
    let currentController: AbortController | undefined;
    const sendResize = () => {
      const socket = socketRef.current;
      if (socket?.readyState === WebSocket.OPEN) {
        socket.send(
          JSON.stringify({
            type: "resize",
            columns: terminal.cols,
            rows: terminal.rows,
          }),
        );
      }
    };
    // Refit on every size change, but send the PTY one resize after the size
    // settles, so dragging the panel edge does not resize it on every frame.
    let resizeTimer: number | undefined;
    const resizeObserver = new ResizeObserver(() => {
      fit.fit();
      if (resizeTimer !== undefined) window.clearTimeout(resizeTimer);
      resizeTimer = window.setTimeout(() => {
        resizeTimer = undefined;
        sendResize();
      }, 100);
    });
    resizeObserver.observe(hostRef.current);
    const input = terminal.onData((value) => {
      const socket = socketRef.current;
      if (socket?.readyState === WebSocket.OPEN) {
        socket.send(new TextEncoder().encode(value));
      }
    });

    const connect = async () => {
      if (!activeRef.current) return;
      setStatus(cursorRef.current > 0 ? "reconnecting" : "connecting");
      currentController = new AbortController();
      try {
        const ticket = requestTicket
          ? await requestTicket(currentController.signal)
          : await api.attachTicket(runId ?? "", cursorRef.current, currentController.signal);
        if (!activeRef.current) return;
        if (ticketExpired(ticket)) {
          setStatus("expired");
          if (reconnect) timer = window.setTimeout(connect, 500);
          return;
        }
        const socket = new WebSocket(terminalSocketURL(ticket, runId));
        socket.binaryType = "arraybuffer";
        socketRef.current = socket;
        socket.addEventListener("open", () => {
          if (!activeRef.current) return;
          setStatus("attached");
          sendResize();
        });
        socket.addEventListener("message", async (event) => {
          if (!activeRef.current) return;
          if (event.data instanceof ArrayBuffer) {
            terminal.write(new Uint8Array(event.data));
            return;
          }
          if (event.data instanceof Blob) {
            terminal.write(new Uint8Array(await event.data.arrayBuffer()));
            return;
          }
          if (typeof event.data !== "string") return;
          try {
            const frame = JSON.parse(event.data) as {
              sequence?: number;
              type?: string;
              data?: string;
            };
            if (frame.type === "exit") {
              setStatus("exited");
              socket.close(1000, "shell exited");
              return;
            }
            if (frame.type === "gap") {
              setGap(frame.sequence ?? cursorRef.current);
              return;
            }
            if (frame.sequence && frame.sequence > cursorRef.current) {
              cursorRef.current = frame.sequence;
            }
            if (frame.data) {
              const raw = atob(frame.data);
              const bytes = Uint8Array.from(raw, (character) =>
                character.charCodeAt(0),
              );
              terminal.write(bytes);
            }
          } catch {
            socket.close(1002, "invalid control frame");
          }
        });
        socket.addEventListener("close", () => {
          if (socketRef.current === socket) socketRef.current = null;
          if (!activeRef.current) return;
          setStatus((current) => {
            if (current === "exited" || current === "unavailable") return current;
            if (reconnect) {
              timer = window.setTimeout(connect, 1_000);
              return "reconnecting";
            }
            return "closed";
          });
        });
        socket.addEventListener("error", () => socket.close());
      } catch (error) {
        if (!activeRef.current) return;
        const normalized = normalizeAPIError(error);
        if (permanentCodes.has(normalized.code) || normalized.status === 404) {
          setStatus("unavailable");
          return;
        }
        if (reconnect) {
          setStatus("reconnecting");
          timer = window.setTimeout(connect, 1_000);
        } else {
          setStatus("closed");
        }
      }
    };

    void connect();
    return () => {
      activeRef.current = false;
      if (timer !== undefined) window.clearTimeout(timer);
      if (resizeTimer !== undefined) window.clearTimeout(resizeTimer);
      currentController?.abort();
      resizeObserver.disconnect();
      input.dispose();
      const socket = socketRef.current;
      socketRef.current = null;
      if (socket && socket.readyState < WebSocket.CLOSING) {
        socket.close(1000, "terminal unmounted");
      }
      terminal.dispose();
    };
  }, [generation, reconnect, requestTicket, restartable, runId, supported]);

  if (!supported) {
    return (
      <section className="notice" aria-label="Terminal unavailable">
        Terminal attachment is unsupported by this provider.
      </section>
    );
  }

  return (
    <section aria-label={label}>
      <div className="terminal-toolbar">
        <span role="status">{terminalStatusLabel[status]}</span>
        {status === "exited" && restartable && (
          <button type="button" onClick={() => setGeneration((value) => value + 1)}>
            New shell
          </button>
        )}
        {status === "unavailable" && (
          <span>This session has no terminal to attach.</span>
        )}
      </div>
      {gap !== undefined && (
        <p className="warning" role="alert">
          Terminal replay gap before cursor {gap}. Earlier PTY bytes are no longer
          available.
        </p>
      )}
      <div ref={hostRef} className="terminal-host" />
      <p className="sensitive-note">
        PTY bytes are displayed in memory only and are never logged or persisted by
        this interface.
      </p>
    </section>
  );
}
