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

export function parseStations(value: unknown): DesktopStation[] | undefined {
  if (!Array.isArray(value) || value.length > 512) return undefined;
  const stations: DesktopStation[] = [];
  for (const item of value) {
    if (!item || typeof item !== "object") return undefined;
    const record = item as Record<string, unknown>;
    const { id, name, host, local, current } = record;
    if (typeof id !== "string" || id.length === 0 || id.length > 300) return undefined;
    if (typeof name !== "string" || name.trim() === "" || name.length > 80) return undefined;
    if (typeof host !== "string" || host.length > 300) return undefined;
    if (typeof local !== "boolean" || typeof current !== "boolean") return undefined;
    stations.push({ id, name, host, local, current });
  }
  return stations;
}
