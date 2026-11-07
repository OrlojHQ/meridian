import { sessionPath } from "./capsuleActivity";

export const DOCUMENT_TITLE = "Meridian";
export const NEEDS_YOU_PHRASE = "Meridian needs you";
export const NEEDS_YOU_NOTIFICATION_TAG = "meridian-needs-you";
export const GO_PREFIX_MS = 1_000;

export type NeedsYouItem = {
  capsule: { id: string };
  activity: { group: string; since?: string };
};

// Attention rows are derived only from the content-free working set. Capsule
// names, prompts, permission details, and transcript text never enter a
// notification, document title, or favicon badge.
export function attentionItems<T extends NeedsYouItem>(items: readonly T[]): T[] {
  return items
    .filter((item) => item.activity.group === "attention")
    .sort(
      (left, right) =>
        Date.parse(left.activity.since ?? "") - Date.parse(right.activity.since ?? ""),
    );
}

export function nextNeedingMe<T extends NeedsYouItem>(
  items: readonly T[],
  currentCapsuleId: string,
): T | undefined {
  const waiting = attentionItems(items);
  if (waiting.length === 0) return undefined;
  const index = waiting.findIndex((item) => item.capsule.id === currentCapsuleId);
  if (index < 0) return waiting[0];
  return waiting[(index + 1) % waiting.length];
}

export function nextNeedingMePath(
  items: readonly NeedsYouItem[],
  currentCapsuleId: string,
): string | undefined {
  const next = nextNeedingMe(items, currentCapsuleId);
  return next ? sessionPath(next.capsule.id) : undefined;
}

export function needsYouDocumentTitle(count: number): string {
  return count > 0 ? `(${count}) ${DOCUMENT_TITLE}` : DOCUMENT_TITLE;
}

export type NeedsYouNotification = {
  title: string;
  body: string;
};

export function needsYouNotification(count: number): NeedsYouNotification {
  return {
    title: NEEDS_YOU_PHRASE,
    body: count === 1 ? "1 needs you" : `${count} need you`,
  };
}

export function incomingNeedsYou(
  previous: ReadonlySet<string>,
  current: ReadonlySet<string>,
  viewedCapsuleId: string,
): string[] {
  const incoming: string[] = [];
  for (const id of current) {
    if (!previous.has(id) && id !== viewedCapsuleId) incoming.push(id);
  }
  return incoming;
}

export function shouldPostNeedsYouNotification({
  hidden,
  permission,
  incoming,
}: {
  hidden: boolean;
  permission: string;
  incoming: readonly string[];
}): boolean {
  return hidden && permission === "granted" && incoming.length > 0;
}

type NotificationCtor = {
  new (title: string, options?: NotificationOptions): {
    onclick: ((this: Notification, ev: Event) => void) | null;
    close: () => void;
  };
};

export function postNeedsYouNotification(
  count: number,
  ctor: NotificationCtor | undefined = typeof Notification === "undefined" ? undefined : Notification,
): NeedsYouNotification | undefined {
  if (!ctor) return undefined;
  const payload = needsYouNotification(count);
  const notification = new ctor(payload.title, {
    body: payload.body,
    tag: NEEDS_YOU_NOTIFICATION_TAG,
  });
  notification.onclick = () => {
    window.focus();
    notification.close();
  };
  return payload;
}

const FAVICON_ID = "meridian-needs-you-favicon";

export function applyNeedsYouFavicon(count: number) {
  const href = needsYouFaviconDataUrl(count);
  if (!href) return;
  let link = document.getElementById(FAVICON_ID) as HTMLLinkElement | null;
  if (!link) {
    link = document.createElement("link");
    link.id = FAVICON_ID;
    link.rel = "icon";
    document.head.appendChild(link);
  }
  link.href = href;
}

export function needsYouFaviconDataUrl(count: number): string | undefined {
  const canvas = document.createElement("canvas");
  canvas.width = 32;
  canvas.height = 32;
  const context = canvas.getContext("2d");
  if (!context) return undefined;
  context.fillStyle = "#151816";
  context.fillRect(2, 2, 28, 28);
  context.fillStyle = "#e8ece6";
  context.font = "700 16px ui-sans-serif, system-ui, sans-serif";
  context.textAlign = "center";
  context.textBaseline = "middle";
  context.fillText("M", 16, 18);
  if (count > 0) {
    context.fillStyle = "#c45c4a";
    context.beginPath();
    context.arc(24, 8, 8, 0, Math.PI * 2);
    context.fill();
    context.fillStyle = "#fffdf8";
    context.font = "700 10px ui-sans-serif, system-ui, sans-serif";
    context.fillText(count > 9 ? "9+" : String(count), 24, 9);
  }
  return canvas.toDataURL("image/png");
}
