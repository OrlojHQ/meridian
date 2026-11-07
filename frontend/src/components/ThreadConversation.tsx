import { useMutation } from "@tanstack/react-query";
import {
  type KeyboardEvent,
  lazy,
  type ReactNode,
  Suspense,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { api, normalizeAPIError } from "../api/client";
import type { Thread, ThreadBlock } from "../api/generated/types.gen";
import {
  boundedThreadText,
  buildThreadChat,
  type ChatActivity,
  type ChatItem,
  type ChatRequest,
  safeInline,
  type ToolCall,
} from "./threadChat";
import { ThreadError } from "./ThreadError";
import {
  CheckIcon,
  ChevronIcon,
  FileIcon,
  SearchIcon,
  SendIcon,
  StopIcon,
  TerminalIcon,
  ToolIcon,
  WarningIcon,
} from "./ui/Icons";

const ThreadMarkdown = lazy(() =>
  import("./ThreadMarkdown").then((module) => ({ default: module.ThreadMarkdown })),
);

const COLLAPSE_TOOL_GROUP_AT = 3;
const STICK_THRESHOLD_PX = 48;

function toolIcon(name: string) {
  if (/shell|bash|exec|command|terminal|run/i.test(name)) return <TerminalIcon />;
  if (/read|write|edit|file|patch|apply/i.test(name)) return <FileIcon />;
  if (/search|grep|find|glob|fetch|web/i.test(name)) return <SearchIcon />;
  return <ToolIcon />;
}

function ToolRow({ call }: { call: ToolCall }) {
  const [open, setOpen] = useState(false);
  const outputId = useId();
  const label = (
    <>
      <span className="tool-icon">{toolIcon(call.name)}</span>
      <span className="tool-name">{call.name}</span>
      {call.summary && <span className="tool-summary">{call.summary}</span>}
      <span className={`tool-state ${call.state}`}>
        {call.state === "running" && <span className="spinner" aria-hidden="true" />}
        {call.state === "done" && <CheckIcon />}
        {call.state === "interrupted" && "Stopped"}
        <span className="sr-only">
          {call.state === "running" ? "Running" : call.state === "done" ? "Done" : ""}
        </span>
      </span>
    </>
  );
  return (
    <li className="tool-row">
      {call.output ? (
        <>
          <button
            type="button"
            className="tool-line"
            aria-expanded={open}
            aria-controls={outputId}
            onClick={() => setOpen((value) => !value)}
          >
            {label}
            <ChevronIcon className="tool-chevron" />
          </button>
          {open && (
            <pre id={outputId} className="tool-output">
              {call.output}
            </pre>
          )}
        </>
      ) : (
        <div className="tool-line">{label}</div>
      )}
    </li>
  );
}

function ToolGroup({ calls }: { calls: ToolCall[] }) {
  const running = calls.some((call) => call.state === "running");
  const [expanded, setExpanded] = useState<boolean>();
  const rows = (
    <ul className="tool-rows">
      {calls.map((call) => (
        <ToolRow key={call.key} call={call} />
      ))}
    </ul>
  );
  if (calls.length < COLLAPSE_TOOL_GROUP_AT) return <div className="tool-group">{rows}</div>;
  const open = expanded ?? running;
  return (
    <div className="tool-group">
      <button
        type="button"
        className="tool-line tool-group-toggle"
        aria-expanded={open}
        onClick={() => setExpanded(!open)}
      >
        <span className="tool-icon">
          <ToolIcon />
        </span>
        <span className="tool-name">
          {running ? `Running ${calls.length} tools` : `Ran ${calls.length} tools`}
        </span>
        <ChevronIcon className="tool-chevron" />
      </button>
      {open && rows}
    </div>
  );
}

const optionLabels: Record<string, string> = {
  allow: "Approve",
  approve: "Approve",
  yes: "Approve",
  deny: "Deny",
  reject: "Deny",
  no: "Deny",
};

export const permissionOptionLabel = (option: string) => {
  const known = optionLabels[option.toLowerCase()];
  if (known) return known;
  const words = option.replace(/[_-]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
};

const isDenial = (option: string) => /^(deny|reject|no|cancel|abort)/i.test(option);

function answerText(request: ChatRequest) {
  if (request.type === "input") return "Answered";
  const choice = request.answer ?? "";
  if (/^(allow|approve|yes)/i.test(choice)) return "Approved";
  if (isDenial(choice)) return "Denied";
  return choice && choice !== "answered" ? `Chose ${permissionOptionLabel(choice)}` : "Answered";
}

function RequestCard({
  thread,
  request,
  actionable,
  onAnswered,
  onConflict,
}: {
  thread: Thread;
  request: ChatRequest;
  actionable: boolean;
  onAnswered: (id: string, answer: string) => void;
  onConflict: () => Promise<void>;
}) {
  const [input, setInput] = useState("");
  const inputId = useId();
  const mutation = useMutation({
    mutationFn: (value: { choice?: string; input?: string }) =>
      api.respondThread(thread.id, {
        expectedResourceVersion: thread.resourceVersion,
        responseTo: request.id,
        ...value,
      }),
    onSuccess: (_result, value) => {
      setInput("");
      onAnswered(request.id, value.choice ?? "answered");
    },
    onError: async (error) => {
      if (normalizeAPIError(error).code === "conflict") await onConflict();
    },
  });
  const isInput = request.type === "input";
  if (!actionable) {
    const answered = request.answer !== undefined;
    return (
      <div className={`chat-request-summary ${answered ? "answered" : "unanswered"}`}>
        {answered ? <CheckIcon /> : <WarningIcon />}
        <span className="chat-request-outcome">
          {answered ? answerText(request) : "Not answered"}
        </span>
        <span className="chat-request-text">
          {safeInline(request.summary) || (isInput ? "Input request" : "Permission request")}
        </span>
      </div>
    );
  }
  return (
    <section className="chat-request" aria-label={isInput ? "Input required" : "Permission required"}>
      <header>
        <strong>{isInput ? "Input required" : "Permission required"}</strong>
        {request.kind && <span className="chat-request-kind">{request.kind}</span>}
      </header>
      {request.summary && <p className="chat-request-body">{request.summary}</p>}
      {isInput ? (
        <form
          className="chat-request-input"
          onSubmit={(event) => {
            event.preventDefault();
            if (input !== "") mutation.mutate({ input });
          }}
        >
          <label className="sr-only" htmlFor={inputId}>
            Response
          </label>
          <input
            id={inputId}
            type={request.secret ? "password" : "text"}
            autoComplete="off"
            placeholder={request.secret ? "Secret response" : "Type a response"}
            value={input}
            onChange={(event) => setInput(event.target.value)}
          />
          <button disabled={mutation.isPending || input === ""}>Send response</button>
        </form>
      ) : (
        <div className="chat-request-actions">
          {request.options.map((option) => (
            <button
              key={option}
              type="button"
              className={isDenial(option) ? "secondary" : undefined}
              disabled={mutation.isPending}
              onClick={() => mutation.mutate({ choice: option })}
            >
              {permissionOptionLabel(option)}
            </button>
          ))}
        </div>
      )}
      {mutation.isError && <ThreadError error={mutation.error} />}
    </section>
  );
}

const activityLabels: Partial<Record<ChatActivity, string>> = {
  starting: "Starting…",
  working: "Working…",
  stopping: "Stopping…",
};

function ChatEntry({
  item,
  thread,
  onAnswered,
  onConflict,
}: {
  item: ChatItem;
  thread: Thread;
  onAnswered: (id: string, answer: string) => void;
  onConflict: () => Promise<void>;
}) {
  switch (item.kind) {
    case "user":
      return <li className="chat-user"><div className="chat-bubble">{item.text}</div></li>;
    case "assistant":
      return (
        <li className={`chat-assistant${item.streaming ? " streaming" : ""}`}>
          <Suspense fallback={<div className="markdown plain">{item.text}</div>}>
            <ThreadMarkdown text={item.text} />
          </Suspense>
        </li>
      );
    case "tools":
      return <li className="chat-tools"><ToolGroup calls={item.calls} /></li>;
    case "request":
      return (
        <li className="chat-request-entry">
          <RequestCard
            thread={thread}
            request={item.request}
            actionable={item.actionable}
            onAnswered={onAnswered}
            onConflict={onConflict}
          />
        </li>
      );
    case "error":
      return (
        <li className="chat-error" role="alert">
          <WarningIcon />
          <span>{safeInline(item.text)}</span>
          {item.code && <span className="chat-error-code">{item.code}</span>}
        </li>
      );
    case "notice":
      return <li className="chat-notice">{item.text}</li>;
  }
}

function RawEvents({ blocks }: { blocks: ThreadBlock[] }) {
  if (blocks.length === 0) return <p className="chat-empty">No events yet.</p>;
  return (
    <ol className="raw-events" aria-label="Raw Thread events">
      {blocks.map((block) => (
        <li key={block.id}>
          <div className="raw-event-heading">
            <strong>{safeInline(block.event?.type ?? block.kind)}</strong>
            <span>
              {safeInline(block.role)} · {safeInline(block.messageKind)}
            </span>
            <span className="mono">
              #{block.messageSequence}.{block.sequence}
            </span>
          </div>
          <pre>
            {boundedThreadText(
              JSON.stringify(
                { event: block.event, content: block.content },
                null,
                2,
              ),
            )}
          </pre>
        </li>
      ))}
    </ol>
  );
}

function useStickToBottom(content: unknown) {
  const ref = useRef<HTMLDivElement>(null);
  const stuck = useRef(true);
  const [away, setAway] = useState(false);
  const pin = useCallback(() => {
    const element = ref.current;
    if (element && stuck.current) element.scrollTop = element.scrollHeight;
  }, []);
  const onScroll = useCallback(() => {
    const element = ref.current;
    if (!element) return;
    const distance = element.scrollHeight - element.scrollTop - element.clientHeight;
    stuck.current = distance < STICK_THRESHOLD_PX;
    setAway(!stuck.current);
  }, []);
  const jump = useCallback(() => {
    stuck.current = true;
    setAway(false);
    pin();
  }, [pin]);
  useLayoutEffect(pin, [content, pin]);
  useEffect(() => {
    const element = ref.current?.firstElementChild;
    if (!element || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(pin);
    observer.observe(element);
    return () => observer.disconnect();
  }, [pin]);
  return { ref, onScroll, away, jump };
}

function ThreadComposer({
  thread,
  activity,
  stopPending,
  onStop,
  onSent,
  onConflict,
}: {
  thread: Thread;
  activity: ChatActivity;
  stopPending: boolean;
  onStop?: () => void;
  onSent: () => void;
  onConflict: () => Promise<void>;
}) {
  const [message, setMessage] = useState("");
  const textarea = useRef<HTMLTextAreaElement>(null);
  const mutation = useMutation({
    mutationFn: () => api.sendThreadMessage(thread.id, thread.resourceVersion, message),
    onSuccess: () => {
      setMessage("");
      onSent();
    },
    onError: async (error) => {
      if (normalizeAPIError(error).code === "conflict") await onConflict();
    },
  });
  useLayoutEffect(() => {
    const element = textarea.current;
    if (!element) return;
    element.style.height = "auto";
    element.style.height = `${element.scrollHeight}px`;
  }, [message]);
  const submit = () => {
    if (message.trim() && !mutation.isPending) mutation.mutate();
  };
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault();
      submit();
    }
  };
  const turnRunning = activity !== "idle";
  return (
    <section className="thread-composer" aria-label="Thread message composer">
      <form
        className="composer-box"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <label className="sr-only" htmlFor="thread-message">
          Message
        </label>
        <textarea
          id="thread-message"
          ref={textarea}
          rows={1}
          maxLength={128 * 1024}
          value={message}
          onChange={(event) => setMessage(event.target.value)}
          onKeyDown={onKeyDown}
          placeholder={turnRunning ? "Send a follow-up…" : "Message the agent…"}
          title="Enter to send · Shift+Enter for a newline"
        />
        <div className="composer-actions">
          {turnRunning && onStop && (
            <button
              type="button"
              className="composer-stop"
              aria-label="Stop"
              title="Stop"
              disabled={stopPending || activity === "stopping"}
              onClick={onStop}
            >
              <StopIcon />
            </button>
          )}
          <button
            type="submit"
            className="composer-send"
            aria-label="Send"
            title="Send"
            disabled={!message.trim() || mutation.isPending}
          >
            <SendIcon />
          </button>
        </div>
      </form>
      {mutation.isError && <ThreadError error={mutation.error} />}
    </section>
  );
}

export function ThreadConversation({
  thread,
  blocks,
  raw,
  banners,
  canCompose,
  stopPending,
  onStop,
  onRefresh,
}: {
  thread: Thread;
  blocks: ThreadBlock[];
  raw: boolean;
  banners?: ReactNode;
  canCompose: boolean;
  stopPending: boolean;
  onStop?: () => void;
  onRefresh: () => Promise<void>;
}) {
  const [answers, setAnswers] = useState<ReadonlyMap<string, string>>(new Map());
  const chat = useMemo(
    () => buildThreadChat(blocks, thread.currentRunState, answers),
    [answers, blocks, thread.currentRunState],
  );
  const scroll = useStickToBottom(raw ? blocks : chat);
  const onAnswered = useCallback(
    (id: string, answer: string) => {
      setAnswers((current) => new Map(current).set(id, answer));
      void onRefresh();
    },
    [onRefresh],
  );
  const label = activityLabels[chat.activity];
  return (
    <>
      <div className="thread-scroll-frame">
        <div className="thread-scroll" ref={scroll.ref} onScroll={scroll.onScroll}>
          <div className="thread-scroll-content">
            {banners}
            {raw ? (
              <RawEvents blocks={blocks} />
            ) : chat.items.length === 0 && !label ? (
              <p className="chat-empty">No messages yet.</p>
            ) : (
              <ol className="chat-transcript" aria-label="Thread transcript">
                {chat.items.map((item) => (
                  <ChatEntry
                    key={item.key}
                    item={item}
                    thread={thread}
                    onAnswered={onAnswered}
                    onConflict={onRefresh}
                  />
                ))}
                {label && (
                  <li className="chat-working" role="status">
                    <span className="shimmer">{label}</span>
                  </li>
                )}
              </ol>
            )}
          </div>
        </div>
        {scroll.away && (
          <button type="button" className="chat-jump" onClick={scroll.jump}>
            Jump to latest
          </button>
        )}
      </div>
      {canCompose && (
        <ThreadComposer
          thread={thread}
          activity={chat.activity}
          stopPending={stopPending}
          onStop={onStop}
          onSent={() => {
            scroll.jump();
            void onRefresh();
          }}
          onConflict={onRefresh}
        />
      )}
    </>
  );
}
