import { streamThreadBlocks } from "./generated/sdk.gen";
import type { ThreadBlock } from "./generated/types.gen";

export type ThreadStreamStatus =
  | "connecting"
  | "live"
  | "reconnecting"
  | "closed";

function isThreadBlock(value: unknown): value is ThreadBlock {
  if (typeof value !== "object" || value === null) return false;
  const block = value as Partial<ThreadBlock>;
  return (
    typeof block.id === "string" &&
    typeof block.messageId === "string" &&
    typeof block.messageSequence === "number" &&
    typeof block.sequence === "number" &&
    typeof block.role === "string" &&
    typeof block.kind === "string" &&
    typeof block.createdAt === "string"
  );
}

const delay = (milliseconds: number, signal: AbortSignal) =>
  new Promise<void>((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const timer = window.setTimeout(resolve, milliseconds);
    signal.addEventListener(
      "abort",
      () => {
        window.clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });

export async function followThreadBlocks(
  threadId: string,
  initialCursor: number,
  signal: AbortSignal,
  onBlock: (block: ThreadBlock) => void,
  onStatus: (status: ThreadStreamStatus) => void,
  onGap: (expected: number, received: number) => void,
): Promise<void> {
  let cursor = initialCursor;
  let attempts = 0;
  const seen = new Set<string>();
  onStatus("connecting");
  while (!signal.aborted && attempts < 8) {
    try {
      const result = await streamThreadBlocks({
        path: { threadId },
        query: { after: cursor },
        headers: cursor > 0 ? { "Last-Event-ID": String(cursor) } : undefined,
        signal,
        sseDefaultRetryDelay: 500,
        sseMaxRetryDelay: 5_000,
        sseMaxRetryAttempts: 3,
        onSseError: () => {
          if (!signal.aborted) onStatus("reconnecting");
        },
      });
      if (signal.aborted) break;
      onStatus("live");
      for await (const value of result.stream) {
        if (signal.aborted || !isThreadBlock(value) || seen.has(value.id)) continue;
        if (value.messageSequence < cursor) continue;
        seen.add(value.id);
        if (value.messageSequence > cursor + 1) {
          onGap(cursor + 1, value.messageSequence);
        }
        cursor = Math.max(cursor, value.messageSequence);
        onBlock(value);
        if (seen.size > 2_000) {
          const keep = [...seen].slice(-1_000);
          seen.clear();
          keep.forEach((id) => seen.add(id));
        }
      }
      attempts += 1;
    } catch {
      if (signal.aborted) break;
      attempts += 1;
    }
    if (!signal.aborted && attempts < 8) {
      onStatus("reconnecting");
      await delay(Math.min(5_000, 250 * 2 ** attempts), signal);
    }
  }
  onStatus("closed");
}
