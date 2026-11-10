import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useOutletContext } from "react-router-dom";

import type { BoardItem, CoordinationRequest, Project } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import {
  relativeTime,
  rollupSummary,
  sessionPath,
  type ActivityGroup,
} from "../shell/capsuleActivity";
import { BlockerPeek } from "./BlockerPeek";
import { DeliveryDecision } from "./DeliveryDecision";
import { PromoteSubagent } from "./PromoteSubagent";
import { SpawnDecision } from "./SpawnApprovals";
import { failureText, safeInline } from "./threadChat";
import { ThreadError } from "./ThreadError";
import { HarnessMark } from "./ui/HarnessMark";
import { PlusIcon } from "./ui/Icons";
import { Button } from "./ui/Button";
import { Card } from "./ui/Card";
import { ListSkeleton } from "./ui/loading";

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
  spawn_approval: "Waiting for spawn approval",
  proposed_delivery: "Waiting for Delivery approval",
};

export function boardFailure(item: BoardItem) {
  if (item.needsYou !== "failed") return undefined;
  const failure = item.capsule.state === "Failed" ? item.capsule.failure : item.currentRun?.failure;
  return failureText(failure) || undefined;
}

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

function plural(count: number, one: string, many: string) {
  return `${count} ${count === 1 ? one : many}`;
}

function StartWork({
  projects,
  openLauncher,
  openNewProject,
}: {
  projects: Project[];
  openLauncher?: (project: Project) => void;
  openNewProject?: () => void;
}) {
  if (!openLauncher && !openNewProject) {
    return (
      <p>
        Use <strong>+</strong> next to a project in the sidebar to start an
        agent in a new Capsule.
      </p>
    );
  }
  return (
    <div className="inbox-start">
      {openLauncher &&
        projects.map((project) => (
          <button key={project.id} type="button" onClick={() => openLauncher(project)}>
            <PlusIcon />
            Start work in {project.name}
          </button>
        ))}
      {openNewProject && (
        <button type="button" className="secondary" onClick={openNewProject}>
          New project
        </button>
      )}
    </div>
  );
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
  projectName,
  git,
  subagents,
  spawnRequest,
}: {
  item: BoardItem;
  leadNames: Map<string, { name: string; capsuleId: string }>;
  projectName?: string;
  git: boolean;
  subagents?: BoardItem[];
  spawnRequest?: CoordinationRequest;
}) {
  const [expanded, setExpanded] = useState(false);
  const group = boardGroups.find((entry) => entry.status === item.status);
  const lead = item.link ? leadNames.get(item.link.parentThreadId) : undefined;
  const pullRequest = safePullRequestURL(item.latestDelivery?.pullRequestUrl);
  const harness = item.thread?.harness ?? item.capsule.harness ?? undefined;
  const label = item.needsYou ? needsYouLabels[item.needsYou] : group?.title;
  const failure = boardFailure(item);
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
          {projectName && <span>{projectName}</span>}
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
        {failure && <p className="inbox-detail">{failure}</p>}
        {item.thread && (item.needsYou === "permission" || item.needsYou === "input") && (
          <BlockerPeek thread={item.thread} kind={item.needsYou} />
        )}
        {item.needsYou === "proposed_delivery" && item.latestDelivery && (
          <DeliveryDecision capsuleId={item.capsule.id} deliveryId={item.latestDelivery.id} />
        )}
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
        {item.surfaced && item.capsule.role === "sub" && item.needsYou !== "spawn_approval" && (
          <PromoteSubagent capsule={item.capsule} />
        )}
        {spawnRequest && <SpawnDecision request={spawnRequest} />}
        {git && item.status === "ready_for_review" && (
          <Link
            className="button-link"
            to={`/ui/capsules/${encodeURIComponent(item.capsule.id)}/diff`}
          >
            Open diff
          </Link>
        )}
        <Link className="button-link primary-button" to={boardRowPath(item)}>
          {item.status === "needs_you" &&
          item.needsYou !== "failed" &&
          item.needsYou !== "spawn_approval" &&
          item.needsYou !== "proposed_delivery"
            ? "Respond"
            : "Open"}
        </Link>
      </div>
    </li>
  );
}

