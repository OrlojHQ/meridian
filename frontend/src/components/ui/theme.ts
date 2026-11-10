import { useEffect, useState } from "react";

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

export function isDarkTheme() {
  const explicit = document.documentElement.dataset.theme;
  if (explicit === "dark") return true;
  if (explicit === "light") return false;
  return systemIsDark();
}

export function applyTheme(theme: ThemePreference) {
  const root = document.documentElement;
  if (theme === "system") delete root.dataset.theme;
  else root.dataset.theme = theme;
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute("content", isDarkTheme() ? "#101512" : "#f4f7f5");
  root.dispatchEvent(new Event("meridian-theme"));
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

export function useDarkTheme() {
  const [dark, setDark] = useState(isDarkTheme);
  useEffect(() => {
    const update = () => setDark(isDarkTheme());
    const media = window.matchMedia?.("(prefers-color-scheme: dark)");
    media?.addEventListener("change", update);
    document.documentElement.addEventListener("meridian-theme", update);
    return () => {
      media?.removeEventListener("change", update);
      document.documentElement.removeEventListener("meridian-theme", update);
    };
  }, []);
  return dark;
}
