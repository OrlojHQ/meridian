import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router-dom";

import type { BoardItem } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import {
  relativeTime,
  rollupSummary,
  sessionPath,
  type ActivityGroup,
} from "../shell/capsuleActivity";
import { PromoteSubagent } from "./PromoteSubagent";
import { safeInline } from "./threadChat";
import { ThreadError } from "./ThreadError";
import { HarnessMark } from "./ui/HarnessMark";

type BoardStatus = BoardItem["status"];

export const boardGroups: { status: BoardStatus; title: string; activity: ActivityGroup }[] = [
  { status: "needs_you", title: "Needs you", activity: "attention" },
  { status: "working", title: "Working", activity: "working" },
  { status: "pr_open", title: "PR open", activity: "finished" },
  { status: "ready_for_review", title: "Ready for review", activity: "finished" },
  { status: "idle", title: "Idle", activity: "idle" },
  { status: "paused", title: "Paused", activity: "paused" },
  { status: "sealed", title: "Sealed", activity: "sealed" },
];

const needsYouLabels: Record<NonNullable<BoardItem["needsYou"]>, string> = {
  permission: "Waiting for permission",
  input: "Waiting for input",
  failed: "Failed",
};

export function boardRowPath(item: BoardItem) {
  return sessionPath(item.capsule.id, item.thread?.id);
}

function safePullRequestURL(value?: string) {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return url.protocol === "https:" ? url.toString() : undefined;
  } catch {
    return undefined;
  }
}

function itemName(item: BoardItem) {
  return safeInline(item.link?.name ?? item.capsule.name);
}

function subagentRow(subagent: BoardItem) {
  const subagentGroup = boardGroups.find((entry) => entry.status === subagent.status);
  return (
    <li key={subagent.capsule.id}>
      <span
        className={`state-dot activity-dot-${subagentGroup?.activity ?? "idle"}`}
        aria-hidden="true"
      />
      <span className="board-subagent-name">
        <Link to={boardRowPath(subagent)}>{itemName(subagent)}</Link>
        {subagent.capsule.promotedAt && <span className="badge">Promoted</span>}
      </span>
      <small>{subagent.needsYou ? needsYouLabels[subagent.needsYou] : subagentGroup?.title}</small>
      <time dateTime={subagent.since}>{relativeTime(subagent.since)}</time>
    </li>
  );
}

function BoardRow({
  item,
  leadNames,
  subagents,
}: {
  item: BoardItem;
  leadNames: Map<string, { name: string; capsuleId: string }>;
  subagents?: BoardItem[];
}) {
  const [expanded, setExpanded] = useState(false);
  const group = boardGroups.find((entry) => entry.status === item.status);
  const lead = item.link ? leadNames.get(item.link.parentThreadId) : undefined;
  const pullRequest = safePullRequestURL(item.latestDelivery?.pullRequestUrl);
  const harness = item.thread?.harness ?? item.capsule.harness ?? undefined;
  const label = item.needsYou ? needsYouLabels[item.needsYou] : group?.title;
  const archived = subagents?.filter((subagent) => subagent.link?.archivedAt) ?? [];
  return (
    <li className={`inbox-row board-row inbox-row-${group?.activity ?? "idle"}`}>
      <HarnessMark harness={harness} size="md" />
      <div className="inbox-main">
        <p className="inbox-title">
          <Link to={boardRowPath(item)}>{itemName(item)}</Link>
          <time dateTime={item.since}>{relativeTime(item.since)}</time>
        </p>
        <p className="inbox-meta">
          <span className={`state-dot activity-dot-${group?.activity ?? "idle"}`} aria-hidden="true" />
          <span className="inbox-status">{label}</span>
          {item.link && (
            <span className="board-breadcrumb">
              {item.capsule.promotedAt ? "promoted from" : "subagent of"}{" "}
              {lead ? (
                <Link to={sessionPath(lead.capsuleId, item.link.parentThreadId)}>{lead.name}</Link>
              ) : (
                "a lead that is gone"
              )}
            </span>
          )}
          {item.childRollup && <span>{rollupSummary(item.childRollup)}</span>}
          {item.latestDelivery && pullRequest && (
            <a href={pullRequest} target="_blank" rel="noreferrer noopener">
              PR #{item.latestDelivery.pullRequestNumber ?? ""}
            </a>
          )}
          {item.latestDelivery && !pullRequest && (
            <span>Delivery {item.latestDelivery.state.replaceAll("_", " ")}</span>
          )}
        </p>
        {expanded && subagents && (
          <ul className="board-subagents" aria-label={`Subagents of ${itemName(item)}`}>
            {subagents.filter((subagent) => !subagent.link?.archivedAt).map(subagentRow)}
            {archived.length > 0 && (
              <li className="board-archived">
                <details>
                  <summary>Archived ({archived.length})</summary>
                  <ul className="board-subagents">{archived.map(subagentRow)}</ul>
                </details>
              </li>
            )}
          </ul>
        )}
      </div>
      <div className="inbox-actions">
        {item.childRollup && subagents && subagents.length > 0 && (
          <button
            type="button"
            className="secondary"
            aria-expanded={expanded}
            onClick={() => setExpanded((value) => !value)}
          >
            {expanded ? "Hide subagents" : `Subagents (${subagents.length})`}
          </button>
        )}
        {item.surfaced && item.capsule.role === "sub" && <PromoteSubagent capsule={item.capsule} />}
        <Link className="button-link primary-button" to={boardRowPath(item)}>
          {item.status === "needs_you" && item.needsYou !== "failed" ? "Respond" : "Open"}
        </Link>
      </div>
    </li>
  );
}

