import { useEffect, useState } from "react";

// Appearance preferences beyond light/dark: contrast, fonts, text size, and
// per-mode colors. Like the theme, they are remembered locally on this device.
// The built-in palettes live in tokens.css. A custom palette is derived from
// three colors and set as inline custom properties, which win over the sheet.

export type ThemeMode = "light" | "dark";
export type Contrast = "default" | "strong";
/** Flat is the original look. The others add layered light and shadow. */
export type Depth = "flat" | "soft" | "glow" | "glass";

export type ThemeColors = {
  background: string;
  foreground: string;
  accent: string;
};

/** A preset id, or "custom" with the operator's own colors. */
export type ThemeChoice = { preset: string; colors?: ThemeColors };

export type Appearance = {
  contrast: Contrast;
  depth: Depth;
  font: string;
  codeFont: string;
  fontSize: number;
  light: ThemeChoice;
  dark: ThemeChoice;
};

export type ThemePreset = { id: string; label: string; colors: ThemeColors };

export type FontChoice = { id: string; label: string; stack?: string };

export const defaultPresetId = "meridian";
export const customPresetId = "custom";
export const defaultFontSize = 14;
export const depths: { value: Depth; label: string }[] = [
  { value: "flat", label: "Flat" },
  { value: "soft", label: "Soft" },
  { value: "glow", label: "Glow" },
  { value: "glass", label: "Glass" },
];
export const fontSizes = [12, 13, 14, 15, 16, 17, 18];

const storageKey = "meridian.appearance";
const sansFallback = 'ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif';
const monoFallback = "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace";

// Bundled faces (see main.tsx) load only when a stack that names them is used.
export const uiFonts: FontChoice[] = [
  { id: "default", label: "Default" },
  { id: "inter", label: "Inter", stack: `"Inter Variable", ${sansFallback}` },
  { id: "geist", label: "Geist", stack: `"Geist Variable", ${sansFallback}` },
  { id: "plex", label: "IBM Plex Sans", stack: `"IBM Plex Sans Variable", ${sansFallback}` },
  { id: "system", label: "System", stack: sansFallback },
  { id: "jetbrains", label: "JetBrains Mono", stack: `"JetBrains Mono Variable", ${monoFallback}` },
];

export const codeFonts: FontChoice[] = [
  { id: "default", label: "Default" },
  { id: "jetbrains", label: "JetBrains Mono", stack: `"JetBrains Mono Variable", ${monoFallback}` },
  { id: "geist-mono", label: "Geist Mono", stack: `"Geist Mono Variable", ${monoFallback}` },
  { id: "plex-mono", label: "IBM Plex Mono", stack: `"IBM Plex Mono", ${monoFallback}` },
  { id: "system", label: "System", stack: monoFallback },
];

// The "meridian" entries match the hand-tuned palettes in tokens.css. While
// one is selected without edits, the sheet is used as is.
export const themePresets: Record<ThemeMode, ThemePreset[]> = {
  light: [
    { id: "meridian", label: "Meridian Light", colors: { background: "#f4f7f5", foreground: "#17211c", accent: "#315c4e" } },
    { id: "paper", label: "Paper", colors: { background: "#f7f3ea", foreground: "#2a251f", accent: "#a4532a" } },
    { id: "slate", label: "Slate", colors: { background: "#f5f7fa", foreground: "#18202c", accent: "#3a5bd9" } },
    { id: "solarized", label: "Solarized Light", colors: { background: "#fdf6e3", foreground: "#073642", accent: "#268bd2" } },
  ],
  dark: [
    { id: "meridian", label: "Meridian Dark", colors: { background: "#101211", foreground: "#f3f5f3", accent: "#315c4e" } },
    { id: "midnight", label: "Midnight", colors: { background: "#070708", foreground: "#ffffff", accent: "#2f71e3" } },
    { id: "nord", label: "Nord", colors: { background: "#2e3440", foreground: "#eceff4", accent: "#88c0d0" } },
    { id: "solarized", label: "Solarized Dark", colors: { background: "#002b36", foreground: "#eee8d5", accent: "#268bd2" } },
    { id: "ember", label: "Ember", colors: { background: "#15110e", foreground: "#f2e9e0", accent: "#d2713a" } },
  ],
};

