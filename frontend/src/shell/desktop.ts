// Present only inside the desktop shell. The browser has no bridge, and every
// caller treats that as a no-op rather than a second UI.

export type DesktopUpdateStatus = {
  available: boolean;
  version?: string;
  action?: "install" | "command";
  command?: string;
  detail?: string;
};

export type DesktopBridge = {
  notify: (title: string, body: string) => void;
  setAttention: (count: number) => void;
  openGatewaySettings?: () => void;
  checkForUpdates?: () => Promise<DesktopUpdateStatus>;
  installUpdate?: () => Promise<void>;
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
