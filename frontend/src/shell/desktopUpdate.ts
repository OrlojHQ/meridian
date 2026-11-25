import { useSyncExternalStore } from "react";

import type { DesktopUpdateStatus } from "./desktop";

// Snooze for the automatic update check. A manual check still shows the offer.

export const UPDATE_LATER_KEY = "meridian.desktop.update.later";
export const UPDATE_LATER_MS = 24 * 60 * 60 * 1000;

export type StoredLater = {
  version: string;
  until: number;
};

export function laterRecord(version: string, now: number): StoredLater {
  return { version, until: now + UPDATE_LATER_MS };
}

export function readLater(raw: string | null, now: number): StoredLater | null {
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as Partial<StoredLater>;
    if (typeof parsed.version === "string" && typeof parsed.until === "number" && parsed.until > now) {
      return { version: parsed.version, until: parsed.until };
    }
  } catch {
    // An unreadable record is no snooze.
  }
  return null;
}

export function updateIsSnoozed(raw: string | null, version: string, now: number): boolean {
  if (!raw) return false;
  try {
    const parsed = JSON.parse(raw) as Partial<StoredLater>;
    return parsed.version === version && typeof parsed.until === "number" && parsed.until > now;
  } catch {
    return false;
  }
}

// Automatic checks. The page checks when it loads, then on the shell's
// meridian-update-due request (every four hours and after a wake), when the
// window is shown again, and on its own slow tick. Each is measured on the
// wall clock, because a hidden window's timers are throttled and stop while
// the computer sleeps.
export const UPDATE_DUE_EVENT = "meridian-update-due";
export const UPDATE_CHECK_EVERY_MS = 4 * 60 * 60 * 1000;
export const UPDATE_DUE_GAP_MS = 10 * 60 * 1000;
export const UPDATE_TICK_MS = 5 * 60 * 1000;

export function automaticCheckDue(last: number | undefined, now: number, gap: number): boolean {
  if (last === undefined) return true;
  // A clock set backwards starts over.
  return now < last || now - last >= gap;
}

export const UPDATE_PROGRESS_EVENT = "meridian-update-progress";

export type UpdateProgress = {
  phase: "download" | "install";
  percent?: number;
};

export function readUpdateProgress(detail: unknown): UpdateProgress | null {
  if (!detail || typeof detail !== "object") return null;
  const value = detail as { phase?: unknown; percent?: unknown };
  if (value.phase !== "download" && value.phase !== "install") return null;
  if (value.phase === "install") return { phase: "install" };
  if (typeof value.percent !== "number" || !Number.isFinite(value.percent)) {
    return { phase: "download" };
  }
  return {
    phase: "download",
    percent: Math.max(0, Math.min(100, Math.floor(value.percent))),
  };
}

export function applyUpdateProgress(
  current: UpdateProgress | undefined,
  detail: unknown,
): UpdateProgress | undefined {
  const next = readUpdateProgress(detail);
  if (!next) return current;
  if (next.phase === "install") return { phase: "install" };
  if (typeof next.percent !== "number") {
    if (current?.phase === "download" && typeof current.percent === "number") return current;
    return { phase: "download" };
  }
  return next;
}

export function updateBusyLabel(progress?: UpdateProgress): string {
  if (progress?.phase === "install") return "Restarting…";
  if (typeof progress?.percent === "number") return `Downloading… ${progress.percent}%`;
  return "Downloading…";
}

// The latest check in this page, automatic or manual, so the Updates screen
// shows what is already known instead of waiting for another check. `status`
// is null when the check failed.
export type UpdateCheckResult = {
  at: number;
  status: DesktopUpdateStatus | null;
};

let lastCheck: UpdateCheckResult | null = null;
const checkListeners = new Set<() => void>();

export function recordUpdateCheck(result: UpdateCheckResult | null) {
  lastCheck = result;
  for (const listener of checkListeners) listener();
}

function subscribeUpdateCheck(listener: () => void) {
  checkListeners.add(listener);
  return () => {
    checkListeners.delete(listener);
  };
}

export function useLastUpdateCheck(): UpdateCheckResult | null {
  return useSyncExternalStore(subscribeUpdateCheck, () => lastCheck);
}

// "Checked 5 minutes ago". Coarse on purpose: checks are hours apart.
export function checkedAgo(at: number, now: number): string {
  const minutes = Math.max(0, Math.floor((now - at) / 60_000));
  if (minutes < 1) return "Checked just now";
  if (minutes < 60) return `Checked ${minutes} ${minutes === 1 ? "minute" : "minutes"} ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `Checked ${hours} ${hours === 1 ? "hour" : "hours"} ago`;
  const days = Math.floor(hours / 24);
  return `Checked ${days} ${days === 1 ? "day" : "days"} ago`;
}

// Release notes live on the GitHub release for each tag. A development build
// has no release.
export const RELEASES_URL = "https://github.com/OrlojHQ/meridian/releases";

export function isReleaseVersion(version: string | undefined): version is string {
  return Boolean(version && /^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(version) && version !== "0.0.0");
}

export function releaseNotesURL(version: string): string {
  return `${RELEASES_URL}/tag/v${encodeURIComponent(version)}`;
}