// Status hues stay recognizable across palettes. Only their tinted
// backgrounds follow the chosen background.
const statusHues: Record<ThemeMode, { success: string; danger: string; warning: string; info: string }> = {
  light: { success: "#2d8a50", danger: "#9b332d", warning: "#9a6b12", info: "#0b5cab" },
  dark: { success: "#5fb887", danger: "#e1857d", warning: "#dfbb72", info: "#9ecbff" },
};

export const defaultAppearance: Appearance = {
  contrast: "default",
  depth: "flat",
  font: "default",
  codeFont: "default",
  fontSize: defaultFontSize,
  light: { preset: defaultPresetId },
  dark: { preset: defaultPresetId },
};

// ---- Colors -------------------------------------------------------------

type Rgb = [number, number, number];

export function normalizeHex(value: string): string | undefined {
  const raw = value.trim().replace(/^#/, "").toLowerCase();
  if (/^[0-9a-f]{3}$/.test(raw)) return `#${[...raw].map((c) => c + c).join("")}`;
  if (/^[0-9a-f]{6}$/.test(raw)) return `#${raw}`;
  return undefined;
}

function rgb(hex: string): Rgb {
  const value = normalizeHex(hex) ?? "#000000";
  return [1, 3, 5].map((i) => Number.parseInt(value.slice(i, i + 2), 16)) as Rgb;
}

function hex([r, g, b]: Rgb): string {
  return `#${[r, g, b]
    .map((c) => Math.round(Math.min(255, Math.max(0, c))).toString(16).padStart(2, "0"))
    .join("")}`;
}

/** Mix a toward b by t (0..1) in sRGB, like CSS color-mix(in srgb). */
export function mix(a: string, b: string, t: number): string {
  const x = rgb(a);
  const y = rgb(b);
  return hex([0, 1, 2].map((i) => x[i] + (y[i] - x[i]) * t) as Rgb);
}

function alpha(color: string, amount: number) {
  const [r, g, b] = rgb(color);
  return `rgb(${r} ${g} ${b} / ${Math.round(amount * 100)}%)`;
}

function luminance(color: string) {
  const [r, g, b] = rgb(color).map((c) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

/** WCAG contrast ratio between two colors, 1..21. */
export function contrastRatio(a: string, b: string) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

/** Move color toward `toward` until it reaches ratio against `against`. */
export function ensureContrast(color: string, against: string, ratio: number, toward: string) {
  for (let t = 0; t <= 1.0001; t += 0.05) {
    const candidate = mix(color, toward, t);
    if (contrastRatio(candidate, against) >= ratio) return candidate;
  }
  return toward;
}

function readableOn(fill: string) {
  return contrastRatio("#ffffff", fill) >= contrastRatio("#0b0d0c", fill) ? "#ffffff" : "#0b0d0c";
}

/** The custom properties a derived palette sets. */
export const derivedTokenNames = Object.keys(
  deriveTokens("dark", themePresets.dark[0].colors, "default"),
);

export function deriveTokens(mode: ThemeMode, colors: ThemeColors, contrast: Contrast): Record<string, string> {
  const bg = normalizeHex(colors.background) ?? "#ffffff";
  const fg = normalizeHex(colors.foreground) ?? "#000000";
  const accent = normalizeHex(colors.accent) ?? fg;
  const strong = contrast === "strong";
  const dark = mode === "dark";
  const hue = statusHues[mode];
  const ink = (t: number) => mix(bg, fg, t);
  const tint = (color: string, t: number) => mix(bg, color, t);
  const accentText = ensureContrast(accent, bg, 4.5, fg);
  const muted = ensureContrast(ink(dark ? (strong ? 0.8 : 0.65) : strong ? 0.88 : 0.75), bg, strong ? 7 : 4.5, fg);
  const faint = ensureContrast(ink(dark ? (strong ? 0.65 : 0.45) : strong ? 0.82 : 0.7), bg, strong ? 4.5 : 3, fg);
  const soft = dark ? 0.22 : 0.12;

  const tokens: Record<string, string> = {
    "--app-bg": dark ? bg : ink(0.05),
    "--side": dark ? mix(bg, "#000000", 0.25) : ink(0.02),
    "--canvas": bg,
    "--surface": dark ? ink(0.03) : mix(bg, "#ffffff", 0.35),
    "--surface-raised": dark ? ink(0.055) : mix(bg, "#ffffff", 0.85),
    "--surface-subtle": dark ? ink(0.075) : ink(0.03),
    "--surface-hover": dark ? ink(0.095) : ink(0.055),
    "--surface-selected": tint(accent, dark ? 0.32 : 0.12),
    "--border": ink(dark ? (strong ? 0.17 : 0.11) : strong ? 0.17 : 0.1),
    "--border-strong": ink(dark ? (strong ? 0.27 : 0.18) : strong ? 0.3 : 0.19),
    "--text": strong ? mix(fg, dark ? "#ffffff" : "#000000", 0.5) : fg,
    "--muted": muted,
    "--faint": faint,
    "--accent": accent,
    "--accent-text": accentText,
    "--accent-hover": dark ? mix(accent, fg, 0.12) : mix(accent, "#000000", 0.18),
    "--accent-soft": tint(accent, dark ? 0.25 : 0.1),
    "--accent-contrast": readableOn(accent),
    "--accent-ring": alpha(accentText, 0.36),
    "--focus": accentText,
    "--danger-soft": tint(hue.danger, soft),
    "--warning-soft": tint(hue.warning, soft),
    "--diff-add-bg": tint(hue.success, dark ? 0.14 : 0.1),
    "--diff-add-gutter": tint(hue.success, dark ? 0.26 : 0.2),
    "--diff-delete-bg": tint(hue.danger, dark ? 0.14 : 0.08),
    "--diff-delete-gutter": tint(hue.danger, dark ? 0.26 : 0.16),
    "--diff-hunk-bg": tint(hue.info, dark ? 0.14 : 0.08),
    "--badge-fg": ink(dark ? 0.82 : 0.78),
    "--badge-bg": ink(dark ? 0.13 : 0.07),
    "--badge-ended-fg": ink(dark ? 0.82 : 0.78),
    "--badge-ended-bg": ink(dark ? 0.13 : 0.07),
    "--badge-ready-bg": tint(hue.success, soft),
    "--badge-failed-bg": tint(hue.danger, soft),
    "--badge-paused-bg": tint(hue.warning, soft),
    "--overlay": dark ? alpha(mix(bg, "#000000", 0.5), 0.62) : alpha(mix(fg, "#000000", 0.3), 0.46),
  };
  if (dark) {
    // In light mode the terminal keeps its dark built-in surface.
    tokens["--terminal"] = mix(bg, "#000000", 0.25);
    tokens["--terminal-text"] = mix(fg, bg, 0.08);
    tokens["--terminal-bar"] = ink(0.04);
  }
  return tokens;
}

// ---- Preferences --------------------------------------------------------

export function presetColors(mode: ThemeMode, preset: string): ThemeColors {
  return (themePresets[mode].find((item) => item.id === preset) ?? themePresets[mode][0]).colors;
}

export function themeColors(appearance: Appearance, mode: ThemeMode): ThemeColors {
  const choice = appearance[mode];
  return choice.colors ?? presetColors(mode, choice.preset);
}

export function isBuiltInPalette(appearance: Appearance, mode: ThemeMode) {
  const choice = appearance[mode];
  return choice.preset === defaultPresetId && !choice.colors;
}

/** Inline tokens for a mode, or null when the built-in palette applies. */
export function customTokens(appearance: Appearance, mode: ThemeMode) {
  if (isBuiltInPalette(appearance, mode)) return null;
  return deriveTokens(mode, themeColors(appearance, mode), appearance.contrast);
}

function readChoice(value: unknown, mode: ThemeMode): ThemeChoice {
  if (!value || typeof value !== "object") return { preset: defaultPresetId };
  const { preset, colors } = value as { preset?: unknown; colors?: Partial<Record<keyof ThemeColors, unknown>> };
  if (preset === customPresetId && colors && typeof colors === "object") {
    const background = typeof colors.background === "string" ? normalizeHex(colors.background) : undefined;
    const foreground = typeof colors.foreground === "string" ? normalizeHex(colors.foreground) : undefined;
    const accent = typeof colors.accent === "string" ? normalizeHex(colors.accent) : undefined;
    if (background && foreground && accent) {
      return { preset: customPresetId, colors: { background, foreground, accent } };
    }
  }
  if (typeof preset === "string" && themePresets[mode].some((item) => item.id === preset)) return { preset };
  return { preset: defaultPresetId };
}

export function parseAppearance(raw: string | null): Appearance {
  if (!raw) return defaultAppearance;
  let value: Record<string, unknown>;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") return defaultAppearance;
    value = parsed as Record<string, unknown>;
  } catch {
    return defaultAppearance;
  }
  const pick = (choices: FontChoice[], id: unknown) =>
    typeof id === "string" && choices.some((choice) => choice.id === id) ? id : "default";
  return {
    contrast: value.contrast === "strong" ? "strong" : "default",
    depth: depths.find((depth) => depth.value === value.depth)?.value ?? "flat",
    font: pick(uiFonts, value.font),
    codeFont: pick(codeFonts, value.codeFont),
    fontSize:
      typeof value.fontSize === "number" && fontSizes.includes(value.fontSize) ? value.fontSize : defaultFontSize,
    light: readChoice(value.light, "light"),
    dark: readChoice(value.dark, "dark"),
  };
}

// Kept for this view when storage is unavailable.
let unsaved: Appearance | undefined;

export function readAppearance(): Appearance {
  try {
    return parseAppearance(localStorage.getItem(storageKey));
  } catch {
    return unsaved ?? defaultAppearance;
  }
}

function saveAppearance(appearance: Appearance) {
  unsaved = appearance;
  try {
    if (JSON.stringify(appearance) === JSON.stringify(defaultAppearance)) localStorage.removeItem(storageKey);
    else localStorage.setItem(storageKey, JSON.stringify(appearance));
  } catch {
    // Keep the choice for this view when storage is unavailable.
  }
}

// ---- Applying -----------------------------------------------------------

export function currentMode(): ThemeMode {
  return document.documentElement.dataset.mode === "dark" ? "dark" : "light";
}

function setOrClear(element: HTMLElement, name: string, value: string | undefined) {
  if (value) element.style.setProperty(name, value);
  else element.style.removeProperty(name);
}

/** Set (or clear) one mode's custom palette on an element. */
export function applyColorTokens(appearance: Appearance, mode: ThemeMode, element: HTMLElement = document.documentElement) {
  for (const name of derivedTokenNames) element.style.removeProperty(name);
  const tokens = customTokens(appearance, mode);
  if (!tokens) return;
  for (const [name, value] of Object.entries(tokens)) element.style.setProperty(name, value);
}

export function applyAppearance(appearance: Appearance) {
  const root = document.documentElement;
  if (appearance.contrast === "strong") root.dataset.contrast = "strong";
  else delete root.dataset.contrast;
  if (appearance.depth === "flat") delete root.dataset.depth;
  else root.dataset.depth = appearance.depth;
  setOrClear(root, "--font-sans", uiFonts.find((font) => font.id === appearance.font)?.stack);
  setOrClear(root, "--font-mono", codeFonts.find((font) => font.id === appearance.codeFont)?.stack);
  // Type and spacing are rem based, so the root size scales the interface.
  root.style.fontSize =
    appearance.fontSize === defaultFontSize ? "" : `${(appearance.fontSize / defaultFontSize) * 100}%`;
  applyColorTokens(appearance, currentMode());
  root.dispatchEvent(new Event("meridian-theme"));
}

export function updateAppearance(change: (current: Appearance) => Appearance) {
  const next = change(readAppearance());
  saveAppearance(next);
  applyAppearance(next);
  return next;
}

export function useAppearance() {
  const [appearance, setAppearance] = useState(readAppearance);
  useEffect(() => {
    const update = () => setAppearance(readAppearance());
    document.documentElement.addEventListener("meridian-theme", update);
    return () => document.documentElement.removeEventListener("meridian-theme", update);
  }, []);
  return appearance;
}
