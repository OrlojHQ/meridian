import type { RunState, Thread, ThreadBlock } from "../api/generated/types.gen";

import {
  isCoordinationWakeEcho,
  parseSubagentReports,
  type SubagentReport,
} from "./subagentReportModel";
import { boundedThreadText, safeInline } from "./threadText";

export { boundedThreadText, safeInline, sanitizeThreadText } from "./threadText";

export const MAX_FAILURE_TEXT = 280;

// Run and Capsule failure text can carry adapter or runtime output, so it is
// shown as one bounded line with bidirectional overrides removed.
export const failureText = (value = "") => {
  const characters = Array.from(
    safeInline(value).replace(/[\u202a-\u202e\u2066-\u2069]/g, "").replace(/ {2,}/g, " ").trim(),
  );
  return characters.length > MAX_FAILURE_TEXT
    ? `${characters.slice(0, MAX_FAILURE_TEXT - 1).join("")}…`
    : characters.join("");
};

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
  | { kind: "user"; key: string; text: string; origin?: "from_lead" | "from_subagents" }
  | {
      kind: "subagent_reports";
      key: string;
      reports: SubagentReport[];
      notices: string[];
      count: number;
      wakeText: string;
    }
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

export type EndedRunState = Extract<RunState, "Failed" | "Cancelled" | "Succeeded">;

// An active Thread whose current Run is terminal accepts no messages until a
// new session starts. Paused and archived Threads have their own controls.
export const endedSessionState = (
  thread: Pick<Thread, "state" | "currentRunState">,
): EndedRunState | undefined => {
  const state = thread.currentRunState;
  return thread.state === "active" &&
    (state === "Failed" || state === "Cancelled" || state === "Succeeded")
    ? state
    : undefined;
};

// The operator's own latest message. Coordination deliveries are agent-authored
// and never offered back as operator input.
export function lastOperatorPrompt(blocks: ThreadBlock[]) {
  for (let index = blocks.length - 1; index >= 0; index -= 1) {
    const block = blocks[index];
    if (!block.event && block.role === "user" && block.kind === "text" && !block.origin && block.content) {
      return boundedThreadText(block.content);
    }
  }
  return undefined;
}

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

// Adapters may restart their message IDs in each session, so once a message
// has its final text, a later delta or final with that ID is a new message.
function assistantMessageKeys(blocks: ThreadBlock[]) {
  const keys = new Map<string, string>();
  const finals = new Map<string, string>();
  const current = new Map<string, { key: string; final: boolean }>();
  let generation = 0;
  for (const block of blocks) {
    const type = block.event?.type;
    if (type !== "assistant_delta" && type !== "assistant_message") continue;
    const id = block.event?.messageId ?? block.messageId;
    let entry = current.get(id);
    if (!entry || entry.final) {
      entry = { key: entry ? `${id}#${(generation += 1)}` : id, final: false };
      current.set(id, entry);
    }
    keys.set(block.id, entry.key);
    if (type === "assistant_message") {
      entry.final = true;
      finals.set(entry.key, block.content ?? "");
    }
  }
  return { keys, finals };
}

export function buildThreadChat(
  blocks: ThreadBlock[],
  runState?: RunState,
  localAnswers: ReadonlyMap<string, string> = new Map(),
): ThreadChat {
  const sessionActive = isActiveRunState(runState);
  const { keys: assistantKeys, finals } = assistantMessageKeys(blocks);
  const answers = answeredRequests(blocks);
  const items: ChatItem[] = [];
  const assistants = new Map<string, Extract<ChatItem, { kind: "assistant" }>>();
  // Labels from the models reports seen so far name an operator's switch.
  const modelLabels = new Map<string, string>();
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
        if (block.origin?.kind === "from_subagents") {
          const parsed = parseSubagentReports(block.content, block.origin.linkIds ?? []);
          items.push({ kind: "subagent_reports", key: block.id, ...parsed });
        } else {
          items.push({
            kind: "user",
            key: block.id,
            text: boundedThreadText(block.content),
            ...(block.origin ? { origin: block.origin.kind } : {}),
          });
        }
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
        assistant(
          assistantKeys.get(block.id) ?? messageKey,
          event.summary ?? block.content ?? "",
          false,
        );
        break;
      case "assistant_message":
        assistant(assistantKeys.get(block.id) ?? messageKey, block.content ?? "", true);
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
        if (event.code === "model_unavailable") {
          items.push({
            kind: "error",
            key: block.id,
            text: `Model not switched: ${safeInline(event.summary ?? "").slice(0, 300) || "the harness refused"}`,
          });
          break;
        }
        items.push({
          kind: "error",
          key: block.id,
          text: boundedThreadText(event.reason || "The agent reported an error."),
          code: event.code ? safeInline(event.code) : undefined,
        });
        break;
      case "models":
        for (const option of event.models?.options ?? []) {
          modelLabels.set(option.id, safeInline(option.label).slice(0, 128));
        }
        break;
      case "set_model": {
        const id = event.model ?? "";
        const label = modelLabels.get(id) || safeInline(id).slice(0, 128);
        items.push({ kind: "notice", key: block.id, text: `Model → ${label}` });
        break;
      }
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
  if (items.some((item) => item.kind === "subagent_reports")) {
    return {
      items: items.filter((item) => item.kind !== "assistant" || !isCoordinationWakeEcho(item.text)),
      activity,
    };
  }
  return { items, activity };
}
