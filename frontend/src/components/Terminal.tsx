import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { useEffect, useRef, useState } from "react";

import { api } from "../api/client";
import type { AttachTicket } from "../api/generated/types.gen";

type TerminalStatus =
  | "connecting"
  | "attached"
  | "reconnecting"
  | "detached"
  | "closed"
  | "expired";

export function ticketExpired(ticket: AttachTicket, now = Date.now()) {
  return Date.parse(ticket.expiresAt) <= now;
}

export function terminalSocketURL(ticket: AttachTicket, runId?: string) {
  const base = new URL(api.baseUrl);
  const target = new URL(ticket.webSocketPath, base);
  const expectedPath = runId
    ? `/runs/${encodeURIComponent(runId)}/attach`
    : undefined;
  if (
    target.origin !== base.origin ||
    !target.pathname.startsWith("/runs/") ||
    (expectedPath !== undefined && target.pathname !== expectedPath)
  ) {
    throw new Error("Attach ticket returned an invalid WebSocket path");
  }
  target.protocol = target.protocol === "https:" ? "wss:" : "ws:";
  target.searchParams.set("ticket", ticket.ticket);
  return target.toString();
}

interface TerminalViewProps {
  runId: string;
  reconnect: boolean;
  supported: boolean;
}

export function TerminalView({ runId, reconnect, supported }: TerminalViewProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const cursorRef = useRef(0);
  const activeRef = useRef(true);
  const socketRef = useRef<WebSocket | null>(null);
  const [status, setStatus] = useState<TerminalStatus>("connecting");
  const [gap, setGap] = useState<number>();

  useEffect(() => {
    if (!supported || !hostRef.current) {
      setStatus("closed");
      return;
    }

    activeRef.current = true;
    const terminal = new Terminal({
      cursorBlink: true,
      convertEol: false,
      scrollback: 2_000,
      theme: {
        background: "#111315",
        foreground: "#e7e8ea",
      },
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(hostRef.current);
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
    const resizeObserver = new ResizeObserver(() => {
      fit.fit();
      sendResize();
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
        const ticket = await api.attachTicket(
          runId,
          cursorRef.current,
          currentController.signal,
        );
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
          if (reconnect) {
            setStatus("reconnecting");
            timer = window.setTimeout(connect, 1_000);
          } else {
            setStatus("closed");
          }
        });
        socket.addEventListener("error", () => socket.close());
      } catch {
        if (!activeRef.current) return;
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
  }, [reconnect, runId, supported]);

  if (!supported) {
    return (
      <section className="notice" aria-label="Terminal unavailable">
        Terminal attachment is unsupported by this provider.
      </section>
    );
  }

  return (
    <section aria-label="Run terminal">
      <div className="terminal-toolbar">
        <span role="status">Terminal: {status}</span>
        <span>Cursor {cursorRef.current}</span>
        <button
          type="button"
          onClick={() => {
            activeRef.current = false;
            socketRef.current?.close(1000, "detached by user");
            setStatus("detached");
          }}
        >
          Detach
        </button>
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
