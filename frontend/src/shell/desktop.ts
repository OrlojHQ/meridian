// Present only inside the desktop shell. The browser has no bridge, and every
// caller treats that as a no-op rather than a second UI.

export type DesktopUpdateStatus = {
  available: boolean;
  version?: string;
  action?: "install" | "command";
  command?: string;
  detail?: string;
};

// The gateway this window is on, as the shell names it. Nothing about any
// other gateway the operator uses is exposed to the page.
export type DesktopGateway = {
  label: string;
  local: boolean;
};

export type DesktopBridge = {
  notify: (title: string, body: string) => void;
  setAttention: (count: number) => void;
  openGatewaySettings?: () => void;
  gateway?: () => Promise<DesktopGateway>;
  checkForUpdates?: () => Promise<DesktopUpdateStatus>;
  installUpdate?: () => Promise<void>;
  // Opens the desktop app's own import window (ADR 0049). It takes nothing
  // and returns only whether the window opened: the page is never told what
  // the app found on this computer.
  openImport?: () => Promise<boolean>;
};

declare global {
  interface Window {
    __MERIDIAN_DESKTOP__?: DesktopBridge;
  }
}

export function desktopBridge(): DesktopBridge | undefined {
  const bridge = window.__MERIDIAN_DESKTOP__;
  if (
    !bridge ||
    typeof bridge.notify !== "function" ||
    typeof bridge.setAttention !== "function"
  ) {
    return undefined;
  }
  return bridge;
}
