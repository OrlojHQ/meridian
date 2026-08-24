import { beforeEach, expect, it, vi } from "vitest";

import type { ThreadBlock, ThreadBlockPage } from "../api/generated/types.gen";

const { listThreadBlocks } = vi.hoisted(() => ({
  listThreadBlocks: vi.fn(),
}));

vi.mock("../api/generated/client.gen", () => ({
  client: { setConfig: vi.fn() },
}));

vi.mock("../api/generated/sdk.gen", () => ({
  listThreadBlocks,
}));

import { api, MeridianAPIError } from "../api/client";

const block = (messageSequence: number): ThreadBlock => ({
  id: `block-${messageSequence}`,
  messageId: `message-${messageSequence}`,
  messageSequence,
  sequence: 1,
  role: "assistant",
  messageKind: "response",
  kind: "text",
  content: `content-${messageSequence}`,
  createdAt: "2026-08-23T00:00:00Z",
});

const result = (page: ThreadBlockPage) =>
  Promise.resolve({ data: page, response: new Response() });

beforeEach(() => {
  listThreadBlocks.mockReset();
});

it("paginates with generated calls and retains the newest bounded transcript", async () => {
  for (let page = 0; page < 5; page += 1) {
    const start = page * 100 + 1;
    listThreadBlocks.mockReturnValueOnce(
      result({
        items: Array.from({ length: 100 }, (_, index) => block(start + index)),
        nextCursor: start + 99,
        more: page < 4,
      }),
    );
  }

  const replay = await api.threadBlocks("thread-1");

  expect(listThreadBlocks).toHaveBeenCalledTimes(5);
  expect(listThreadBlocks).toHaveBeenNthCalledWith(
    2,
    expect.objectContaining({ query: { after: 100, limit: 100 } }),
  );
  expect(replay.items).toHaveLength(400);
  expect(replay.items[0]?.messageSequence).toBe(101);
  expect(replay.items.at(-1)?.messageSequence).toBe(500);
  expect(replay.nextCursor).toBe(500);
  expect(replay.more).toBe(false);
});

it("fails closed when a replay cursor does not advance", async () => {
  listThreadBlocks.mockReturnValue(
    result({ items: [block(1)], nextCursor: 0, more: true }),
  );

  await expect(api.threadBlocks("thread-1")).rejects.toEqual(
    expect.objectContaining<Partial<MeridianAPIError>>({
      code: "invalid_cursor",
    }),
  );
  expect(listThreadBlocks).toHaveBeenCalledTimes(1);
});
