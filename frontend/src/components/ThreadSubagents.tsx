import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { CoordinationGrant, CoordinationRequest, Thread, ThreadLink } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { harnessName } from "../shell/capsuleActivity";
import { PromoteSubagent } from "./PromoteSubagent";
import { SpawnDecision } from "./SpawnApprovals";
import { subagentDiffstat, subagentHref, subagentPhaseLabels, subagentReportStatusLabel } from "./subagentReportModel";
import { safeInline } from "./threadText";
import { StateBadge } from "./ui/StateBadge";

// subagentModel names the model (and effort) the subagent's live session last
// reported. The daemon derives it in memory; it is harness text, so it is
// folded to one bounded line before display.
export function subagentModel(link: Pick<ThreadLink, "model" | "effort">) {
  const model = safeInline(link.model ?? "").trim().slice(0, 128);
  if (!model) return "";
  const effort = safeInline(link.effort ?? "").trim().slice(0, 128);
  return effort ? `${model} · ${effort}` : model;
}

function subagentRow(link: ThreadLink, request?: CoordinationRequest) {
  return (
    <li key={link.id}>
      <Link to={subagentHref(link)} className="subagent-name">{link.name}</Link>
      <span className="muted-copy">{harnessName(link.harness)}</span>
      {subagentModel(link) && (
        <span className="muted-copy subagent-model" title="Model the subagent last reported">
          {subagentModel(link)}
        </span>
      )}
      <span className={`badge subagent-phase phase-${link.phase}`}>
        {link.state === "pending_approval" ? "Waiting for approval" : subagentPhaseLabels[link.phase]}
      </span>
      {link.promotedAt && (
        <span className="badge">{link.closeReason === "detached" ? "Promoted, detached" : "Promoted"}</span>
      )}
      <span className="muted-copy">
        {link.reportSeq === 1 ? "1 report" : `${link.reportSeq} reports`}
      </span>
      {link.lastReportStatus && (
        <span className="muted-copy">{subagentReportStatusLabel(link.lastReportStatus)}</span>
      )}
      {subagentDiffstat(link) && <span className="muted-copy">{subagentDiffstat(link)}</span>}
      {request && <SpawnDecision request={request} />}
    </li>
  );
}

export function untilDeadline(deadlineAt: string, now = Date.now()) {
  const minutes = Math.ceil((Date.parse(deadlineAt) - now) / 60_000);
  if (!(minutes > 0)) return "deadline passed";
  const days = Math.floor(minutes / 1_440);
  const hours = Math.floor((minutes % 1_440) / 60);
  const rest = minutes % 60;
  if (days > 0) return `ends in ${days}d ${hours}h`;
  return hours > 0 ? `ends in ${hours}h ${rest}m` : `ends in ${rest}m`;
}

// Leads have depth 1, so every descendant is a direct subagent and the lead
// can start at most the smaller of the two caps.
export function grantLimits(
  grant: Pick<
    CoordinationGrant,
    | "descendantsCreated"
    | "maxChildren"
    | "maxDescendants"
    | "maxLiveCapsules"
    | "deadlineAt"
    | "spawnApproval"
  >,
  active: boolean,
  now = Date.now(),
) {
  const total = Math.min(grant.maxChildren, grant.maxDescendants);
  const parts = [
    `${grant.descendantsCreated} of ${total} started`,
    `up to ${grant.maxLiveCapsules} at a time`,
  ];
  if (active) parts.push(untilDeadline(grant.deadlineAt, now));
  if (grant.spawnApproval === "operator") parts.push("approve each spawn");
  return parts.join(" · ");
}

// Subagent Threads name their lead above the transcript. A lead's subagents
// live behind the composer's Agents button (SubagentsButton) so they do not
// crowd the conversation. Neither view shows report text: that lives only in
// each subagent's encrypted transcript.
export function ThreadSubagents({ thread }: { thread: Thread }) {
  if (!thread.coordinationGrantId || !thread.parentThreadId) return null;
  return <SubagentOf thread={thread} />;
}

function AgentsIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.5">
      <circle cx="6" cy="5.5" r="2.25" />
      <path d="M1.75 13.25c.4-2.3 2.1-3.75 4.25-3.75s3.85 1.45 4.25 3.75" strokeLinecap="round" />
      <path d="M10.5 3.6a2.1 2.1 0 1 1 .6 4.1M12 9.7c1.3.4 2.1 1.6 2.3 3.2" strokeLinecap="round" />
    </svg>
  );
}

// The lead's Agents button: a count with an attention dot when a subagent or a
// queued spawn waits on the operator. It opens the subagent list above the
// composer; Escape or a click elsewhere closes it.
export function SubagentsButton({ thread }: { thread: Thread }) {
  const grantId = thread.coordinationGrantId ?? "";
  const lead = Boolean(thread.coordinationGrantId && !thread.parentThreadId);
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const root = useRef<HTMLDivElement>(null);
  const links = useQuery({ ...queries.threadLinks(thread.id), enabled: lead });
  const pending = useQuery({ ...queries.coordinationRequests(), enabled: lead });
  useEffect(() => {
    if (!open) return;
    const onPointer = (event: PointerEvent) => {
      if (root.current && !root.current.contains(event.target as Node)) setOpen(false);
    };
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };
    document.addEventListener("pointerdown", onPointer);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onPointer);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  if (!lead) return null;
  const current = (links.data?.items ?? []).filter((link) => !link.archivedAt);
  const waiting =
    current.filter((link) => link.phase === "awaiting_operator").length +
    (pending.data?.items ?? []).filter((item) => item.grantId === grantId).length;
  const label = `${current.length} ${current.length === 1 ? "agent" : "agents"}`;
  return (
    <div className="composer-agents" ref={root}>
      <button
        type="button"
        className={`composer-chip${waiting > 0 ? " needs-you" : ""}`}
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        aria-label={waiting > 0 ? `${label}, ${waiting} need you` : label}
        title="Subagents"
        onClick={() => setOpen((value) => !value)}
      >
        <AgentsIcon />
        <span>Agents</span>
        {current.length > 0 && <span className="composer-chip-count">{current.length}</span>}
        {waiting > 0 && <span className="composer-chip-dot" aria-hidden="true" />}
      </button>
      {open && (
        <div className="composer-popover" id={panelId}>
          <SubagentsPanel thread={thread} grantId={grantId} />
        </div>
      )}
    </div>
  );
}

function SubagentOf({ thread }: { thread: Thread }) {
  const lead = useQuery(queries.thread(thread.parentThreadId ?? ""));
  const capsule = useQuery(queries.capsule(thread.capsuleId));
  const leadCapsule = useQuery({
    ...queries.capsule(lead.data?.capsuleId ?? ""),
    enabled: Boolean(lead.data?.capsuleId),
  });
  const leadHref = lead.data
    ? `/ui/capsules/${encodeURIComponent(lead.data.capsuleId)}?session=${encodeURIComponent(lead.data.id)}`
    : undefined;
  const leadName = leadCapsule.data ? safeInline(leadCapsule.data.name) || "Lead" : "Lead";
  const leadLink = leadHref ? (
    <Link to={leadHref} aria-label="Open the lead">
      {leadName}
    </Link>
  ) : null;
  const promoted = capsule.data?.role === "main";
  return (
    <div className="notice subagent-of" role="status">
      {!promoted && (
        <nav className="thread-breadcrumb" aria-label="Lead Thread">
          {leadLink ?? <span>Lead</span>}
          <span aria-hidden="true"> / </span>
          <span>This subagent</span>
        </nav>
      )}
      <p>
        {promoted
          ? capsule.data?.linkId
            ? "Promoted from a subagent; its lead can still message it."
            : "Promoted from a subagent and detached from its lead."
          : "Permission and input requests come to you, not the lead."}{" "}
        {promoted ? leadLink : null}
      </p>
      {capsule.data && <PromoteSubagent capsule={capsule.data} />}
    </div>
  );
}

