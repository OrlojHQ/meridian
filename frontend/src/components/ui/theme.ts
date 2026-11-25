import { useEffect, useState } from "react";

import { applyColorTokens, readAppearance, type ThemeMode } from "./appearance";

export type ThemePreference = "system" | "light" | "dark";

const storageKey = "meridian.theme";

export function readTheme(): ThemePreference {
  try {
    const value = localStorage.getItem(storageKey);
    if (value === "light" || value === "dark" || value === "system") return value;
  } catch {
    // Theme is a preference. The interface still follows the system.
  }
  return "system";
}

export function systemIsDark() {
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ?? false;
}

export function resolveTheme(theme: ThemePreference): ThemeMode {
  if (theme === "system") return systemIsDark() ? "dark" : "light";
  return theme;
}

export function isDarkTheme() {
  const mode = document.documentElement.dataset.mode;
  if (mode === "dark") return true;
  if (mode === "light") return false;
  return systemIsDark();
}

let preference: ThemePreference = "system";
let followingSystem = false;

// data-theme records the preference (absent for System). data-mode is the
// resolved light or dark palette the stylesheet keys on.
export function applyTheme(theme: ThemePreference) {
  preference = theme;
  const root = document.documentElement;
  if (theme === "system") delete root.dataset.theme;
  else root.dataset.theme = theme;
  const mode = resolveTheme(theme);
  root.dataset.mode = mode;
  applyColorTokens(readAppearance(), mode);
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) {
    const appBg = getComputedStyle(root).getPropertyValue("--app-bg").trim();
    meta.setAttribute("content", appBg || (mode === "dark" ? "#101211" : "#e7eee9"));
  }
  followSystem();
  root.dispatchEvent(new Event("meridian-theme"));
}

function followSystem() {
  if (followingSystem) return;
  const media = window.matchMedia?.("(prefers-color-scheme: dark)");
  if (!media) return;
  followingSystem = true;
  media.addEventListener?.("change", () => {
    if (preference === "system") applyTheme("system");
  });
}

export function rememberTheme(theme: ThemePreference) {
  applyTheme(theme);
  try {
    if (theme === "system") localStorage.removeItem(storageKey);
    else localStorage.setItem(storageKey, theme);
  } catch {
    // Keep the choice for this view when storage is unavailable.
  }
}

export function nextTheme(theme: ThemePreference): ThemePreference {
  if (theme === "system") return "light";
  if (theme === "light") return "dark";
  return "system";
}

export function themeLabel(theme: ThemePreference) {
  if (theme === "light") return "Light";
  if (theme === "dark") return "Dark";
  return "System";
}

/** True while the dark palette applies. Updates on any appearance change. */
export function useDarkTheme() {
  const [dark, setDark] = useState(isDarkTheme);
  useEffect(() => {
    const update = () => setDark(isDarkTheme());
    document.documentElement.addEventListener("meridian-theme", update);
    return () => document.documentElement.removeEventListener("meridian-theme", update);
  }, []);
  return dark;
}
