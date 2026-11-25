import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import {
  applyAppearance,
  contrastRatio,
  customTokens,
  defaultAppearance,
  deriveTokens,
  derivedTokenNames,
  ensureContrast,
  normalizeHex,
  parseAppearance,
  readAppearance,
  themePresets,
  updateAppearance,
} from "../components/ui/appearance";
import { applyTheme, rememberTheme } from "../components/ui/theme";
import {
  CodeFontSetting,
  ContrastSetting,
  DarkPaletteSetting,
  DepthSetting,
  FontSetting,
  FontSizeSetting,
  LightPaletteSetting,
} from "../settings/appearanceControls";
import { filterSettings } from "../settings/registry";

const root = document.documentElement;

afterEach(() => {
  cleanup();
  localStorage.clear();
  applyTheme("system");
  applyAppearance(defaultAppearance);
});

describe("appearance colors", () => {
  it("normalizes hex input", () => {
    expect(normalizeHex("#ABC")).toBe("#aabbcc");
    expect(normalizeHex("2F71E3")).toBe("#2f71e3");
    expect(normalizeHex(" #070708 ")).toBe("#070708");
    expect(normalizeHex("#12345")).toBeUndefined();
    expect(normalizeHex("red")).toBeUndefined();
  });

  it("keeps derived text readable for every preset", () => {
    for (const mode of ["light", "dark"] as const) {
      for (const preset of themePresets[mode]) {
        for (const contrast of ["default", "strong"] as const) {
          const tokens = deriveTokens(mode, preset.colors, contrast);
          const canvas = tokens["--canvas"]!;
          expect(contrastRatio(tokens["--text"]!, canvas), `${mode} ${preset.id} text`).toBeGreaterThanOrEqual(7);
          expect(contrastRatio(tokens["--muted"]!, canvas), `${mode} ${preset.id} muted`).toBeGreaterThanOrEqual(4.5);
          expect(contrastRatio(tokens["--accent-text"]!, canvas), `${mode} ${preset.id} accent`).toBeGreaterThanOrEqual(4.5);
          expect(
            contrastRatio(tokens["--accent-contrast"]!, tokens["--accent"]!),
            `${mode} ${preset.id} on accent`,
          ).toBeGreaterThanOrEqual(3);
        }
      }
    }
  });

  it("raises secondary text contrast when contrast is strong", () => {
    const colors = themePresets.dark[1]!.colors;
    const normal = deriveTokens("dark", colors, "default");
    const strong = deriveTokens("dark", colors, "strong");
    expect(contrastRatio(strong["--muted"]!, colors.background)).toBeGreaterThan(
      contrastRatio(normal["--muted"]!, colors.background),
    );
  });

  it("moves a low-contrast color until it is readable", () => {
    const fixed = ensureContrast("#315c4e", "#101211", 4.5, "#f3f5f3");
    expect(contrastRatio(fixed, "#101211")).toBeGreaterThanOrEqual(4.5);
    expect(ensureContrast("#000000", "#ffffff", 4.5, "#ffffff")).toBe("#000000");
  });

  it("uses the stylesheet palette until a preset or color is chosen", () => {
    expect(customTokens(defaultAppearance, "dark")).toBeNull();
    const nord = { ...defaultAppearance, dark: { preset: "nord" } };
    expect(customTokens(nord, "dark")?.["--canvas"]).toBe("#2e3440");
    expect(customTokens(nord, "light")).toBeNull();
  });
});

describe("appearance preferences", () => {
  it("falls back to defaults for missing or invalid stored values", () => {
    expect(parseAppearance(null)).toEqual(defaultAppearance);
    expect(parseAppearance("not json")).toEqual(defaultAppearance);
    expect(
      parseAppearance(
        JSON.stringify({
          contrast: "loud",
          depth: "neon",
          font: "comic",
          fontSize: 99,
          light: { preset: "missing" },
          dark: { preset: "custom", colors: { background: "#000", foreground: "nope", accent: "#fff" } },
        }),
      ),
    ).toEqual(defaultAppearance);
    expect(
      parseAppearance(
        JSON.stringify({
          contrast: "strong",
          depth: "glass",
          font: "geist",
          codeFont: "plex-mono",
          fontSize: 16,
          dark: { preset: "custom", colors: { background: "#000", foreground: "#FFF", accent: "2f71e3" } },
        }),
      ),
    ).toEqual({
      ...defaultAppearance,
      contrast: "strong",
      depth: "glass",
      font: "geist",
      codeFont: "plex-mono",
      fontSize: 16,
      dark: { preset: "custom", colors: { background: "#000000", foreground: "#ffffff", accent: "#2f71e3" } },
    });
  });

  it("applies fonts, size, contrast, and the current mode's colors to the root", () => {
    applyTheme("dark");
    updateAppearance((current) => ({
      ...current,
      contrast: "strong",
      font: "geist",
      codeFont: "jetbrains",
      fontSize: 16,
      dark: { preset: "midnight" },
    }));
    expect(root.dataset.contrast).toBe("strong");
    expect(root.style.getPropertyValue("--font-sans")).toContain("Geist Variable");
    expect(root.style.getPropertyValue("--font-mono")).toContain("JetBrains Mono Variable");
    expect(root.style.fontSize).toMatch(/^114\.28/);
    expect(root.style.getPropertyValue("--canvas")).toBe("#070708");
    expect(readAppearance().dark).toEqual({ preset: "midnight" });

    // Switching mode swaps to that mode's palette, here the built-in one.
    rememberTheme("light");
    expect(root.dataset.mode).toBe("light");
    expect(root.style.getPropertyValue("--canvas")).toBe("");

    applyAppearance(defaultAppearance);
    expect(root.dataset.contrast).toBeUndefined();
    expect(root.style.getPropertyValue("--font-sans")).toBe("");
    expect(root.style.fontSize).toBe("");
    for (const name of derivedTokenNames) expect(root.style.getPropertyValue(name)).toBe("");
  });

  it("forgets the stored record when everything is back to default", () => {
    updateAppearance((current) => ({ ...current, fontSize: 15 }));
    expect(localStorage.getItem("meridian.appearance")).not.toBeNull();
    updateAppearance((current) => ({ ...current, fontSize: 14 }));
    expect(localStorage.getItem("meridian.appearance")).toBeNull();
  });
});

