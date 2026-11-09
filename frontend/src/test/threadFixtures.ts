import type {
  ThreadAdapterEvent,
  ThreadBlock,
  ThreadBlockKind,
  ThreadCommand,
  ThreadMessageKind,
  ThreadMessageRole,
  ThreadModels,
} from "../api/generated/types.gen";

// Builds blocks the way the server maps adapter frames (mapStructuredEvent in
// internal/app/threads.go), so fixtures read like recorded transcripts.
export function transcript(start = 1) {
  let sequence = start - 1;
  const blocks: ThreadBlock[] = [];
  const push = (
    role: ThreadMessageRole,
    messageKind: ThreadMessageKind,
    kind: ThreadBlockKind,
    event?: ThreadAdapterEvent,
    content?: string,
  ) => {
    sequence += 1;
    const block: ThreadBlock = {
      id: `block-${sequence}`,
      messageId: `message-${sequence}`,
      messageSequence: sequence,
      sequence: 1,
      role,
      messageKind,
      kind,
      content,
      event,
      createdAt: "2026-11-07T00:00:00Z",
    };
    blocks.push(block);
    return block;
  };
  const api = {
    blocks,
    status: (status: NonNullable<ThreadAdapterEvent["status"]>) =>
      push("system", "status", "json", { type: "status", status }),
    user: (text: string) => push("user", "prompt", "text", undefined, text),
    delta: (messageId: string, summary: string) =>
      push("assistant", "status", "text", { type: "assistant_delta", messageId, summary }),
    final: (messageId: string, content: string) =>
      push("assistant", "response", "text", { type: "assistant_message", messageId }, content),
    tool: (toolCallId: string, toolName: string, summary: string) =>
      push("assistant", "tool_call", "tool_call", {
        type: "tool_start",
        toolCallId,
        toolName,
        summary,
      }),
    result: (toolCallId: string, result: string) =>
      push("tool", "tool_result", "tool_result", { type: "tool_result", toolCallId, result }),
    permission: (messageId: string, summary: string, options = ["allow", "deny"], kind = "shell") =>
      push("system", "status", "json", {
        type: "permission_request",
        messageId,
        permission: { kind, summary, options },
      }),
    permissionResponse: (responseTo: string, choice: string) =>
      push("user", "status", "json", {
        type: "permission_response",
        responseTo,
        permission: { kind: "permission", choice },
      }),
    input: (messageId: string, prompt: string, secret = false) =>
      push("system", "status", "json", {
        type: "input_request",
        messageId,
        input: { prompt, secret },
      }),
    error: (code: string, reason: string) =>
      push("system", "status", "error", { type: "error", code, reason }),
    commands: (commands: ThreadCommand[]) =>
      push("system", "status", "json", { type: "commands", commands }),
    models: (models: ThreadModels) =>
      push("system", "status", "json", { type: "models", models }),
    setModel: (model: string) =>
      push("user", "status", "json", { type: "set_model", model }),
    modelError: (summary: string) =>
      push("system", "status", "error", { type: "error", code: "model_unavailable", summary }),
  };
  return api;
}

// The sequence behind the reported screenshot: blocks 11-14 of a mock Thread.
export function reportedScreenshotTranscript() {
  const t = transcript(9);
  t.delta("mock-message-1", "Hello ");
  t.delta("mock-message-1", "World!");
  t.final("mock-message-1", "Hello World!");
  t.status("idle");
  t.user("prompt");
  t.status("running");
  return t.blocks;
}
