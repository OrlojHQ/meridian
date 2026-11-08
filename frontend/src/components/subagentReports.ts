import type { ThreadLink } from "../api/generated/types.gen";

import { boundedThreadText, safeInline, sanitizeThreadText } from "./threadText";

export const MAX_REPORT_SUMMARY = 4 * 1024;
export const MAX_SUBAGENT_NAME = 48;

const REPORT_HEADER =
  /^--- (?:subagent|helper) "([^"\n]{1,64})" \(link ([^\s)]{1,128})\), report (\d+): ([^\n]+?) ---$/gm;
const OPERATOR_NOTICE =
  /^- The operator messaged (?:subagent|helper) "([^"\n]{1,64})" directly \((\d+) new\)\.$/gm;
const LINK_MARKER = / \(link [^\s)]{1,128}\)/g;
const WAKE_INTRO = /\[Meridian coordination\]/;
const REPORT_MARK = /--- (?:subagent|helper) "/;

const statusLabels: Record<string, string> = {
  turn_ended: "turn ended",
  failed: "failed",
};

const reasonLabels: Record<string, string> = {
  cancelled: "cancelled",
  capsule_failed: "capsule failed",
  session_ended: "session ended",
  revoked: "revoked",
};

export type SubagentReport = {
  name: string;
  status: string;
  summary: string;
  linkId?: string;
};

export type ParsedSubagentReports = {
  reports: SubagentReport[];
  notices: string[];
  count: number;
  wakeText: string;
};

export const subagentPhaseLabels: Record<ThreadLink["phase"], string> = {
  provisioning: "Starting",
  working: "Working",
  idle: "Reported",
  awaiting_operator: "Needs you",
  ended: "Ended",
  failed: "Failed",
};

export function subagentHref(link: Pick<ThreadLink, "childCapsuleId" | "childThreadId">) {
  return `/ui/capsules/${encodeURIComponent(link.childCapsuleId)}?session=${encodeURIComponent(link.childThreadId)}`;
}

export function subagentsNeedingYou(links: readonly ThreadLink[]) {
  return links.filter((link) => !link.archivedAt && link.phase === "awaiting_operator");
}

export function boundedReportText(value = "") {
  const safe = sanitizeThreadText(value);
  return safe.length > MAX_REPORT_SUMMARY
    ? `${safe.slice(0, MAX_REPORT_SUMMARY)}\n… content truncated …`
    : safe;
}

// Mock (and some real harnesses) echo the wake prompt as assistant text.
// Drop that duplicate once report cards already cover the same payload.
export function isCoordinationWakeEcho(text = "") {
  const body = sanitizeThreadText(text).replace(/^mock:/i, "").trim();
  return WAKE_INTRO.test(body) && REPORT_MARK.test(body);
}

export function hideLinkIds(text: string, linkIds: readonly string[] = []) {
  let next = sanitizeThreadText(text).replace(LINK_MARKER, "");
  for (const id of linkIds) {
    if (id.length < 8) continue;
    next = next.split(id).join("");
  }
  return next.replace(/[ \t]{2,}/g, " ").replace(/ +\n/g, "\n");
}

function reportName(value: string) {
  const name = safeInline(value).slice(0, MAX_SUBAGENT_NAME).trim();
  return name || "subagent";
}

function reportStatus(value: string) {
  const match = value.trim().match(/^([a-z_]+)(?: \(([^)]+)\))?$/i);
  if (!match) return safeInline(value.replaceAll("_", " ")) || "reported";
  const status = statusLabels[match[1]] ?? safeInline(match[1].replaceAll("_", " "));
  if (!match[2]) return status;
  const reason = reasonLabels[match[2]] ?? safeInline(match[2].replaceAll("_", " "));
  return `${status}: ${reason}`;
}

function noticeText(name: string, count: string) {
  const n = Number.parseInt(count, 10);
  const times = Number.isFinite(n) && n > 0 ? n : 1;
  return times === 1
    ? `The operator messaged ${reportName(name)} directly (1 new).`
    : `The operator messaged ${reportName(name)} directly (${times} new).`;
}

export function parseSubagentReports(
  content = "",
  linkIds: readonly string[] = [],
): ParsedSubagentReports {
  const reports: SubagentReport[] = [];
  const headers = [...content.matchAll(REPORT_HEADER)];
  for (const [index, match] of headers.entries()) {
    const start = (match.index ?? 0) + match[0].length;
    const end = headers[index + 1]?.index ?? content.length;
    let body = content.slice(start, end);
    const operatorAt = body.search(/^Operator activity \(no message content is shared\):/m);
    if (operatorAt >= 0) body = body.slice(0, operatorAt);
    const summarizeAt = body.search(/^Summarize these results for the user and decide the next step\./m);
    if (summarizeAt >= 0) body = body.slice(0, summarizeAt);
    reports.push({
      name: reportName(match[1]),
      status: reportStatus(match[4]),
      summary: boundedReportText(body.trim()),
      linkId: match[2],
    });
  }
  const notices = [...content.matchAll(OPERATOR_NOTICE)].map((match) =>
    noticeText(match[1], match[2]),
  );
  return {
    reports,
    notices,
    count: reports.length || linkIds.length || 1,
    wakeText: boundedThreadText(hideLinkIds(content, [
      ...linkIds,
      ...reports.map((report) => report.linkId ?? ""),
    ])),
  };
}

export function resolveSubagentReport(
  report: SubagentReport,
  links: readonly ThreadLink[],
): SubagentReport & { href?: string } {
  const link =
    (report.linkId ? links.find((item) => item.id === report.linkId) : undefined) ??
    links.find((item) => item.name === report.name);
  return {
    ...report,
    name: reportName(link?.name ?? report.name),
    href: link ? subagentHref(link) : undefined,
  };
}