describe("appearance settings", () => {
  it("is searchable from the settings window", () => {
    const ids = (query: string) => filterSettings(query)?.map((setting) => setting.id) ?? [];
    expect(ids("contrast")).toContain("contrast");
    expect(ids("monospace")).toContain("code-font");
    expect(ids("glow")).toContain("depth");
    expect(ids("accent")).toEqual(expect.arrayContaining(["light-palette", "dark-palette"]));
  });

  it("switches depth styles and clears the attribute for Flat", async () => {
    const user = userEvent.setup();
    render(<DepthSetting />);
    expect(screen.getByRole("radio", { name: "Flat" })).toHaveAttribute("aria-checked", "true");
    expect(root.dataset.depth).toBeUndefined();
    await user.click(screen.getByRole("radio", { name: "Glow" }));
    expect(root.dataset.depth).toBe("glow");
    expect(readAppearance().depth).toBe("glow");
    expect(screen.getByRole("radio", { name: "Glow" })).toHaveAttribute("aria-checked", "true");
    await user.click(screen.getByRole("radio", { name: "Glass" }));
    expect(root.dataset.depth).toBe("glass");
    await user.click(screen.getByRole("radio", { name: "Flat" }));
    expect(root.dataset.depth).toBeUndefined();
    expect(localStorage.getItem("meridian.appearance")).toBeNull();
  });

  it("changes contrast, fonts, and size from the controls", async () => {
    const user = userEvent.setup();
    render(
      <>
        <ContrastSetting />
        <FontSetting />
        <CodeFontSetting />
        <FontSizeSetting />
      </>,
    );
    await user.click(screen.getByRole("radio", { name: "Strong" }));
    expect(screen.getByRole("radio", { name: "Strong" })).toHaveAttribute("aria-checked", "true");
    expect(root.dataset.contrast).toBe("strong");
    await user.selectOptions(screen.getByRole("combobox", { name: "Font" }), "plex");
    await user.selectOptions(screen.getByRole("combobox", { name: "Code font" }), "geist-mono");
    await user.selectOptions(screen.getByRole("combobox", { name: "Font size" }), "12");
    expect(readAppearance()).toMatchObject({ contrast: "strong", font: "plex", codeFont: "geist-mono", fontSize: 12 });
    expect(root.style.getPropertyValue("--font-mono")).toContain("Geist Mono Variable");
  });

  it("picks a preset, edits a color into a custom palette, and resets", async () => {
    applyTheme("dark");
    const user = userEvent.setup();
    render(<DarkPaletteSetting />);
    const panel = screen.getByRole("region", { name: "Dark theme" });
    await user.selectOptions(within(panel).getByRole("combobox", { name: "Dark theme preset" }), "nord");
    expect(root.style.getPropertyValue("--canvas")).toBe("#2e3440");
    expect(within(panel).getByRole("textbox", { name: "Dark theme accent hex value" })).toHaveValue("88C0D0");

    const accent = within(panel).getByRole("textbox", { name: "Dark theme accent hex value" });
    await user.clear(accent);
    await user.type(accent, "2F71E3");
    expect(readAppearance().dark).toEqual({
      preset: "custom",
      colors: { background: "#2e3440", foreground: "#eceff4", accent: "#2f71e3" },
    });
    expect(root.style.getPropertyValue("--accent")).toBe("#2f71e3");
    expect(within(panel).getByRole("combobox", { name: "Dark theme preset" })).toHaveValue("custom");

    await user.click(within(panel).getByRole("button", { name: "Reset" }));
    expect(readAppearance().dark).toEqual({ preset: "meridian" });
    expect(root.style.getPropertyValue("--canvas")).toBe("");
  });

  it("warns when text and background are too close", async () => {
    const user = userEvent.setup();
    render(<LightPaletteSetting />);
    const panel = screen.getByRole("region", { name: "Light theme" });
    const foreground = within(panel).getByRole("textbox", { name: "Light theme foreground hex value" });
    await user.clear(foreground);
    await user.type(foreground, "D0D8D4");
    expect(within(panel).getByRole("status")).toHaveTextContent(/hard to read/);
  });

  it("previews the other mode without changing the app", () => {
    applyTheme("light");
    updateAppearance((current) => ({ ...current, dark: { preset: "ember" } }));
    const { container } = render(<DarkPaletteSetting />);
    const preview = container.querySelector<HTMLElement>(".settings-palette-preview");
    expect(preview).toHaveAttribute("data-mode", "dark");
    expect(preview?.style.getPropertyValue("--canvas")).toBe("#15110e");
    expect(root.style.getPropertyValue("--canvas")).toBe("");
  });
});
