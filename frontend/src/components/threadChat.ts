import type { RunState, ThreadBlock } from "../api/generated/types.gen";

const MAX_CONTENT = 64 * 1024;

export const sanitizeThreadText = (value = "") =>
  value.replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g, "");

export const boundedThreadText = (value = "") => {
  const safe = sanitizeThreadText(value);
  return safe.length > MAX_CONTENT
    ? `${safe.slice(0, MAX_CONTENT)}\n… content truncated …`
    : safe;
};

export const safeInline = (value = "") =>
  sanitizeThreadText(value).replace(/[\n\t]+/g, " ");

export type ToolCall = {
  key: string;
  name: string;
  summary: string;
  output?: string;
  state: "running" | "done" | "interrupted";
};

export type ChatRequest = {
  id: string;
  type: "permission" | "input";
  kind: string;
  summary: string;
  options: string[];
  secret: boolean;
  answer?: string;
};

export type ChatItem =
  | { kind: "user"; key: string; text: string }
  | { kind: "assistant"; key: string; text: string; streaming: boolean }
  | { kind: "tools"; key: string; calls: ToolCall[] }
  | { kind: "request"; key: string; request: ChatRequest; actionable: boolean }
  | { kind: "error"; key: string; text: string; code?: string }
  | { kind: "notice"; key: string; text: string };

export type ChatActivity = "idle" | "starting" | "working" | "waiting" | "stopping";

export type ThreadChat = {
  items: ChatItem[];
  activity: ChatActivity;
};

const ACTIVE_RUN_STATES: RunState[] = ["Queued", "Starting", "Running", "Cancelling"];
const BUSY_STATUSES = new Set(["starting", "running", "restarting", "waiting"]);

export const isBusyAdapterStatus = (status?: string) =>
  status !== undefined && BUSY_STATUSES.has(status);

export const isActiveRunState = (state?: RunState) =>
  state !== undefined && ACTIVE_RUN_STATES.includes(state);

// Matches responses to their requests. Responses name their request through
// responseTo; older transcripts without it answer the latest open request.
function answeredRequests(blocks: ThreadBlock[]) {
  const answers = new Map<string, string>();
  const open: Record<"permission" | "input", string[]> = { permission: [], input: [] };
  for (const block of blocks) {
    const event = block.event;
    if (!event) continue;
    const id = event.messageId ?? block.messageId;
    if (event.type === "permission_request") open.permission.push(id);
    else if (event.type === "input_request") open.input.push(id);
    else if (event.type === "permission_response" || event.type === "input_response") {
      const type = event.type === "permission_response" ? "permission" : "input";
      const target = event.responseTo ?? open[type].at(-1);
      if (!target) continue;
      open[type] = open[type].filter((pending) => pending !== target);
      answers.set(
        target,
        type === "permission" ? event.permission?.choice ?? "answered" : "answered",
      );
    }
  }
  return answers;
}

