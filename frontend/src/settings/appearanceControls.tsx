import { useEffect, useLayoutEffect, useRef, useState } from "react";

import { SyntaxTokenLine, syntaxHighlighter, type HighlightToken } from "../components/SyntaxCode";
import {
  applyColorTokens,
  codeFonts,
  contrastRatio,
  customPresetId,
  defaultPresetId,
  depths,
  fontSizes,
  normalizeHex,
  themeColors,
  themePresets,
  uiFonts,
  updateAppearance,
  useAppearance,
  type Appearance,
  type Contrast,
  type ThemeColors,
  type ThemeMode,
} from "../components/ui/appearance";
import { Button } from "../components/ui/Button";
import { Select } from "../components/ui/Field";
import { CheckIcon } from "../components/ui/Icons";
import { readTheme, rememberTheme, themeLabel, type ThemePreference } from "../components/ui/theme";

const themeDescriptions: Record<ThemePreference, string> = {
  system: "Follows OS setting",
  light: "Crisp light surface",
  dark: "Low-glare dark surface",
};

/** Applies one mode's palette to an element so it can preview that mode. */
function usePalette<T extends HTMLElement>(mode: ThemeMode, appearance: Appearance) {
  const ref = useRef<T>(null);
  useLayoutEffect(() => {
    if (ref.current) applyColorTokens(appearance, mode, ref.current);
  }, [appearance, mode]);
  return ref;
}

function ThemeCardPreview({ value, appearance }: { value: ThemePreference; appearance: Appearance }) {
  const mode = value === "system" ? undefined : value;
  const ref = usePalette<HTMLSpanElement>(mode ?? "light", appearance);
  return (
    <span
      ref={mode ? ref : undefined}
      data-mode={mode}
      className={`settings-theme-preview settings-theme-preview--${value}`}
      aria-hidden="true"
    >
      <span className="settings-theme-preview-rail" />
      <span className="settings-theme-preview-main">
        <span className="settings-theme-preview-bar" />
        <span className="settings-theme-preview-line is-wide" />
        <span className="settings-theme-preview-line is-short" />
      </span>
    </span>
  );
}

export function ThemeSetting() {
  const [theme, setTheme] = useState<ThemePreference>(readTheme);
  const appearance = useAppearance();
  return (
    <div className="settings-theme-grid" role="radiogroup" aria-label="Color theme">
      {(["system", "light", "dark"] as const).map((value) => {
        const active = theme === value;
        return (
          <button
            key={value}
            type="button"
            role="radio"
            aria-checked={active}
            aria-label={themeLabel(value)}
            className={`settings-theme-card${active ? " is-active" : ""}`}
            onClick={() => {
              setTheme(value);
              rememberTheme(value);
            }}
          >
            <ThemeCardPreview value={value} appearance={appearance} />
            <span className="settings-theme-meta">
              <span className="settings-theme-title">
                <strong>{themeLabel(value)}</strong>
                {active && (
                  <span className="settings-theme-check" aria-hidden="true">
                    <CheckIcon />
                  </span>
                )}
              </span>
              <span className="settings-theme-desc">{themeDescriptions[value]}</span>
            </span>
          </button>
        );
      })}
    </div>
  );
}

export function ContrastSetting() {
  const { contrast } = useAppearance();
  const choices: { value: Contrast; label: string }[] = [
    { value: "default", label: "Default" },
    { value: "strong", label: "Strong" },
  ];
  return (
    <div className="settings-segmented" role="radiogroup" aria-label="Contrast">
      {choices.map((choice) => (
        <button
          key={choice.value}
          type="button"
          role="radio"
          aria-checked={contrast === choice.value}
          onClick={() => updateAppearance((current) => ({ ...current, contrast: choice.value }))}
        >
          {choice.label}
        </button>
      ))}
    </div>
  );
}

export function DepthSetting() {
  const { depth } = useAppearance();
  return (
    <div className="settings-segmented" role="radiogroup" aria-label="Depth">
      {depths.map((choice) => (
        <button
          key={choice.value}
          type="button"
          role="radio"
          aria-checked={depth === choice.value}
          onClick={() => updateAppearance((current) => ({ ...current, depth: choice.value }))}
        >
          {choice.label}
        </button>
      ))}
    </div>
  );
}

export function FontSetting() {
  const { font } = useAppearance();
  return (
    <Select
      aria-label="Font"
      value={font}
      onChange={(event) => updateAppearance((current) => ({ ...current, font: event.target.value }))}
    >
      {uiFonts.map((choice) => (
        <option key={choice.id} value={choice.id}>
          {choice.label}
        </option>
      ))}
    </Select>
  );
}

export function CodeFontSetting() {
  const { codeFont } = useAppearance();
  return (
    <Select
      aria-label="Code font"
      value={codeFont}
      onChange={(event) => updateAppearance((current) => ({ ...current, codeFont: event.target.value }))}
    >
      {codeFonts.map((choice) => (
        <option key={choice.id} value={choice.id}>
          {choice.label}
        </option>
      ))}
    </Select>
  );
}

export function FontSizeSetting() {
  const { fontSize } = useAppearance();
  return (
    <Select
      aria-label="Font size"
      value={String(fontSize)}
      onChange={(event) =>
        updateAppearance((current) => ({ ...current, fontSize: Number(event.target.value) }))
      }
    >
      {fontSizes.map((size) => (
        <option key={size} value={size}>
          {size}px
        </option>
      ))}
    </Select>
  );
}

