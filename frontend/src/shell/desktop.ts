import { queryOptions } from "@tanstack/react-query";

// Present only inside the desktop shell. The browser has no bridge, and every
// caller treats that as a no-op rather than a second UI.

export type DesktopUpdateStatus = {
  available: boolean;
  version?: string;
  action?: "install" | "command";
  command?: string;
  detail?: string;
};

// The desktop app's own version and how this copy is updated. `install` is a
// fixed word; the page never learns the app's path. Development builds report
// version 0.0.0.
export type DesktopAppInfo = {
  version: string;
  install: "mac-app" | "homebrew" | "linux-package";
  selfUpdate: boolean;
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
  // Switches to saved Station 1–9 (1 is this computer). The desktop app
  // resolves the slot; the page never receives the saved list.
  switchStation?: (slot: number) => Promise<void>;
  openAddStation?: () => Promise<void>;
  setupCheck?: () => Promise<boolean>;
  setSetupCheck?: (enabled: boolean) => Promise<void>;
  checkForUpdates?: () => Promise<DesktopUpdateStatus>;
  // Downloads, installs, and restarts. While it runs, the shell may dispatch
  // meridian-update-progress with { phase: "download", percent } or
  // { phase: "install" }. Percent is absent when the download size is unknown.
  installUpdate?: () => Promise<void>;
  appInfo?: () => Promise<DesktopAppInfo>;
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

// The app's version and install kind do not change while the page is open.
// Shared by the Updates screen and the Station version notice.
export function desktopAppInfoQuery(bridge: DesktopBridge | undefined) {
  return queryOptions({
    queryKey: ["desktop-app-info"],
    queryFn: () => bridge!.appInfo!(),
    enabled: Boolean(bridge?.appInfo),
    retry: false,
    staleTime: Infinity,
  });
}