export function buildThreadChat(
  blocks: ThreadBlock[],
  runState?: RunState,
  localAnswers: ReadonlyMap<string, string> = new Map(),
): ThreadChat {
  const sessionActive = isActiveRunState(runState);
  const finals = new Map<string, string>();
  for (const block of blocks) {
    if (block.event?.type === "assistant_message") {
      finals.set(block.event.messageId ?? block.messageId, block.content ?? "");
    }
  }
  const answers = answeredRequests(blocks);
  const items: ChatItem[] = [];
  const assistants = new Map<string, Extract<ChatItem, { kind: "assistant" }>>();
  const tools = new Map<string, ToolCall>();
  const turnOf = new Map<ToolCall, number>();
  let turn = 0;
  let busy = false;

  const pushTool = (call: ToolCall) => {
    turnOf.set(call, turn);
    const last = items.at(-1);
    if (last?.kind === "tools") last.calls.push(call);
    else items.push({ kind: "tools", key: `tools-${call.key}`, calls: [call] });
  };
  const assistant = (key: string, text: string, final: boolean) => {
    const existing = assistants.get(key);
    if (existing) {
      if (final) {
        existing.text = boundedThreadText(text);
        existing.streaming = false;
      } else if (existing.streaming) {
        existing.text = boundedThreadText(existing.text + text);
      }
      return;
    }
    const known = finals.get(key);
    const item = {
      kind: "assistant" as const,
      key: `assistant-${key}`,
      text: boundedThreadText(known ?? text),
      streaming: known === undefined,
    };
    assistants.set(key, item);
    items.push(item);
  };

  for (const block of blocks) {
    const event = block.event;
    if (!event) {
      if (block.role === "user" && block.kind === "text") {
        turn += 1;
        busy = true;
        items.push({ kind: "user", key: block.id, text: boundedThreadText(block.content) });
      } else if (block.kind === "error") {
        items.push({ kind: "error", key: block.id, text: boundedThreadText(block.content) });
      } else if (block.kind === "text" && block.content) {
        assistant(block.messageId, block.content, true);
      } else if (block.kind !== "json") {
        items.push({
          kind: "notice",
          key: block.id,
          text: `Unrecognized block · ${safeInline(block.kind)}`,
        });
      }
      continue;
    }
    const messageKey = event.messageId ?? block.messageId;
    switch (event.type) {
      case "assistant_delta":
        assistant(messageKey, event.summary ?? block.content ?? "", false);
        break;
      case "assistant_message":
        assistant(messageKey, block.content ?? "", true);
        break;
      case "tool_start": {
        const call: ToolCall = {
          key: block.id,
          name: safeInline(event.toolName ?? "tool"),
          summary: safeInline(event.summary ?? ""),
          state: "running",
        };
        if (event.toolCallId) tools.set(event.toolCallId, call);
        pushTool(call);
        break;
      }
      case "tool_result": {
        const started = event.toolCallId ? tools.get(event.toolCallId) : undefined;
        const output = boundedThreadText(event.result ?? block.content);
        if (started) {
          started.output = output;
          started.state = "done";
        } else {
          pushTool({ key: block.id, name: "Tool result", summary: "", output, state: "done" });
        }
        break;
      }
      case "status":
      case "end":
        if (event.status) busy = isBusyAdapterStatus(event.status);
        if (event.type === "end") busy = false;
        if (event.status === "failed") {
          items.push({ kind: "error", key: block.id, text: "The session failed." });
        } else if (event.status === "cancelled") {
          items.push({ kind: "notice", key: block.id, text: "Stopped" });
        }
        break;
      case "error":
        items.push({
          kind: "error",
          key: block.id,
          text: boundedThreadText(event.reason || "The agent reported an error."),
          code: event.code ? safeInline(event.code) : undefined,
        });
        break;
      case "permission_request":
      case "input_request": {
        const isInput = event.type === "input_request";
        const answer = answers.get(messageKey) ?? localAnswers.get(messageKey);
        items.push({
          kind: "request",
          key: block.id,
          actionable: answer === undefined && sessionActive,
          request: {
            id: messageKey,
            type: isInput ? "input" : "permission",
            kind: safeInline(event.permission?.kind ?? ""),
            summary: boundedThreadText(
              isInput ? event.input?.prompt : event.permission?.summary,
            ),
            options: event.permission?.options?.length
              ? event.permission.options.map((option) => safeInline(option))
              : ["allow", "deny"],
            secret: event.input?.secret === true,
            answer,
          },
        });
        break;
      }
      case "permission_response":
      case "input_response":
      case "commands":
        break;
      case "gap":
        items.push({ kind: "notice", key: block.id, text: "Some earlier events are unavailable." });
        break;
      default:
        items.push({
          kind: "notice",
          key: block.id,
          text: `Unrecognized event · ${safeInline(String(event.type))}`,
        });
    }
  }

  let activity: ChatActivity = "idle";
  if (runState === "Queued" || runState === "Starting") activity = "starting";
  else if (runState === "Cancelling") activity = "stopping";
  else if (sessionActive && busy) {
    activity = items.some((item) => item.kind === "request" && item.actionable)
      ? "waiting"
      : "working";
  }
  const running = activity === "working" || activity === "waiting";
  for (const [call, callTurn] of turnOf) {
    if (call.state === "running" && !(running && callTurn === turn)) {
      call.state = "interrupted";
    }
  }
  for (const item of assistants.values()) {
    if (!running) item.streaming = false;
  }
  return { items, activity };
}
