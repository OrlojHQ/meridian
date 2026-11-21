import type { Thread } from "../api/generated/types.gen";

// Context fill, as a structured session's harness last reported it (ADR 0059).
// The numbers are counts held in the daemon's memory; the Thread carries
// them only while its session is live and reporting.

// A plain Thread shows the meter once the context is half full: below that
// the operator has nothing to act on.
export const CONTEXT_METER_THRESHOLD = 50;

export type ContextUsage = NonNullable<Thread["contextUsage"]>;

export function contextUsage(thread: Pick<Thread, "contextUsage">): ContextUsage | undefined {
  const usage = thread.contextUsage;
  if (!usage || !Number.isFinite(usage.tokens) || !Number.isFinite(usage.window)) return undefined;
  if (usage.tokens <= 0 || usage.window <= 0) return undefined;
  return usage;
}

// Whole percent of the window in use. A harness may count slightly past its
// window just before it compacts, so the figure stops at 100.
export const contextPercent = (usage: ContextUsage) =>
  Math.min(100, Math.round((usage.tokens / usage.window) * 100));

// 200000 -> "200k", 1000000 -> "1M", 1500000 -> "1.5M", 950 -> "950".
export function compactTokens(value: number) {
  const trim = (number: number) => number.toFixed(1).replace(/\.0$/, "");
  if (value >= 1_000_000) return `${trim(value / 1_000_000)}M`;
  if (value >= 1_000) return `${trim(value / 1_000)}k`.replace(/^(\d{3,})\.\dk$/, "$1k");
  return String(Math.round(value));
}

export const contextLabel = (usage: ContextUsage) =>
  `${contextPercent(usage)}% of ${compactTokens(usage.window)}`;

export const contextDetail = (usage: ContextUsage) =>
  `${contextLabel(usage)} (${usage.tokens.toLocaleString("en-US")} of ${usage.window.toLocaleString("en-US")} tokens)`;

export const isLeadThread = (thread: Pick<Thread, "coordinationGrantId" | "parentThreadId">) =>
  Boolean(thread.coordinationGrantId && !thread.parentThreadId);

// Plain Threads show the meter when it is worth acting on. Leads never do: a
// lead is meant to read as one continuous conversation, and its fill is in
// the Thread's details instead.
export function contextMeterUsage(
  thread: Pick<Thread, "contextUsage" | "coordinationGrantId" | "parentThreadId">,
): ContextUsage | undefined {
  if (isLeadThread(thread)) return undefined;
  const usage = contextUsage(thread);
  return usage && contextPercent(usage) >= CONTEXT_METER_THRESHOLD ? usage : undefined;
}
