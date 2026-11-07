import { useQueryClient } from "@tanstack/react-query";
import { type KeyboardEvent, useEffect, useId, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { api } from "../api/client";
import type { RunState, Thread, ThreadBlock } from "../api/generated/types.gen";
import { sessionPath } from "../shell/capsuleActivity";
import { plainAgentText } from "./agentText";
import { buildThreadChat, type ChatRequest } from "./threadChat";
import { RequestCard } from "./ThreadConversation";
import { ThreadError } from "./ThreadError";
import { ChevronIcon } from "./ui/Icons";

// The daemon reports a Thread as awaiting only from its trailing 256 messages
// (ADR 0025), so the peek reads the same window and nothing older.
const PEEK_WINDOW = 256;
export const PEEK_KIND_MAX = 64;
export const PEEK_SUMMARY_MAX = 600;

// The open request is the newest actionable one, matching the daemon's
// awaiting rule. Its agent-supplied kind and summary are flattened and capped;
// option values stay exactly as offered because the answer must name one.
export function openRequest(blocks: ThreadBlock[], runState?: RunState): ChatRequest | undefined {
  const items = buildThreadChat(blocks, runState).items;
  for (let index = items.length - 1; index >= 0; index -= 1) {
    const item = items[index];
    if (item.kind === "request" && item.actionable) {
      return {
        ...item.request,
        kind: plainAgentText(item.request.kind, PEEK_KIND_MAX),
        summary: plainAgentText(item.request.summary, PEEK_SUMMARY_MAX),
      };
    }
  }
  return undefined;
}

type PeekState =
  | { status: "loading" }
  | { status: "error"; error: unknown }
  | { status: "ready"; thread: Thread; request?: ChatRequest };

function focusNextBlocker(current: HTMLElement | null) {
  const scope = current?.closest(".fleet-view") ?? document;
  const toggles = Array.from(scope.querySelectorAll<HTMLElement>("[data-blocker-peek]"));
  const index = current ? toggles.indexOf(current) : -1;
  const next = [...toggles.slice(index + 1), ...toggles.slice(0, Math.max(index, 0))][0];
  next?.focus();
}

// A needs-you row's peek. The row itself stays content-free; only an explicit
// expand reads this one Thread's transcript tail, and the result lives in this
// component's state, never in the query cache, and is dropped on collapse.
export function BlockerPeek({
  thread,
  kind,
  label,
}: {
  thread: Thread;
  kind: "permission" | "input";
  label?: string;
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<PeekState>({ status: "loading" });
  const toggle = useRef<HTMLButtonElement>(null);
  const messageCount = useRef(thread.messageCount);
  messageCount.current = thread.messageCount;
  const regionId = useId();
  const threadId = thread.id;

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    setState({ status: "loading" });
    const after = Math.max(messageCount.current - PEEK_WINDOW, 0);
    (async () => {
      const blocks = await api.threadBlocks(threadId, after, controller.signal);
      const fresh = await api.thread(threadId, controller.signal);
      return { thread: fresh, request: openRequest(blocks.items, fresh.currentRunState) };
    })().then(
      (result) => {
        if (!controller.signal.aborted) setState({ status: "ready", ...result });
      },
      (error: unknown) => {
        if (!controller.signal.aborted) setState({ status: "error", error });
      },
    );
    return () => controller.abort();
  }, [open, attempt, threadId]);

  const answered = () => {
    setOpen(false);
    focusNextBlocker(toggle.current);
    void queryClient.invalidateQueries({ queryKey: ["board"] });
    void queryClient.invalidateQueries({ queryKey: ["threads"] });
    void queryClient.invalidateQueries({ queryKey: ["thread", threadId] });
  };
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key !== "Escape") return;
    event.stopPropagation();
    setOpen(false);
    toggle.current?.focus();
  };
  const noun = kind === "permission" ? "permission request" : "question";
  return (
    <div className="blocker-peek">
      <button
        type="button"
        ref={toggle}
        data-blocker-peek=""
        className="blocker-peek-toggle"
        aria-expanded={open}
        aria-controls={open ? regionId : undefined}
        onClick={() => setOpen((value) => !value)}
      >
        <ChevronIcon className="blocker-peek-chevron" />
        {open ? `Hide ${noun}` : `Peek at ${noun}`}
        {label && <span className="blocker-peek-label"> · {label}</span>}
      </button>
      {open && (
        <div
          id={regionId}
          className="blocker-peek-body"
          role="region"
          aria-label={kind === "permission" ? "Permission request" : "Question"}
          onKeyDown={onKeyDown}
        >
          {state.status === "loading" && (
            <p className="muted-copy" role="status">
              Loading request…
            </p>
          )}
          {state.status === "error" && <ThreadError error={state.error} />}
          {state.status === "ready" && !state.request && (
            <p className="muted-copy">
              Nothing is waiting any more.{" "}
              <Link to={sessionPath(state.thread.capsuleId, state.thread.id)}>Open the Thread</Link>
            </p>
          )}
          {state.status === "ready" && state.request && (
            <RequestCard
              thread={state.thread}
              request={state.request}
              actionable
              onAnswered={answered}
              onConflict={async () => setAttempt((value) => value + 1)}
            />
          )}
        </div>
      )}
    </div>
  );
}
