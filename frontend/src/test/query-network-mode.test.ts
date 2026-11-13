import { MutationObserver, onlineManager } from "@tanstack/react-query";
import { afterEach, expect, it } from "vitest";

import { queryClient } from "../api/queries";

// The gateway is usually on loopback, so an `offline` event from the webview
// must not pause requests: a paused send left the composer disabled with
// nothing reaching the daemon, and new panels never loaded.
afterEach(() => {
  onlineManager.setOnline(true);
  queryClient.clear();
});

it("still runs queries while the browser reports offline", async () => {
  onlineManager.setOnline(false);
  const data = await queryClient.fetchQuery({
    queryKey: ["network-mode-probe"],
    queryFn: () => Promise.resolve("loaded"),
  });
  expect(data).toBe("loaded");
});

it("still sends mutations while the browser reports offline", async () => {
  onlineManager.setOnline(false);
  const observer = new MutationObserver(queryClient, {
    mutationFn: (text: string) => Promise.resolve(`sent ${text}`),
  });
  await expect(observer.mutate("hello")).resolves.toBe("sent hello");
  expect(observer.getCurrentResult().isPaused).toBe(false);
});
