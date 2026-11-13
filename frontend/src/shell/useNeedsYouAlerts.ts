import { useEffect, useRef } from "react";

import { desktopBridge } from "./desktop";
import {
  applyNeedsYouFavicon,
  attentionItems,
  DOCUMENT_TITLE,
  incomingNeedsYou,
  needsYouDocumentTitle,
  needsYouNotification,
  postNeedsYouNotification,
  shouldPostNeedsYouNotification,
} from "./needsYou";
import type { WorkingSetItem } from "./useWorkingSet";

export function useNeedsYouAlerts({
  items,
  viewedCapsuleId,
  ready,
}: {
  items: WorkingSetItem[];
  viewedCapsuleId: string;
  ready: boolean;
}) {
  const count = attentionItems(items).length;

  useEffect(() => {
    document.title = needsYouDocumentTitle(count);
    applyNeedsYouFavicon(count);
    try {
      desktopBridge()?.setAttention(count);
    } catch {
      // The shell went away between detection and the call.
    }
  }, [count]);

  useEffect(
    () => () => {
      document.title = DOCUMENT_TITLE;
      applyNeedsYouFavicon(0);
      try {
        desktopBridge()?.setAttention(0);
      } catch {
        // The shell went away between detection and the call.
      }
    },
    [],
  );

  const previous = useRef<Set<string> | undefined>(undefined);
  useEffect(() => {
    if (!ready) return;
    const current = new Set(attentionItems(items).map((item) => item.capsule.id));
    const prior = previous.current;
    previous.current = current;
    if (!prior) return;
    const incoming = incomingNeedsYou(prior, current, viewedCapsuleId);
    if (incoming.length === 0) return;
    const bridge = desktopBridge();
    if (bridge) {
      const unfocused =
        document.hidden ||
        (typeof document.hasFocus === "function" && !document.hasFocus());
      if (!unfocused) return;
      const payload = needsYouNotification(current.size);
      try {
        bridge.notify(payload.title, payload.body);
      } catch {
        // A rejected payload stays off the lock screen.
      }
      return;
    }
    const permission =
      typeof Notification === "undefined" ? "denied" : Notification.permission;
    if (
      !shouldPostNeedsYouNotification({
        hidden: document.hidden,
        permission,
        incoming,
      })
    ) {
      return;
    }
    postNeedsYouNotification(current.size);
  }, [items, viewedCapsuleId, ready]);
}