type InboxOutlet = {
  openNewProject: () => void;
  openLauncher: (project: Project) => void;
};

export function Board() {
  const outlet = useOutletContext<InboxOutlet | undefined>();
  const [showAll, setShowAll] = useState(false);
  const board = useQuery(queries.board(showAll ? "all" : "attention"));
  const pending = useQuery(queries.coordinationRequests());
  const pendingByLink = new Map((pending.data?.items ?? []).map((item) => [item.linkId, item]));
  // Every subagent row, for the per-lead folds; one content-free read shared by
  // all leads.
  const everything = useQuery(queries.board("all"));
  const projects = useQuery(queries.projects());
  const capabilities = useQuery(queries.capabilities());

  if (board.isPending) {
    return <ListSkeleton label="Loading inbox…" />;
  }
  if (board.isError) return <ThreadError error={board.error} />;

  const items = board.data.items;
  const all = everything.data?.items ?? items;
  const projectItems = projects.data?.items ?? [];
  const projectNames = new Map(projectItems.map((project) => [project.id, project.name]));
  const git = capabilities.data?.git === true;
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
  const working = items.filter((item) => item.status === "working").length;

  if (items.length === 0 && projects.isPending) {
    return <ListSkeleton label="Loading inbox…" />;
  }

  if (projects.isSuccess && projectItems.length === 0 && items.length === 0) {
    return (
      <Card className="empty-state">
        <h1>No projects yet</h1>
        <p>
          A Project points Meridian at a repository. Every agent session then
          runs in its own disposable Capsule with a fresh clone.
        </p>
        {outlet?.openNewProject && (
          <Button type="button" variant="primary" onClick={outlet.openNewProject}>
            New project
          </Button>
        )}
      </Card>
    );
  }

  if (items.length === 0) {
    return (
      <section className="fleet-view board-view activity-inbox">
        <header className="workspace-view-header">
          <div>
            <p className="eyebrow">Inbox</p>
            <h1>Nothing running yet</h1>
          </div>
        </header>
        <div className="empty-state ui-card">
          <p>Start an agent in a new Capsule.</p>
          <StartWork
            projects={projectItems}
            openLauncher={outlet?.openLauncher}
            openNewProject={outlet?.openNewProject}
          />
        </div>
      </section>
    );
  }

  const headline =
    needsYou > 0
      ? `${needsYou} need${needsYou === 1 ? "s" : ""} attention`
      : "All caught up";

  return (
    <section className="fleet-view board-view activity-inbox">
      <header className="workspace-view-header">
        <div>
          <p className="eyebrow">Inbox</p>
          <h1>{headline}</h1>
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
          <span className="count-label">{plural(items.length, "Capsule", "Capsules")}</span>
        </div>
      </header>
      {board.data.truncated && (
        <p className="warning" role="status">
          Only the first 500 Capsules are shown.
        </p>
      )}
      {needsYou === 0 && (
        <div className="inbox-clear">
          <p>
            Nothing needs you right now.
            {working > 0 &&
              ` ${plural(working, "agent is", "agents are")} still working.`}
          </p>
          <StartWork
            projects={projectItems}
            openLauncher={outlet?.openLauncher}
            openNewProject={outlet?.openNewProject}
          />
        </div>
      )}
      {boardGroups.map(({ status, title }) => {
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
                  projectName={projectNames.get(item.capsule.projectId)}
                  git={git}
                  subagents={item.childRollup ? subagentsOf(item) : undefined}
                  spawnRequest={item.link ? pendingByLink.get(item.link.id) : undefined}
                />
              ))}
            </ul>
          </section>
        );
      })}
    </section>
  );
}
