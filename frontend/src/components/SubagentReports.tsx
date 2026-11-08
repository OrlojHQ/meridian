import { Link } from "react-router-dom";

import type { ThreadLink } from "../api/generated/types.gen";
import type { ChatItem } from "./threadChat";
import {
  resolveSubagentReport,
  subagentHref,
  subagentPhaseLabels,
  subagentsNeedingYou,
} from "./subagentReportModel";

export function SubagentReportGroup({
  item,
  links,
}: {
  item: Extract<ChatItem, { kind: "subagent_reports" }>;
  links: readonly ThreadLink[];
}) {
  const count = item.count || item.reports.length;
  const noun = count === 1 ? "subagent report" : "subagent reports";
  return (
    <>
      <span className="chat-origin">From subagents</span>
      <div className="subagent-report-group">
        <p className="subagent-report-delivered">
          Meridian delivered {count} {noun}
        </p>
        {item.reports.length > 0 && (
          <ul className="subagent-report-cards">
            {item.reports.map((report, index) => {
              const resolved = resolveSubagentReport(report, links);
              const label = `Report from ${resolved.name} · ${resolved.status}`;
              return (
                <li key={`${resolved.linkId ?? resolved.name}-${index}`}>
                  <article className="subagent-report-card" aria-label={label}>
                    <header>
                      <span className="subagent-report-heading">{label}</span>
                      {resolved.href && (
                        <Link to={resolved.href} className="subagent-report-open">
                          Open Thread
                        </Link>
                      )}
                    </header>
                    {resolved.summary && (
                      <p className="subagent-report-summary">{resolved.summary}</p>
                    )}
                  </article>
                </li>
              );
            })}
          </ul>
        )}
        {item.notices.length > 0 && (
          <ul className="subagent-report-notices">
            {item.notices.map((notice) => (
              <li key={notice}>{notice}</li>
            ))}
          </ul>
        )}
        {item.wakeText && (
          <details className="subagent-wake">
            <summary>Wake prompt</summary>
            <pre>{item.wakeText}</pre>
          </details>
        )}
      </div>
    </>
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
