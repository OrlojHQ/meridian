import { streamRunEvents } from "./generated/sdk.gen";
import type { RunEvent } from "./generated/types.gen";
import { streamFetch } from "./streamFetch";

export type StreamStatus = "connecting" | "live" | "reconnecting" | "closed";

function isRunEvent(value: unknown): value is RunEvent {
  if (typeof value !== "object" || value === null) return false;
  const event = value as Partial<RunEvent>;
  return (
    typeof event.sequence === "number" &&
    typeof event.type === "string" &&
    typeof event.timestamp === "string"
  );
}

export async function followRunEvents(
  runId: string,
  after: number,
  signal: AbortSignal,
  onEvent: (event: RunEvent) => void,
  onStatus: (status: StreamStatus) => void,
): Promise<void> {
  onStatus("connecting");
  const result = await streamRunEvents({
    path: { runId },
    query: { after },
    signal,
    fetch: streamFetch(signal),
    sseDefaultRetryDelay: 500,
    sseMaxRetryDelay: 5_000,
    sseMaxRetryAttempts: 8,
    onSseError: () => {
      if (!signal.aborted) onStatus("reconnecting");
    },
  });
  onStatus("live");
  for await (const value of result.stream) {
    if (isRunEvent(value)) onEvent(value);
  }
  onStatus("closed");
}
