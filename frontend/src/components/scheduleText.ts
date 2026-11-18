import type { Schedule, ScheduleFire } from "../api/generated/types.gen";

// Plain-language text for schedules (ADR 0057). Everything here is derived
// from content-free fields: cadence, codes, and times. No prompt text.

export type CadenceMode = "hourly" | "daily" | "weekdays" | "weekly" | "custom";

export type CadenceParts = {
  mode: CadenceMode;
  minute: number;
  hour: number;
  weekday: number;
  custom: string;
};

export const weekdayNames = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

const pad = (value: number) => String(value).padStart(2, "0");

export function clockTime(hour: number, minute: number) {
  return `${pad(hour)}:${pad(minute)}`;
}

/** buildCadence turns the form's parts into a five-field cron expression. */
export function buildCadence(parts: CadenceParts): string {
  const { minute, hour, weekday } = parts;
  switch (parts.mode) {
    case "hourly":
      return `${minute} * * * *`;
    case "daily":
      return `${minute} ${hour} * * *`;
    case "weekdays":
      return `${minute} ${hour} * * 1-5`;
    case "weekly":
      return `${minute} ${hour} * * ${weekday}`;
    default:
      return parts.custom.trim();
  }
}

const number = (value: string, max: number) => {
  if (!/^\d{1,2}$/.test(value)) return undefined;
  const parsed = Number(value);
  return parsed <= max ? parsed : undefined;
};

/** parseCadence recognizes the shapes buildCadence writes. Anything else is custom. */
export function parseCadence(expression: string): CadenceParts {
  const custom: CadenceParts = { mode: "custom", minute: 0, hour: 9, weekday: 1, custom: expression };
  const fields = expression.trim().split(/\s+/);
  if (fields.length !== 5) return custom;
  const [m, h, dom, month, dow] = fields;
  const minute = number(m, 59);
  if (minute === undefined || dom !== "*" || month !== "*") return custom;
  if (h === "*" && dow === "*") return { ...custom, mode: "hourly", minute };
  const hour = number(h, 23);
  if (hour === undefined) return custom;
  if (dow === "*") return { ...custom, mode: "daily", minute, hour };
  if (dow === "1-5") return { ...custom, mode: "weekdays", minute, hour };
  const weekday = number(dow, 7);
  if (weekday === undefined) return custom;
  return { ...custom, mode: "weekly", minute, hour, weekday: weekday % 7 };
}

/** describeCadence is the one-line "when" for a schedule card. */
export function describeCadence(expression: string): string {
  const parts = parseCadence(expression);
  const at = clockTime(parts.hour, parts.minute);
  switch (parts.mode) {
    case "hourly":
      return parts.minute === 0 ? "Every hour" : `Every hour at :${pad(parts.minute)}`;
    case "daily":
      return `Every day at ${at}`;
    case "weekdays":
      return `Weekdays at ${at}`;
    case "weekly":
      return `${weekdayNames[parts.weekday]}s at ${at}`;
    default: {
      const every = /^(\d{1,2}) \*\/(\d{1,2}) \* \* \*$/.exec(expression.trim());
      if (every && Number(every[1]) < 60 && Number(every[2]) > 1) {
        const minute = Number(every[1]);
        return `Every ${every[2]} hours${minute ? ` at :${pad(minute)}` : ""}`;
      }
      return `Cron ${expression}`;
    }
  }
}

const disabledReasons: Record<string, string> = {
  start_failures: "Turned off after 3 failed starts",
  harness_not_allowed: "Turned off: the harness is no longer on this Project",
  transferred: "Turned off when it moved to you. Read the prompt, then turn it on.",
};

export function disabledText(schedule: Schedule): string {
  if (schedule.enabled) return "";
  return disabledReasons[schedule.disabledReason ?? ""] ?? "Off";
}

const skipReasons: Record<string, string> = {
  overlap: "Skipped: the previous run was still going",
  backlog: "Skipped: runs are waiting for you",
  capacity: "Skipped: too many Capsules were running",
  owner_unavailable: "Skipped: the owner is disabled",
  project_unavailable: "Skipped: the Project is not accepting work",
  harness_not_allowed: "Skipped: the harness is no longer on this Project",
};

const failureReasons: Record<string, string> = {
  start_failed: "the Capsule did not start",
  model_unreported: "the harness never reported its models",
  model_unsupported: "this harness cannot switch models",
  model_unavailable: "the model is not offered",
  model_refused: "the harness refused the model",
  effort_unsupported: "this harness cannot set effort",
  effort_unavailable: "the effort level is not offered",
  effort_refused: "the harness refused the effort level",
  session_ended: "the session ended before the prompt",
};

export type FireTone = "ready" | "paused" | "failed" | "ended" | "running";

/** fireSummary is a label and a badge tone for one fire. */
export function fireSummary(fire: ScheduleFire): { label: string; tone: FireTone } {
  switch (fire.state) {
    case "skipped":
      return { label: skipReasons[fire.reason ?? ""] ?? "Skipped", tone: "ended" };
    case "missed":
      return { label: "Missed: the Station was not running", tone: "ended" };
    case "starting":
      return { label: "Starting a Capsule", tone: "running" };
    case "selecting":
      return { label: "Setting the model", tone: "running" };
    case "cleaning":
      return { label: "No changes. Deleting the Capsule", tone: "running" };
    case "running":
      if (fire.outcome === "waiting") return { label: "Waiting on you", tone: "failed" };
      return { label: "Running", tone: "running" };
  }
  switch (fire.outcome) {
    case "no_changes":
      return { label: "No changes. Capsule deleted", tone: "ended" };
    case "changed":
      return { label: "Changes to review", tone: "ready" };
    case "proposed":
      return { label: "Proposed a delivery", tone: "ready" };
    case "waiting":
      return { label: "Waiting on you", tone: "failed" };
    case "timed_out":
      return { label: "Hit the time limit. Capsule paused", tone: "paused" };
    case "failed": {
      const why = failureReasons[fire.reason ?? ""];
      return { label: why ? `Failed: ${why}` : "Failed", tone: "failed" };
    }
  }
  return { label: "Finished", tone: "ended" };
}

/** fireKeptCapsule reports whether the fire left a Capsule to open. */
export function fireKeptCapsule(fire: ScheduleFire): boolean {
  return Boolean(fire.capsuleId) && !fire.capsuleDeleted && fire.state !== "cleaning";
}

const triggerLabels: Record<string, string> = {
  clock: "On time",
  catch_up: "Caught up",
  manual: "Run now",
};

export function triggerLabel(trigger: string) {
  return triggerLabels[trigger] ?? trigger;
}

export function formatWhen(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "";
  const at = new Date(iso);
  const absolute = at.toLocaleString(undefined, {
    weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit",
  });
  const delta = at.getTime() - now;
  const minutes = Math.round(Math.abs(delta) / 60_000);
  let relative: string;
  if (minutes < 1) relative = "now";
  else if (minutes < 60) relative = `${minutes} min`;
  else if (minutes < 48 * 60) relative = `${Math.round(minutes / 60)} h`;
  else relative = `${Math.round(minutes / (24 * 60))} days`;
  if (relative === "now") return `${absolute} (now)`;
  return delta >= 0 ? `${absolute} (in ${relative})` : `${absolute} (${relative} ago)`;
}

export function durationText(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600} h`;
  if (seconds % 60 === 0) return `${seconds / 60} min`;
  return `${seconds} s`;
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}
