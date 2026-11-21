import type { Routine, RoutineFire } from "../api/generated/types.gen";

// Plain-language text for routines (ADR 0057). Everything here is derived
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

/** describeCadence is the one-line "when" for a routine card. */
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

export function disabledText(routine: Routine): string {
  if (routine.enabled) return "";
  return disabledReasons[routine.disabledReason ?? ""] ?? "Off";
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
export function fireSummary(fire: RoutineFire): { label: string; tone: FireTone } {
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
export function fireKeptCapsule(fire: RoutineFire): boolean {
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

// A signed offset such as UTC−06:00. The sign is a minus, not a hyphen.
const minusSign = "\u2212";

const fallbackTimeZones = [
  "UTC",
  "Pacific/Honolulu",
  "America/Anchorage",
  "America/Los_Angeles",
  "America/Denver",
  "America/Chicago",
  "America/New_York",
  "America/Sao_Paulo",
  "Europe/London",
  "Europe/Berlin",
  "Europe/Paris",
  "Africa/Cairo",
  "Africa/Johannesburg",
  "Asia/Dubai",
  "Asia/Kolkata",
  "Asia/Singapore",
  "Asia/Shanghai",
  "Asia/Tokyo",
  "Australia/Sydney",
  "Pacific/Auckland",
];

const maxCadenceBytes = 128;
const maxTimeZoneBytes = 64;
const cadenceHorizon = 5 * 366;

const cadenceMacros: Record<string, string> = {
  "@hourly": "0 * * * *",
  "@daily": "0 0 * * *",
  "@midnight": "0 0 * * *",
  "@weekly": "0 0 * * 0",
  "@monthly": "0 0 1 * *",
  "@yearly": "0 0 1 1 *",
  "@annually": "0 0 1 1 *",
};

export type TimeZoneChoice = {
  id: string;
  region: string;
  offset: string;
  time: string;
  local: boolean;
  /** A saved zone the runtime's list does not contain. */
  savedOnly: boolean;
  search: string;
};

type ZoneFacts = { id: string; region: string; search: string };

type ParsedCron = {
  minutes: number[];
  hours: number[];
  days: boolean[];
  months: boolean[];
  weekdays: boolean[];
  anyDay: boolean;
  anyWeekday: boolean;
};

type Wall = { year: number; month: number; day: number; hour: number; minute: number; second: number };

const factsCache = new Map<string, ZoneFacts[]>();
const choiceCache = new Map<string, TimeZoneChoice[]>();
const wallFormatters = new Map<string, Intl.DateTimeFormat>();
const clockFormatters = new Map<string, Intl.DateTimeFormat>();

function listedTimeZones(): string[] {
  let ids: string[] = [];
  try {
    if (typeof Intl.supportedValuesOf === "function") {
      const supported = Intl.supportedValuesOf("timeZone");
      if (supported.length > 0) ids = [...supported];
    }
  } catch {
    // The runtime cannot list zones. The fallback still offers a choice.
  }
  if (ids.length === 0) ids = [...fallbackTimeZones];
  if (!ids.includes("UTC")) ids.unshift("UTC");
  return ids;
}

function regionOf(id: string) {
  const slash = id.indexOf("/");
  return slash > 0 ? id.slice(0, slash) : "";
}

function searchText(id: string): string {
  const loose = id.replaceAll("_", " ").replaceAll("/", " ");
  const names: string[] = [];
  const year = new Date().getFullYear();
  const samples = [Date.UTC(year, 0, 15, 12), Date.UTC(year, 6, 15, 12)];
  for (const sample of samples) {
    for (const timeZoneName of ["long", "short", "longGeneric", "shortGeneric"] as const) {
      try {
        const value = new Intl.DateTimeFormat("en-US", { timeZone: id, timeZoneName })
          .formatToParts(new Date(sample))
          .find((part) => part.type === "timeZoneName")?.value;
        if (value && value !== id) names.push(value);
      } catch {
        return `${id} ${loose}`.toLowerCase();
      }
    }
  }
  return [id, loose, ...names].join(" ").toLowerCase();
}

function factFor(id: string): ZoneFacts {
  return { id, region: regionOf(id), search: searchText(id) };
}

function listKey(ids: string[]) {
  let sum = 0;
  for (let index = 0; index < ids.length; index += 17) {
    sum = (sum + ids[index].length * (index + 1)) % 1000003;
  }
  return `${ids.length}:${ids[0] ?? ""}:${ids[ids.length - 1] ?? ""}:${sum}`;
}

function zoneFacts(): ZoneFacts[] {
  const ids = listedTimeZones();
  const key = listKey(ids);
  const cached = factsCache.get(key);
  if (cached) return cached;
  const facts = ids.map((id) => factFor(id));
  factsCache.set(key, facts);
  return facts;
}

function wallFormatter(timeZone: string) {
  let fmt = wallFormatters.get(timeZone);
  if (!fmt) {
    fmt = new Intl.DateTimeFormat("en-US", {
      timeZone,
      hourCycle: "h23",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
    wallFormatters.set(timeZone, fmt);
  }
  return fmt;
}

function zoneWall(date: Date, timeZone: string): Wall {
  const parts: Record<string, string> = {};
  for (const part of wallFormatter(timeZone).formatToParts(date)) {
    if (part.type !== "literal") parts[part.type] = part.value;
  }
  return {
    year: Number(parts.year),
    month: Number(parts.month),
    day: Number(parts.day),
    hour: Number(parts.hour),
    minute: Number(parts.minute),
    second: Number(parts.second),
  };
}

function offsetMinutes(date: Date, timeZone: string) {
  const wall = zoneWall(date, timeZone);
  const asUTC = Date.UTC(wall.year, wall.month - 1, wall.day, wall.hour, wall.minute, wall.second);
  return Math.round((asUTC - date.getTime()) / 60_000);
}

function formatOffset(minutes: number) {
  const sign = minutes < 0 ? minusSign : "+";
  const absolute = Math.abs(minutes);
  const hours = Math.floor(absolute / 60);
  const mins = absolute % 60;
  return `UTC${sign}${String(hours).padStart(2, "0")}:${String(mins).padStart(2, "0")}`;
}

function clockIn(date: Date, timeZone: string) {
  let fmt = clockFormatters.get(timeZone);
  if (!fmt) {
    fmt = new Intl.DateTimeFormat(undefined, {
      timeZone,
      hour: "numeric",
      minute: "2-digit",
    });
    clockFormatters.set(timeZone, fmt);
  }
  return fmt.format(date);
}

function describeZone(fact: ZoneFacts, now: Date, local: boolean, savedOnly: boolean): TimeZoneChoice {
  let offset = "";
  let time = "";
  try {
    offset = formatOffset(offsetMinutes(now, fact.id));
    time = clockIn(now, fact.id);
  } catch {
    // A saved zone the runtime cannot resolve still stays on the routine.
  }
  const offsetSearch = offset
    ? ` ${offset.toLowerCase()} ${offset.replaceAll(minusSign, "-").toLowerCase()}`
    : "";
  return {
    id: fact.id,
    region: fact.region,
    offset,
    time,
    local,
    savedOnly,
    search: `${fact.search}${offsetSearch}${local ? " your local zone" : ""}`,
  };
}

/** timeZoneChoices lists zones for the picker. The browser's zone is first. */
export function timeZoneChoices(now = new Date(), selected = ""): TimeZoneChoice[] {
  const localId = browserTimeZone();
  const facts = zoneFacts();
  const minute = Math.floor(now.getTime() / 60_000);
  const key = `${minute}\0${localId}\0${selected}\0${facts.length}:${facts[0]?.id ?? ""}:${facts[facts.length - 1]?.id ?? ""}`;
  const cached = choiceCache.get(key);
  if (cached) return cached;
  if (choiceCache.size > 8) choiceCache.clear();

  const known = new Set(facts.map((fact) => fact.id));
  const local = facts.find((fact) => fact.id === localId) ?? factFor(localId);
  const saved =
    selected && selected !== local.id && !known.has(selected) ? factFor(selected) : undefined;
  const choices = [describeZone(local, now, true, false)];
  if (saved) choices.push(describeZone(saved, now, false, true));
  for (const fact of facts) {
    if (fact.id === local.id) continue;
    choices.push(describeZone(fact, now, false, false));
  }
  // UTC stays near the top, after the browser zone and any saved zone the list lacks.
  const utcAt = choices.findIndex((choice) => choice.id === "UTC");
  if (utcAt > 1) {
    const [utc] = choices.splice(utcAt, 1);
    choices.splice(saved ? 2 : 1, 0, utc);
  }
  choiceCache.set(key, choices);
  return choices;
}

/** filterTimeZones keeps zones whose city, region, name, or offset contains every term. */
export function filterTimeZones(choices: TimeZoneChoice[], query: string): TimeZoneChoice[] {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return choices;
  return choices.filter((choice) => terms.every((term) => choice.search.includes(term)));
}

function parseField(field: string, low: number, high: number): boolean[] | undefined {
  const set = Array<boolean>(high + 1).fill(false);
  for (const part of field.split(",")) {
    if (part === "") return undefined;
    const slash = part.indexOf("/");
    const rangePart = slash >= 0 ? part.slice(0, slash) : part;
    const stepPart = slash >= 0 ? part.slice(slash + 1) : undefined;
    let step = 1;
    if (stepPart !== undefined) {
      if (!/^\d+$/.test(stepPart)) return undefined;
      step = Number(stepPart);
      if (step < 1 || step > high) return undefined;
    }
    let start = low;
    let end = high;
    if (rangePart === "*") {
      // The whole span.
    } else if (rangePart.includes("-")) {
      const dash = rangePart.indexOf("-");
      const first = rangePart.slice(0, dash);
      const last = rangePart.slice(dash + 1);
      if (!/^\d+$/.test(first) || !/^\d+$/.test(last)) return undefined;
      const lower = Number(first);
      const upper = Number(last);
      if (lower < low || upper > high || lower > upper) return undefined;
      start = lower;
      end = upper;
    } else {
      if (!/^\d+$/.test(rangePart)) return undefined;
      const value = Number(rangePart);
      if (value < low || value > high) return undefined;
      start = value;
      if (stepPart === undefined) end = value;
    }
    for (let value = start; value <= end; value += step) set[value] = true;
  }
  return set;
}

function selectedValues(set: boolean[], low: number) {
  const values: number[] = [];
  for (let value = low; value < set.length; value += 1) {
    if (set[value]) values.push(value);
  }
  return values;
}

// parseCron reads the same five-field form the server accepts, including
// lists, ranges, and steps. Macros expand to that form.
function parseCron(expression: string): ParsedCron | undefined {
  let text = expression.trim();
  if (text === "" || text.length > maxCadenceBytes) return undefined;
  const macro = cadenceMacros[text.toLowerCase()];
  if (macro) text = macro;
  const fields = text.split(/\s+/);
  if (fields.length !== 5) return undefined;
  const minutes = parseField(fields[0], 0, 59);
  const hours = parseField(fields[1], 0, 23);
  const days = parseField(fields[2], 1, 31);
  const months = parseField(fields[3], 1, 12);
  const dow = parseField(fields[4], 0, 7);
  if (!minutes || !hours || !days || !months || !dow) return undefined;
  const weekdays = Array<boolean>(7).fill(false);
  for (let day = 0; day < 7; day += 1) weekdays[day] = dow[day] === true;
  if (dow[7]) weekdays[0] = true;
  return {
    minutes: selectedValues(minutes, 0),
    hours: selectedValues(hours, 0),
    days,
    months,
    weekdays,
    anyDay: fields[2] === "*",
    anyWeekday: fields[4] === "*",
  };
}

function zonedDate(
  year: number,
  month: number,
  day: number,
  hour: number,
  minute: number,
  timeZone: string,
): Date | undefined {
  const utcGuess = Date.UTC(year, month - 1, day, hour, minute, 0);
  const guess = new Date(utcGuess);
  if (
    guess.getUTCFullYear() !== year ||
    guess.getUTCMonth() !== month - 1 ||
    guess.getUTCDate() !== day ||
    guess.getUTCHours() !== hour ||
    guess.getUTCMinutes() !== minute
  ) {
    return undefined;
  }
  let shift = offsetMinutes(guess, timeZone);
  let utc = utcGuess - shift * 60_000;
  const again = offsetMinutes(new Date(utc), timeZone);
  if (again !== shift) {
    shift = again;
    utc = utcGuess - shift * 60_000;
  }
  const wall = zoneWall(new Date(utc), timeZone);
  const wallHour = wall.hour === 24 ? 0 : wall.hour;
  if (wall.year !== year || wall.month !== month || wall.day !== day || wallHour !== hour || wall.minute !== minute) {
    return undefined;
  }
  return new Date(utc);
}

function weekdayOf(year: number, month: number, day: number, timeZone: string) {
  const probe = zonedDate(year, month, day, 12, 0, timeZone);
  if (!probe) return undefined;
  const name = new Intl.DateTimeFormat("en-US", { timeZone, weekday: "short" }).format(probe);
  const index = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"].indexOf(name);
  return index >= 0 ? index : undefined;
}

function dayMatches(cron: ParsedCron, year: number, month: number, day: number, timeZone: string) {
  if (!cron.months[month]) return false;
  const dom = cron.days[day] === true;
  if (cron.anyDay && cron.anyWeekday) return true;
  if (cron.anyWeekday) return dom;
  const weekday = weekdayOf(year, month, day, timeZone);
  if (weekday === undefined) return false;
  if (cron.anyDay) return cron.weekdays[weekday] === true;
  return dom || cron.weekdays[weekday] === true;
}

/**
 * nextCadenceFire is the first fire strictly after `after`. A wall time skipped
 * by a daylight-saving change does not fire, and a wall time that happens
 * twice fires once. An expression or zone the server would reject has no next fire.
 */
export function nextCadenceFire(expression: string, zone: string, after = new Date()): Date | undefined {
  if (zone === "" || zone.length > maxTimeZoneBytes || zone.toLowerCase() === "local") return undefined;
  const cron = parseCron(expression);
  if (!cron) return undefined;
  let start: Wall;
  try {
    start = zoneWall(after, zone);
  } catch {
    return undefined;
  }
  let year = start.year;
  let month = start.month;
  let day = start.day;
  const startHour = start.hour === 24 ? 24 : start.hour;
  for (let index = 0; index < cadenceHorizon; index += 1) {
    if (dayMatches(cron, year, month, day, zone)) {
      for (const hour of cron.hours) {
        if (index === 0 && hour < startHour) continue;
        for (const minute of cron.minutes) {
          if (index === 0 && hour === startHour && minute <= start.minute) continue;
          const candidate = zonedDate(year, month, day, hour, minute, zone);
          if (candidate && candidate.getTime() > after.getTime()) return candidate;
        }
      }
    }
    const next = new Date(Date.UTC(year, month - 1, day + 1));
    year = next.getUTCFullYear();
    month = next.getUTCMonth() + 1;
    day = next.getUTCDate();
  }
  return undefined;
}

function formatInZone(date: Date, timeZone: string) {
  return new Intl.DateTimeFormat(undefined, {
    timeZone,
    weekday: "short",
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(date);
}

/** nextRunHint is the next fire in the routine's zone, and in the viewer's zone when they differ. */
export function nextRunHint(
  cadence: string,
  zone: string,
  now = new Date(),
): { there: string; here?: string } | undefined {
  const next = nextCadenceFire(cadence, zone, now);
  if (!next) return undefined;
  let there = "";
  try {
    there = formatInZone(next, zone);
  } catch {
    return undefined;
  }
  if (!there) return undefined;
  const local = browserTimeZone();
  if (!local || local === zone) return { there };
  try {
    const here = formatInZone(next, local);
    return here ? { there, here } : { there };
  } catch {
    return { there };
  }
}
