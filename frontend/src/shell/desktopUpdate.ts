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

export function updateIsSnoozed(raw: string | null, version: string, now: number): boolean {
  if (!raw) return false;
  try {
    const parsed = JSON.parse(raw) as Partial<StoredLater>;
    return parsed.version === version && typeof parsed.until === "number" && parsed.until > now;
  } catch {
    return false;
  }
}
