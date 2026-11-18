import { hasComposerDraft } from "./composerDraft";
import { desktopBridge, type DesktopStation } from "./desktop";

let handler: (id: string) => void = () => {};

export function requestStationSwitch(id: string) {
  handler(id);
}

export function bindStationSwitch(next: (id: string) => void) {
  handler = next;
  return () => {
    if (handler === next) handler = () => {};
  };
}

export function switchStationNow(id: string) {
  void desktopBridge()?.switchStation?.(id);
}

export function stationNeedsDraftWarning() {
  return hasComposerDraft();
}

// ⌘/Ctrl+1–9. Digit plus that modifier does not overlap ⌘K, ⌘N, or the
// unmodified single-key shortcuts. Ctrl+digit stays with a focused terminal.
export function isStationShortcut(
  event: { key: string; metaKey: boolean; ctrlKey: boolean; altKey: boolean; shiftKey: boolean },
  apple: boolean,
  inTerminal = false,
) {
  if (event.altKey || event.shiftKey || !/^[1-9]$/.test(event.key)) return false;
  if (apple) return event.metaKey && !event.ctrlKey;
  return event.ctrlKey && !event.metaKey && !inTerminal;
}

// The desktop shell's own limits (gateway.rs): a name is at most 60 Unicode
// scalar values, counted the same way here rather than in UTF-16 units, and
// a saved origin is at most 300 bytes.
const MAX_STATION_NAME_CHARS = 60;
const MAX_STATION_ID_LENGTH = 300;
const MAX_STATIONS = 513;

function parseStation(item: unknown): DesktopStation | undefined {
  if (!item || typeof item !== "object") return undefined;
  const { id, name, host, local, current } = item as Record<string, unknown>;
  if (typeof id !== "string" || id.length === 0 || id.length > MAX_STATION_ID_LENGTH) return undefined;
  if (typeof name !== "string" || name.trim() === "" || [...name].length > MAX_STATION_NAME_CHARS) return undefined;
  if (typeof host !== "string" || host.length > MAX_STATION_ID_LENGTH) return undefined;
  if (typeof local !== "boolean" || typeof current !== "boolean") return undefined;
  return { id, name, host, local, current };
}

// One entry the page cannot show is skipped. It does not hide the switcher
// or the other Stations.
export function parseStations(value: unknown): DesktopStation[] | undefined {
  if (!Array.isArray(value) || value.length > MAX_STATIONS) return undefined;
  return value.flatMap((item) => {
    const station = parseStation(item);
    return station ? [station] : [];
  });
}
