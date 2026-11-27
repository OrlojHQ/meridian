import { useQuery } from "@tanstack/react-query";

import { queries } from "../api/queries";
import { desktopAppInfoQuery, desktopBridge } from "./desktop";
import { isReleaseVersion, laterRecord, updateIsSnoozed } from "./desktopUpdate";

// A team's Station is upgraded on the admin's schedule and each member's
// desktop app on their own, so an app can fall behind the Station it opens.
// Fixes that live in the desktop shell (link handling, the Station picker,
// notifications, updates) then stay missing until the member updates the app.

export type StationGap = {
  app: string;
  station: string;
};

type Parsed = {
  core: [number, number, number];
  pre: string[];
};

function parse(version: string): Parsed | null {
  const match = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$/.exec(version);
  if (!match) return null;
  return {
    core: [Number(match[1]), Number(match[2]), Number(match[3])],
    pre: match[4] ? match[4].split(".") : [],
  };
}

function compareIdentifier(a: string, b: string): number {
  const numericA = /^\d+$/.test(a);
  const numericB = /^\d+$/.test(b);
  if (numericA && numericB) return Math.sign(Number(a) - Number(b));
  if (numericA) return -1;
  if (numericB) return 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

// Semantic Versioning precedence: -1, 0, or 1, or null when either is not a
// version. Build metadata is ignored, and a prerelease sorts before its
// release.
export function compareVersions(a: string, b: string): number | null {
  const left = parse(a);
  const right = parse(b);
  if (!left || !right) return null;
  for (let index = 0; index < 3; index += 1) {
    const diff = Math.sign(left.core[index]! - right.core[index]!);
    if (diff !== 0) return diff;
  }
  if (left.pre.length === 0 || right.pre.length === 0) {
    return Math.sign(right.pre.length - left.pre.length);
  }
  const shared = Math.min(left.pre.length, right.pre.length);
  for (let index = 0; index < shared; index += 1) {
    const diff = compareIdentifier(left.pre[index]!, right.pre[index]!);
    if (diff !== 0) return diff;
  }
  return Math.sign(left.pre.length - right.pre.length);
}

// The gap only when both sides are releases and the app is older. A
// development build on either side has no release to compare.
export function stationGap(app: string | undefined, station: string | undefined): StationGap | null {
  if (!isReleaseVersion(app) || !isReleaseVersion(station)) return null;
  return compareVersions(app, station) === -1 ? { app, station } : null;
}

export function useStationGap(): StationGap | null {
  const bridge = desktopBridge();
  const inDesktop = Boolean(bridge?.appInfo);
  const appInfo = useQuery(desktopAppInfoQuery(bridge));
  const capabilities = useQuery({ ...queries.capabilities(), enabled: inDesktop });
  if (!inDesktop) return null;
  return stationGap(appInfo.data?.version, capabilities.data?.build?.version);
}

// Dismissing the notice hides it for a day for this pair of versions. A new
// Station or app version shows it again.
export const STATION_GAP_LATER_KEY = "meridian.desktop.station-gap.later";

function gapKey(gap: StationGap): string {
  return `${gap.app} < ${gap.station}`;
}

export function stationGapIsSnoozed(gap: StationGap, now: number): boolean {
  try {
    return updateIsSnoozed(window.localStorage.getItem(STATION_GAP_LATER_KEY), gapKey(gap), now);
  } catch {
    return false;
  }
}

export function snoozeStationGap(gap: StationGap, now: number) {
  try {
    window.localStorage.setItem(STATION_GAP_LATER_KEY, JSON.stringify(laterRecord(gapKey(gap), now)));
  } catch {
    // Storage refused: the notice comes back on the next load.
  }
}
