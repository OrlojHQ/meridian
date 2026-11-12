// Present only inside the desktop shell. The browser has no bridge, and every
// caller treats that as a no-op rather than a second UI.

export type DesktopBridge = {
  notify: (title: string, body: string) => void;
  setAttention: (count: number) => void;
  openGatewaySettings?: () => void;
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
