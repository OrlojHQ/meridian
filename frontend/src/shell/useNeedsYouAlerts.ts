import { useEffect, useRef } from "react";

import {
  applyNeedsYouFavicon,
  attentionItems,
  DOCUMENT_TITLE,
  incomingNeedsYou,
  needsYouDocumentTitle,
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
  }, [count]);

  useEffect(
    () => () => {
      document.title = DOCUMENT_TITLE;
      applyNeedsYouFavicon(0);
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
