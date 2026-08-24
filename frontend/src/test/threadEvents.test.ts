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
  expect(statuses.at(-1)).toBe("closed");
});
