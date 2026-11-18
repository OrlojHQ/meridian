import { hasComposerDraft } from "./composerDraft";
import { desktopBridge, type DesktopBridge } from "./desktop";

// Leaving this page for another Station. The page never sees the saved
// list (ADR 0055): a ⌘1–9 slot is resolved by the desktop app, and the
// picker where Stations are listed, added, and renamed is the app's own
// window, which a Station page cannot read.
export type StationAction = { kind: "slot"; slot: number } | { kind: "picker" } | { kind: "add" };

export function runStationAction(action: StationAction) {
  const bridge = desktopBridge();
  if (action.kind === "slot") void bridge?.switchStation?.(action.slot);
  else if (action.kind === "add") void bridge?.openAddStation?.();
  else void bridge?.openGatewaySettings?.();
}

// The shell binds a handler that asks before an unsent draft is lost.
// Until then an action runs directly.
let handler: (action: StationAction) => void = runStationAction;

export function requestStationAction(action: StationAction) {
  handler(action);
}

export function bindStationActions(next: (action: StationAction) => void) {
  handler = next;
  return () => {
    if (handler === next) handler = runStationAction;
  };
}

export function stationNeedsDraftWarning() {
  return hasComposerDraft();
}

// The desktop app offers the switcher. A browser has no saved Stations.
export function hasStationSwitcher(bridge: DesktopBridge | undefined) {
  return Boolean(bridge?.switchStation && bridge.openGatewaySettings);
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