export function Board() {
  const [showAll, setShowAll] = useState(false);
  const board = useQuery(queries.board(showAll ? "all" : "attention"));
  // Every subagent row, for the per-lead folds; one content-free read shared by
  // all leads.
  const everything = useQuery(queries.board("all"));

  if (board.isPending) {
    return (
      <p className="loading" role="status">
        Loading board…
      </p>
    );
  }
  if (board.isError) return <ThreadError error={board.error} />;

  const items = board.data.items;
  const all = everything.data?.items ?? items;
  const leadNames = new Map(
    all.flatMap((item) =>
      item.thread && item.capsule.role !== "sub"
        ? [[item.thread.id, { name: safeInline(item.capsule.name), capsuleId: item.capsule.id }] as const]
        : [],
    ),
  );
  const subagentsOf = (item: BoardItem) =>
    item.thread
      ? all.filter((candidate) => candidate.link?.parentThreadId === item.thread?.id)
      : [];
  const needsYou = items.filter((item) => item.status === "needs_you").length;
  return (
    <section className="fleet-view board-view">
      <header className="workspace-view-header">
        <div>
          <p className="eyebrow">Board</p>
          <h1>{needsYou > 0 ? `${needsYou} need${needsYou === 1 ? "s" : ""} you` : "Everything"}</h1>
          <p className="view-description">
            Every Capsule by status. Subagents fold under their lead unless they need you.
          </p>
        </div>
        <div className="workspace-header-meta">
          <label className="check-label">
            <input
              type="checkbox"
              checked={showAll}
              onChange={(event) => setShowAll(event.target.checked)}
            />
            Show all subagents
          </label>
          <span className="count-label">{items.length} rows</span>
        </div>
      </header>
      {board.data.truncated && (
        <p className="warning" role="status">
          Only the first 500 Capsules are shown.
        </p>
      )}
      {items.length === 0 ? (
        <div className="empty-state">
          <h2>Nothing on the board</h2>
          <p>Start an agent in a new Capsule from the sidebar.</p>
        </div>
      ) : (
        boardGroups.map(({ status, title }) => {
          const rows = items.filter((item) => item.status === status);
          if (rows.length === 0) return null;
          return (
            <section className="inbox-group" key={status} aria-labelledby={`board-${status}`}>
              <h2 id={`board-${status}`}>
                {title} <small>{rows.length}</small>
              </h2>
              <ul>
                {rows.map((item) => (
                  <BoardRow
                    key={item.capsule.id}
                    item={item}
                    leadNames={leadNames}
                    subagents={item.childRollup ? subagentsOf(item) : undefined}
                  />
                ))}
              </ul>
            </section>
          );
        })
      )}
    </section>
  );
}
