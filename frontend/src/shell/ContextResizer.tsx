import { useEffect, useRef, useState } from "react";
import type {
  CSSProperties,
  KeyboardEvent as ReactKeyboardEvent,
  PointerEvent as ReactPointerEvent,
} from "react";

const WIDTH_KEY = "meridian.contextPanel.width";
const KEY_STEP = 16;
const KEY_STEP_LARGE = 64;
// Keep in step with styles: the panel's 24rem floor, the Thread column's
// minmax(25rem, 1fr) floor, and the default clamp(24rem, 42vw, 48rem).
const MIN_REM = 24;
const THREAD_MIN_REM = 25;
const DEFAULT_VW = 0.42;
const DEFAULT_MAX_REM = 48;
const RESIZING_CLASS = "resizing-context";

// The panel width is a per-browser convenience. Storage can be missing or
// refuse access, so the panel falls back to its default width.
export function readContextWidth(): number | undefined {
  try {
    const raw = window.localStorage.getItem(WIDTH_KEY);
    if (raw === null) return undefined;
    const value = Number(raw);
    return Number.isFinite(value) && value > 0 ? Math.round(value) : undefined;
  } catch {
    return undefined;
  }
}

function writeContextWidth(width: number | undefined) {
  try {
    if (width === undefined) window.localStorage.removeItem(WIDTH_KEY);
    else window.localStorage.setItem(WIDTH_KEY, String(Math.round(width)));
  } catch {
    // Resizing still works for this page.
  }
}

/**
 * Overrides --context-width on the shell. CSS keeps the result between the
 * panel's floor and what leaves the Thread column its floor, so a stored
 * width stays valid when the window shrinks.
 */
export function contextWidthStyle(width: number | undefined): CSSProperties | undefined {
  if (width === undefined) return undefined;
  return {
    "--context-width": `clamp(${MIN_REM}rem, ${Math.round(width)}px, max(${MIN_REM}rem, calc(100vw - var(--sidebar-width) - ${THREAD_MIN_REM}rem)))`,
  } as CSSProperties;
}

function rootPixels(): number {
  return Number.parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
}

function cssLength(value: string, rem: number, fallback: number): number {
  const trimmed = value.trim();
  const number = Number.parseFloat(trimmed);
  if (!Number.isFinite(number)) return fallback;
  if (trimmed.endsWith("rem")) return number * rem;
  if (trimmed.endsWith("px")) return number;
  return fallback;
}

function bounds(): { min: number; max: number } {
  const rem = rootPixels();
  const sidebar = cssLength(
    getComputedStyle(document.documentElement).getPropertyValue("--sidebar-width"),
    rem,
    18 * rem,
  );
  const min = MIN_REM * rem;
  return { min, max: Math.max(min, window.innerWidth - sidebar - THREAD_MIN_REM * rem) };
}

function clampWidth(width: number): number {
  const { min, max } = bounds();
  return Math.round(Math.min(max, Math.max(min, width)));
}

function defaultWidth(): number {
  const rem = rootPixels();
  return clampWidth(Math.min(DEFAULT_MAX_REM * rem, Math.max(MIN_REM * rem, window.innerWidth * DEFAULT_VW)));
}

/**
 * The drag handle on the left edge of the Capsule tools panel. It renders
 * inside the shell so it reads the same --context-width as the panel.
 */
export function ContextResizer({
  width,
  onChange,
}: {
  width: number | undefined;
  onChange: (width: number | undefined) => void;
}) {
  const [, setViewport] = useState(0);
  const dragging = useRef(false);
  const pending = useRef<number | undefined>(undefined);
  const frame = useRef<number | undefined>(undefined);
  const latest = useRef(width);
  latest.current = width;

  // aria-valuenow and the bounds depend on the window size.
  useEffect(() => {
    const onResize = () => setViewport((value) => value + 1);
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      if (frame.current !== undefined) cancelAnimationFrame(frame.current);
      document.documentElement.classList.remove(RESIZING_CLASS);
    };
  }, []);

  const { min, max } = bounds();
  const current = width === undefined ? defaultWidth() : clampWidth(width);

  const commit = (next: number | undefined) => {
    latest.current = next;
    onChange(next);
    writeContextWidth(next);
  };

  const endDrag = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!dragging.current) return;
    dragging.current = false;
    // Pointer capture is missing in some test DOMs; the drag still works.
    if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    document.documentElement.classList.remove(RESIZING_CLASS);
    if (frame.current !== undefined) {
      cancelAnimationFrame(frame.current);
      frame.current = undefined;
    }
    const next = pending.current ?? latest.current;
    pending.current = undefined;
    if (next !== undefined) commit(next);
  };

  const onPointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    event.preventDefault();
    event.currentTarget.setPointerCapture?.(event.pointerId);
    dragging.current = true;
    document.documentElement.classList.add(RESIZING_CLASS);
  };

  const onPointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!dragging.current) return;
    // The panel is the last column of a full-width shell.
    pending.current = clampWidth(window.innerWidth - event.clientX);
    if (frame.current !== undefined) return;
    frame.current = requestAnimationFrame(() => {
      frame.current = undefined;
      if (pending.current !== undefined) onChange(pending.current);
    });
  };

  const onKeyDown = (event: ReactKeyboardEvent<HTMLDivElement>) => {
    const step = event.shiftKey ? KEY_STEP_LARGE : KEY_STEP;
    let next: number | undefined;
    if (event.key === "ArrowLeft") next = current + step;
    else if (event.key === "ArrowRight") next = current - step;
    else if (event.key === "Home") next = min;
    else if (event.key === "End") next = max;
    else return;
    event.preventDefault();
    commit(clampWidth(next));
  };

  return (
    <div
      className="context-resizer"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize Capsule tools"
      aria-valuenow={current}
      aria-valuemin={Math.round(min)}
      aria-valuemax={Math.round(max)}
      tabIndex={0}
      title="Drag to resize. Double-click to reset."
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      onDoubleClick={() => commit(undefined)}
      onKeyDown={onKeyDown}
    />
  );
}