export function SubagentsPanel({ thread, grantId }: { thread: Thread; grantId: string }) {
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  const grant = useQuery(queries.coordinationGrant(grantId));
  const links = useQuery(queries.threadLinks(thread.id));
  const pending = useQuery(queries.coordinationRequests());
  const pendingByLink = new Map(
    (pending.data?.items ?? []).filter((item) => item.grantId === grantId).map((item) => [item.linkId, item]),
  );
  const approval = useMutation({
    mutationFn: (spawnApproval: "auto" | "operator") => {
      if (!grant.data) throw new Error("Grant is not loaded");
      return api.updateCoordinationGrant(grant.data, { spawnApproval });
    },
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: ["coordination-grant", grantId] });
    },
  });
  const revoke = useMutation({
    mutationFn: () => {
      if (!grant.data) throw new Error("Grant is not loaded");
      return api.revokeCoordinationGrant(grant.data);
    },
    onSettled: async () => {
      setConfirm(false);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["coordination-grant", grantId] }),
        queryClient.invalidateQueries({ queryKey: ["thread-links", thread.id] }),
      ]);
    },
  });
  const items = links.data?.items ?? [];
  const current = items.filter((link) => !link.archivedAt);
  const archived = items.filter((link) => link.archivedAt);
  const active = grant.data?.state === "active";
  return (
    <section className="thread-subagents" aria-labelledby="thread-subagents-title">
      <header>
        <h2 id="thread-subagents-title">Subagents</h2>
        {grant.data && (
          <span
            className="muted-copy"
            title={active ? `Ends ${new Date(grant.data.deadlineAt).toLocaleString()}` : undefined}
          >
            {grantLimits(grant.data, active)}
          </span>
        )}
        {grant.data && <StateBadge state={grant.data.state} />}
        {active && grant.data && (
          <label className="checkbox-label" htmlFor={`grant-spawn-approval-${grantId}`}>
            <input
              id={`grant-spawn-approval-${grantId}`}
              type="checkbox"
              checked={grant.data.spawnApproval === "operator"}
              disabled={approval.isPending}
              onChange={(event) => approval.mutate(event.currentTarget.checked ? "operator" : "auto")}
            />
            Approve each spawn
          </label>
        )}
        {active && !confirm && (
          <button type="button" className="danger" onClick={() => setConfirm(true)}>
            Stop subagents
          </button>
        )}
      </header>
      {confirm && (
        <div className="actions" role="alertdialog" aria-label="Stop subagents">
          <span>The lead can no longer start subagents, and running subagents are cancelled and paused.</span>
          <button type="button" className="danger" disabled={revoke.isPending} onClick={() => revoke.mutate()}>
            Confirm stop
          </button>
          <button type="button" onClick={() => setConfirm(false)}>Keep running</button>
        </div>
      )}
      {revoke.isError && (
        <p role="alert" className="inline-error">{normalizeAPIError(revoke.error).message}</p>
      )}
      {approval.isError && (
        <p role="alert" className="inline-error">{normalizeAPIError(approval.error).message}</p>
      )}
      {items.length === 0 ? (
        <p className="muted-copy">{links.isPending ? "Loading subagents…" : "No subagents yet."}</p>
      ) : (
        <ul className="subagent-list">
          {current.map((link) => subagentRow(link, pendingByLink.get(link.id)))}
          {archived.length > 0 && (
            <li className="subagent-archived">
              <details>
                <summary>Archived ({archived.length})</summary>
                <ul className="subagent-list">{archived.map((link) => subagentRow(link))}</ul>
              </details>
            </li>
          )}
        </ul>
      )}
    </section>
  );
}
