import { useId, useState } from "react";
import { Link } from "react-router-dom";

import type { ThreadLink } from "../api/generated/types.gen";
import type { ChatItem } from "./threadChat";
import {
  resolveSubagentReport,
  subagentHref,
  subagentPhaseLabels,
  subagentsNeedingYou,
} from "./subagentReportModel";

// At most this many subagents are named on the line; the rest are counted.
const MAX_NAMED_REPORTS = 3;

// A report that ended its turn or said it was done is a normal finish and
// shows only the subagent's name. Anything else, such as failed or blocked,
// is said on the line.
const NORMAL_FINISH = new Set(["turn ended", "done"]);

function reportStatusSuffix(status: string) {
  return NORMAL_FINISH.has(status) ? "" : status;
}

// SubagentReportGroup is the quiet line where Meridian woke the lead with
// subagent report headlines. The lead's next reply summarizes them, each
// subagent's Thread holds its full output, and the Agents panel shows each
// status, so the line names the subagents and links to their Threads
// instead of repeating report text. The exact wake prompt, including any
// operator-activity notice, is one click away. It is never drawn as an
// operator message (ADR 0029, from_subagents provenance).
export function SubagentReportGroup({
  item,
  links,
}: {
  item: Extract<ChatItem, { kind: "subagent_reports" }>;
  links: readonly ThreadLink[];
}) {
  const [open, setOpen] = useState(false);
  const wakeId = useId();
  const reports = item.reports.map((report) => resolveSubagentReport(report, links));
  const named = reports.slice(0, MAX_NAMED_REPORTS);
  const more = reports.length - named.length;
  const count = item.count || reports.length;
  const described = reports.map((report) => {
    const suffix = reportStatusSuffix(report.status);
    return suffix ? `${report.name} ${suffix}` : report.name;
  });
  const label = described.length > 0
    ? `Subagent reports delivered by Meridian: ${described.join(", ")}`
    : `Meridian delivered ${count} ${count === 1 ? "subagent report" : "subagent reports"}`;
  return (
    <div className="subagent-wake-line" role="group" aria-label={label}>
      <p>
        <span aria-hidden="true">↳ </span>
        {named.length === 0 ? (
          <>{count === 1 ? "A subagent report" : `${count} subagent reports`}</>
        ) : (
          <>
            Reports from{" "}
            {named.map((report, index) => {
              const suffix = reportStatusSuffix(report.status);
              return (
                <span key={`${report.linkId ?? report.name}-${index}`} className="subagent-wake-name">
                  {index > 0 && ", "}
                  {report.href ? <Link to={report.href}>{report.name}</Link> : report.name}
                  {suffix && <span className="subagent-wake-status"> {suffix}</span>}
                </span>
              );
            })}
            {more > 0 && <>{" "}+{more} more</>}
          </>
        )}
        {item.wakeText && (
          <>
            {" · "}
            <button
              type="button"
              className="subagent-wake-toggle"
              aria-expanded={open}
              aria-controls={wakeId}
              onClick={() => setOpen((value) => !value)}
            >
              Wake prompt <span aria-hidden="true">{open ? "▾" : "▸"}</span>
            </button>
          </>
        )}
      </p>
      {item.wakeText && open && (
        <pre id={wakeId} className="subagent-wake-text">{item.wakeText}</pre>
      )}
    </div>
  );
}

export function SubagentNeedsYouTray({ links }: { links: readonly ThreadLink[] }) {
  const needing = subagentsNeedingYou(links);
  if (needing.length === 0) return null;
  const noun = needing.length === 1 ? "subagent needs you" : "subagents need you";
  return (
    <section className="subagent-tray" aria-label="Subagents that need you">
      <div className="subagent-tray-inner">
        <p>
          <strong>
            {needing.length} {noun}
          </strong>
        </p>
        <ul>
          {needing.map((link) => (
            <li key={link.id}>
              <Link to={subagentHref(link)} className="subagent-name">
                {link.name}
              </Link>
              <span className="badge subagent-phase phase-awaiting_operator">
                {subagentPhaseLabels.awaiting_operator}
              </span>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}
