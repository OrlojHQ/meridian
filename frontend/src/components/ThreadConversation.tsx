import { useMutation, useQuery } from "@tanstack/react-query";
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
import type { HarnessProfile, Thread, ThreadBlock, ThreadLink } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { plainAgentText } from "./agentText";
import {
  adapterCanSteer,
  composerBusyHint,
  profileAdapterKind,
} from "./composerDelivery";
import { SubagentNeedsYouTray, SubagentReportGroup } from "./SubagentReports";
import {
  boundedThreadText,
  buildThreadChat,
  type ChatActivity,
  type ChatItem,
  type ChatRequest,
  safeInline,
  type ToolCall,
} from "./threadChat";
import { harnessName } from "../shell/capsuleActivity";
import {
  filterSlashEntries,
  latestThreadCommands,
  type SlashCommand,
  type SlashControl,
  type SlashEntry,
  slashQuery,
} from "./slashCommands";
import { ThreadError } from "./ThreadError";
import {
  CheckIcon,
  ChevronIcon,
  CloseIcon,
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

const MAX_OPTION_LABEL = 48;

export const permissionOptionLabel = (option: string) => {
  const known = optionLabels[option.toLowerCase()];
  if (known) return known;
  const words = plainAgentText(option.replace(/[_-]+/g, " "), MAX_OPTION_LABEL);
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

export function RequestCard({
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
  links,
  onAnswered,
  onConflict,
}: {
  item: ChatItem;
  thread: Thread;
  links: readonly ThreadLink[];
  onAnswered: (id: string, answer: string) => void;
  onConflict: () => Promise<void>;
}) {
  switch (item.kind) {
    case "user":
      return (
        <li className={`chat-user${item.origin ? " chat-coordination" : ""}`}>
          {item.origin && (
            <span className="chat-origin">
              {item.origin === "from_lead" ? "From the lead" : "From subagents"}
            </span>
          )}
          <div className="chat-bubble">{item.text}</div>
        </li>
      );
    case "subagent_reports":
      return (
        <li className="chat-subagent-reports">
          <SubagentReportGroup item={item} links={links} />
        </li>
      );
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

const commandKindLabels: Record<SlashCommand["kind"], string> = {
  prompt: "Prompt",
  action: "Action",
};

function SlashMenu({
  id,
  harness,
  commands,
  entries,
  active,
  onHover,
  onPick,
}: {
  id: string;
  harness: string;
  commands?: SlashCommand[];
  entries: SlashEntry[];
  active: number;
  onHover: (index: number) => void;
  onPick: (entry: SlashEntry) => void;
}) {
  const list = useRef<HTMLUListElement>(null);
  useEffect(() => {
    list.current
      ?.querySelector<HTMLElement>(`[data-index="${active}"]`)
      ?.scrollIntoView?.({ block: "nearest" });
  }, [active]);
  const firstControl = entries.findIndex((entry) => entry.type === "control");
  const commandCount = firstControl === -1 ? entries.length : firstControl;
  const option = (entry: SlashEntry, index: number) => (
    <li
      key={entry.key}
      id={`${id}-${index}`}
      data-index={index}
      role="option"
      aria-selected={index === active}
      className={`slash-option${index === active ? " active" : ""}`}
      onMouseDown={(event) => event.preventDefault()}
      onMouseMove={() => index !== active && onHover(index)}
      onClick={() => onPick(entry)}
    >
      {entry.type === "command" ? (
        <>
          <span className="slash-name">
            /{entry.command.name}
            {entry.command.hint && <span className="slash-hint"> {entry.command.hint}</span>}
            {entry.alias && <span className="slash-alias"> · /{entry.alias}</span>}
          </span>
          <span className="slash-description">{entry.command.description}</span>
          <span className={`slash-kind ${entry.command.kind}`}>
            {commandKindLabels[entry.command.kind]}
          </span>
        </>
      ) : (
        <>
          <span className="slash-name">{entry.control.label}</span>
          <span className="slash-description">{entry.control.description}</span>
        </>
      )}
    </li>
  );
  const agent = safeInline(harnessName(harness)) || "the agent";
  return (
    <div className="slash-menu">
      <ul id={id} ref={list} role="listbox" aria-label="Commands">
        <li role="presentation" className="slash-group">{agent} commands</li>
        {commandCount === 0 && (
          <li role="presentation" className="slash-empty">
            {commands === undefined
              ? `${agent} has not reported commands for this session.`
              : commands.length === 0
                ? `${agent} reported no commands.`
                : "No matching commands. Enter sends the text as a message."}
          </li>
        )}
        {entries.slice(0, commandCount).map((entry, index) => option(entry, index))}
        {firstControl !== -1 && (
          <>
            <li role="presentation" className="slash-group">Meridian</li>
            {entries.slice(firstControl).map((entry, index) => option(entry, firstControl + index))}
          </>
        )}
      </ul>
    </div>
  );
}

type QueuedFollowUp = { id: number; text: string };

function ThreadComposer({
  thread,
  activity,
  adapterKind,
  commands,
  ended,
  stopPending,
  onStop,
  onNewSession,
  onSent,
  onConflict,
}: {
  thread: Thread;
  activity: ChatActivity;
  adapterKind?: HarnessProfile["adapterKind"];
  commands?: SlashCommand[];
  ended: boolean;
  stopPending: boolean;
  onStop?: () => void;
  onNewSession?: () => void;
  onSent: () => void;
  onConflict: () => Promise<void>;
}) {
  const [message, setMessage] = useState("");
  const [queue, setQueue] = useState<QueuedFollowUp[]>([]);
  const [active, setActive] = useState(0);
  const [dismissed, setDismissed] = useState<string>();
  const textarea = useRef<HTMLTextAreaElement>(null);
  const nextQueueId = useRef(1);
  const queueRef = useRef(queue);
  queueRef.current = queue;
  const runningRef = useRef(false);
  const flushingRef = useRef(false);
  const menuId = useId();
  const mutation = useMutation({
    mutationFn: (delivery?: "steer") =>
      delivery === "steer"
        ? api.sendThreadMessage(thread.id, thread.resourceVersion, message, delivery)
        : api.sendThreadMessage(thread.id, thread.resourceVersion, message),
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
  const turnRunning = activity !== "idle";
  const canSteer = adapterCanSteer(adapterKind);
  const canStop = turnRunning && onStop !== undefined && !stopPending && activity !== "stopping";
  const controls = useMemo(() => {
    const available: SlashControl[] = [];
    if (canStop) {
      available.push({ id: "stop", label: "Stop", description: "Stop the current turn" });
    }
    if (onNewSession) {
      available.push({
        id: "new-session",
        label: "New session",
        description: "Start a fresh conversation in this Capsule",
      });
    }
    return available;
  }, [canStop, onNewSession]);
  const query = slashQuery(message);
  const menuOpen = query !== undefined && dismissed !== message;
  const entries = useMemo(
    () => (query === undefined ? [] : filterSlashEntries(query, commands ?? [], controls)),
    [commands, controls, query],
  );
  const selected = Math.min(active, Math.max(entries.length - 1, 0));
  const enqueue = (text: string) => {
    const trimmed = text.trim();
    if (!trimmed) return;
    setQueue((current) => [...current, { id: nextQueueId.current++, text: trimmed }]);
  };
  const takeBack = (item: QueuedFollowUp) => {
    setQueue((current) => current.filter((entry) => entry.id !== item.id));
    setMessage((current) => (current.trim() ? `${current.replace(/\s+$/, "")}\n${item.text}` : item.text));
    textarea.current?.focus();
  };
  const removeQueued = (item: QueuedFollowUp) => {
    setQueue((current) => current.filter((entry) => entry.id !== item.id));
  };
  const submit = () => {
    if (!message.trim() || mutation.isPending || ended) return;
    if (turnRunning) {
      enqueue(message);
      setMessage("");
      return;
    }
    mutation.mutate(undefined);
  };
  const steer = () => {
    if (!message.trim() || mutation.isPending || ended || !canSteer) return;
    mutation.mutate("steer");
  };
  useEffect(() => {
    const wasRunning = runningRef.current;
    runningRef.current = turnRunning;
    if (!wasRunning || turnRunning || ended || flushingRef.current) return;
    const head = queueRef.current[0];
    if (!head) return;
    flushingRef.current = true;
    api
      .sendThreadMessage(thread.id, thread.resourceVersion, head.text)
      .then(() => {
        setQueue((current) => current.filter((item) => item.id !== head.id));
        onSent();
      })
      .catch(async (error) => {
        if (normalizeAPIError(error).code === "conflict") await onConflict();
      })
      .finally(() => {
        flushingRef.current = false;
      });
  }, [ended, onConflict, onSent, thread.id, thread.resourceVersion, turnRunning]);
  const pick = (entry: SlashEntry) => {
    if (entry.type === "control") {
      setMessage("");
      if (entry.control.id === "stop") onStop?.();
      else onNewSession?.();
      return;
    }
    setMessage(`/${entry.command.name} `);
    textarea.current?.focus();
  };
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.nativeEvent.isComposing) return;
    if (menuOpen && entries.length > 0) {
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        const step = event.key === "ArrowDown" ? 1 : -1;
        setActive((selected + step + entries.length) % entries.length);
        return;
      }
      if (event.key === "Tab" || (event.key === "Enter" && !event.shiftKey)) {
        event.preventDefault();
        pick(entries[selected]);
        return;
      }
    }
    if (menuOpen && event.key === "Escape") {
      event.preventDefault();
      setDismissed(message);
      return;
    }
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter" && turnRunning && canSteer) {
      event.preventDefault();
      steer();
      return;
    }
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      submit();
    }
  };
  const placeholder = ended
    ? "This session has ended. Restart it to send a message."
    : turnRunning
      ? canSteer
        ? "Queue a follow-up, or steer at the next tool boundary"
        : "Queue a follow-up for after this turn"
      : "Message the agent, or type / for commands";
  const title = turnRunning
    ? canSteer
      ? "Enter to queue · ⌘Enter to steer · Shift+Enter for a newline"
      : "Enter to queue · Shift+Enter for a newline"
    : "Enter to send · Shift+Enter for a newline · / for commands";
  return (
    <section className="thread-composer" aria-label="Thread message composer">
      <div className="composer-frame">
        {queue.length > 0 && (
          <ul className="composer-queue" aria-label="Queued follow-ups">
            {queue.map((item) => (
              <li key={item.id} className="composer-queue-item">
                <p className="composer-queue-when">After this turn</p>
                <p className="composer-queue-text">{safeInline(item.text)}</p>
                <div className="composer-queue-actions">
                  <button type="button" className="secondary" onClick={() => takeBack(item)}>
                    Edit
                  </button>
                  <button
                    type="button"
                    className="composer-queue-remove"
                    aria-label="Remove queued follow-up"
                    title="Remove"
                    onClick={() => removeQueued(item)}
                  >
                    <CloseIcon />
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
        {menuOpen && (
          <SlashMenu
            id={menuId}
            harness={thread.harness}
            commands={commands}
            entries={entries}
            active={selected}
            onHover={setActive}
            onPick={pick}
          />
        )}
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
            disabled={ended}
            value={message}
            role="combobox"
            aria-autocomplete="list"
            aria-expanded={menuOpen}
            aria-controls={menuOpen ? menuId : undefined}
            aria-activedescendant={
              menuOpen && entries.length > 0 ? `${menuId}-${selected}` : undefined
            }
            onChange={(event) => {
              setMessage(event.target.value);
              setActive(0);
            }}
            onKeyDown={onKeyDown}
            onBlur={() => setDismissed(message)}
            onFocus={() => setDismissed(undefined)}
            placeholder={placeholder}
            title={title}
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
            {turnRunning ? (
              <>
                <button
                  type="submit"
                  className="composer-queue-send"
                  disabled={!message.trim() || mutation.isPending || ended}
                >
                  Queue
                </button>
                {canSteer && (
                  <button
                    type="button"
                    className="composer-steer"
                    title="Deliver at the next tool boundary"
                    disabled={!message.trim() || mutation.isPending || ended}
                    onClick={steer}
                  >
                    Steer
                  </button>
                )}
              </>
            ) : (
              <button
                type="submit"
                className="composer-send"
                aria-label="Send"
                title="Send"
                disabled={!message.trim() || mutation.isPending || ended}
              >
                <SendIcon />
              </button>
            )}
          </div>
        </form>
        {turnRunning && !ended && (
          <p className="composer-hint">{composerBusyHint(adapterKind, canSteer)}</p>
        )}
      </div>
      {mutation.isError && <ThreadError error={mutation.error} />}
    </section>
  );
}

export function ThreadConversation({
  thread,
  blocks,
  raw,
  banners,
  endedNotice,
  canCompose,
  stopPending,
  onStop,
  onNewSession,
  onRefresh,
}: {
  thread: Thread;
  blocks: ThreadBlock[];
  raw: boolean;
  banners?: ReactNode;
  // Present once the session has ended; it follows the transcript and the
  // composer stays disabled until a new session starts.
  endedNotice?: ReactNode;
  canCompose: boolean;
  stopPending: boolean;
  onStop?: () => void;
  onNewSession?: () => void;
  onRefresh: () => Promise<void>;
}) {
  const commands = useMemo(() => latestThreadCommands(blocks), [blocks]);
  const isLead = Boolean(thread.coordinationGrantId && !thread.parentThreadId);
  const profiles = useQuery(queries.harnessProfiles(thread.capsuleId));
  const adapterKind = profileAdapterKind(profiles.data?.items, thread.harness);
  const links = useQuery({
    ...queries.threadLinks(thread.id),
    enabled: isLead,
  });
  const subagents = links.data?.items ?? [];
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
                    links={subagents}
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
            {endedNotice && <div className="session-ended-frame">{endedNotice}</div>}
          </div>
        </div>
        {scroll.away && (
          <button type="button" className="chat-jump" onClick={scroll.jump}>
            Jump to latest
          </button>
        )}
      </div>
      {isLead && <SubagentNeedsYouTray links={subagents} />}
      {canCompose && (
        <ThreadComposer
          thread={thread}
          activity={chat.activity}
          adapterKind={adapterKind}
          commands={commands}
          ended={Boolean(endedNotice)}
          stopPending={stopPending}
          onStop={onStop}
          onNewSession={onNewSession}
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
