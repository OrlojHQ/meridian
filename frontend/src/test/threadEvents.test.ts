import { expect, it, vi } from "vitest";

import type { ThreadBlock } from "../api/generated/types.gen";

const { streamThreadBlocks } = vi.hoisted(() => ({
  streamThreadBlocks: vi.fn(),
}));
vi.mock("../api/generated/sdk.gen", () => ({ streamThreadBlocks }));

import { followThreadBlocks } from "../api/threadEvents";

const block = (id: string, messageSequence: number): ThreadBlock => ({
  id,
  messageId: `message-${messageSequence}`,
  messageSequence,
  sequence: 1,
  role: "assistant",
  messageKind: "response",
  kind: "text",
  content: id,
  createdAt: "2026-08-23T00:00:00Z",
});

const stream = (items: ThreadBlock[]) =>
  (async function* () {
    for (const item of items) yield item;
  })();

it("reconnects with a cursor, deduplicates blocks, reports gaps, and aborts", async () => {
  const controller = new AbortController();
  streamThreadBlocks
    .mockResolvedValueOnce({ stream: stream([block("one", 1), block("one", 1)]) })
    .mockResolvedValueOnce({
      stream: (async function* () {
        yield block("three", 3);
        yield block("late-two", 2);
        controller.abort();
      })(),
    });
  const received: string[] = [];
  const gaps: Array<[number, number]> = [];
  const statuses: string[] = [];
  await followThreadBlocks(
    "thread-1",
    0,
    controller.signal,
    (value) => {
      received.push(value.id);
    },
    (status) => statuses.push(status),
    (expected, actual) => gaps.push([expected, actual]),
  );
  expect(received).toEqual(["one", "three"]);
  expect(gaps).toEqual([[2, 3]]);
  expect(streamThreadBlocks).toHaveBeenNthCalledWith(
    2,
    expect.objectContaining({
      query: { after: 1 },
      headers: { "Last-Event-ID": "1" },
    }),
  );
  expect(statuses).toContain("reconnecting");
  // The caller aborted, so nothing is reported after it stopped listening.
  expect(statuses).not.toContain("closed");
});

it("stops after the server closes the stream of an ended session", async () => {
  streamThreadBlocks.mockReset();
  streamThreadBlocks.mockResolvedValue({ stream: stream([block("last", 4)]) });
  const statuses: string[] = [];
  const received: string[] = [];
  let ended = false;
  await followThreadBlocks(
    "thread-1",
    3,
    new AbortController().signal,
    (value) => {
      received.push(value.id);
      ended = true;
    },
    (status) => statuses.push(status),
    () => {},
    () => ended,
  );
  expect(received).toEqual(["last"]);
  expect(streamThreadBlocks).toHaveBeenCalledTimes(1);
  expect(statuses).toEqual(["connecting", "live", "closed"]);
});

it("streams through a fetch that takes a URL and the caller's signal", async () => {
  // WebKit can keep an aborted stream's connection open when fetch is given
  // a Request object, so the SSE client must not pass one through.
  streamThreadBlocks.mockReset();
  streamThreadBlocks.mockResolvedValue({ stream: stream([]) });
  const controller = new AbortController();
  await followThreadBlocks(
    "thread-1",
    0,
    controller.signal,
    () => {},
    () => {},
    () => {},
    () => true,
  );
  const options = streamThreadBlocks.mock.calls[0]?.[0] as { fetch?: typeof fetch };
  expect(options.fetch).toBeTypeOf("function");

  const native = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(""));
  const request = new Request("http://127.0.0.1:8080/threads/thread-1/blocks/stream?after=0", {
    headers: { "Last-Event-ID": "4" },
    signal: new AbortController().signal,
  });
  await options.fetch?.(request);
  const [input, init] = native.mock.calls[0] ?? [];
  expect(input).toBe("http://127.0.0.1:8080/threads/thread-1/blocks/stream?after=0");
  expect(init?.signal).toBe(controller.signal);
  expect(new Headers(init?.headers).get("Last-Event-ID")).toBe("4");
  native.mockRestore();
});