function ColorField({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  const [draft, setDraft] = useState(value.slice(1).toUpperCase());
  useEffect(() => setDraft(value.slice(1).toUpperCase()), [value]);
  return (
    <div className="settings-color-field">
      <input
        type="color"
        className="settings-color-swatch"
        aria-label={`${label} color picker`}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
      <span className="settings-color-hash" aria-hidden="true">
        #
      </span>
      <input
        type="text"
        className="settings-color-hex"
        aria-label={`${label} hex value`}
        spellCheck={false}
        autoComplete="off"
        maxLength={7}
        value={draft}
        onChange={(event) => {
          setDraft(event.target.value.replace(/^#/, "").toUpperCase());
          const next = normalizeHex(event.target.value);
          if (next && event.target.value.replace(/^#/, "").length === 6) onChange(next);
        }}
        onBlur={() => {
          const next = normalizeHex(draft);
          if (next) onChange(next);
          else setDraft(value.slice(1).toUpperCase());
        }}
      />
    </div>
  );
}

const previewSource = `export function greet(name: string) {
  // Say hello from Meridian
  const message = \`Hello, \${name}!\`;
  return { message, length: 42 };
}`;

function PalettePreview({ mode, appearance }: { mode: ThemeMode; appearance: Appearance }) {
  const ref = usePalette<HTMLDivElement>(mode, appearance);
  const [tokens, setTokens] = useState<HighlightToken[][]>();
  useEffect(() => {
    let active = true;
    void syntaxHighlighter()
      .then((highlighter) => {
        const result = highlighter.codeToTokens(previewSource, {
          lang: "typescript",
          theme: mode === "dark" ? "github-dark" : "github-light",
        });
        if (active) setTokens(result.tokens);
      })
      .catch(() => {
        // The preview stays readable as plain text.
      });
    return () => {
      active = false;
    };
  }, [mode]);
  return (
    <div ref={ref} data-mode={mode} className="settings-palette-preview">
      <div className="settings-palette-preview-bar">
        <span className="settings-palette-preview-title">greet.ts</span>
        <span className="ui-badge state-ready">Ready</span>
      </div>
      <pre className="settings-palette-preview-code">
        <code>
          {previewSource.split("\n").map((line, index) => (
            <span key={index} className="settings-palette-preview-line">
              <span className="settings-palette-preview-gutter" aria-hidden="true">
                {index + 1}
              </span>
              <SyntaxTokenLine tokens={tokens?.[index]} fallback={line} />
              {"\n"}
            </span>
          ))}
        </code>
      </pre>
      <div className="settings-palette-preview-foot">
        <span className="settings-palette-preview-muted">2 files changed</span>
        <span className="settings-palette-preview-actions">
          <Button variant="secondary" tabIndex={-1}>
            Discard
          </Button>
          <Button variant="primary" tabIndex={-1}>
            Commit
          </Button>
        </span>
      </div>
    </div>
  );
}

function ThemePalette({ mode }: { mode: ThemeMode }) {
  const appearance = useAppearance();
  const choice = appearance[mode];
  const colors = themeColors(appearance, mode);
  const title = mode === "light" ? "Light theme" : "Dark theme";
  const ratio = contrastRatio(colors.background, colors.foreground);
  const setChoice = (next: Appearance[ThemeMode]) =>
    updateAppearance((current) => ({ ...current, [mode]: next }));
  const setColor = (key: keyof ThemeColors, value: string) => {
    if (colors[key] === value) return;
    setChoice({ preset: customPresetId, colors: { ...colors, [key]: value } });
  };
  const fields: { key: keyof ThemeColors; label: string }[] = [
    { key: "background", label: "Background" },
    { key: "foreground", label: "Foreground" },
    { key: "accent", label: "Accent" },
  ];
  return (
    <section className="settings-palette" aria-label={title}>
      <header className="settings-palette-head">
        <h3>{title}</h3>
        {(choice.preset !== defaultPresetId || choice.colors) && (
          <Button variant="secondary" onClick={() => setChoice({ preset: defaultPresetId })}>
            Reset
          </Button>
        )}
      </header>
      <div className="settings-palette-rows">
        <label className="settings-palette-row">
          <span>Preset</span>
          <Select
            aria-label={`${title} preset`}
            value={choice.preset}
            onChange={(event) => {
              if (event.target.value !== customPresetId) setChoice({ preset: event.target.value });
            }}
          >
            {themePresets[mode].map((preset) => (
              <option key={preset.id} value={preset.id}>
                {preset.label}
              </option>
            ))}
            {choice.preset === customPresetId && <option value={customPresetId}>Custom</option>}
          </Select>
        </label>
        {fields.map((field) => (
          <div key={field.key} className="settings-palette-row">
            <span>{field.label}</span>
            <ColorField
              label={`${title} ${field.label.toLowerCase()}`}
              value={colors[field.key]}
              onChange={(value) => setColor(field.key, value)}
            />
          </div>
        ))}
        {ratio < 4.5 && (
          <p className="settings-palette-warning" role="status">
            Text and background contrast is {ratio.toFixed(1)}:1. Text may be hard to read below 4.5:1.
          </p>
        )}
        <details className="settings-palette-details">
          <summary>Preview</summary>
          <PalettePreview mode={mode} appearance={appearance} />
        </details>
      </div>
    </section>
  );
}

export function LightPaletteSetting() {
  return <ThemePalette mode="light" />;
}

export function DarkPaletteSetting() {
  return <ThemePalette mode="dark" />;
}
