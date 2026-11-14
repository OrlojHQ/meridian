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
import { HarnessMark } from "./ui/HarnessMark";
import { SettingsIcon, StopIcon } from "./ui/Icons";

// subagentModel names the model (and effort) the subagent's live session last
// reported. The daemon derives it in memory; it is harness text, so it is
// folded to one bounded line before display.
export function subagentModel(link: Pick<ThreadLink, "model" | "effort">) {
  const model = safeInline(link.model ?? "").trim().slice(0, 128);
  if (!model) return "";
  const effort = safeInline(link.effort ?? "").trim().slice(0, 128);
  return effort ? `${model} · ${effort}` : model;
}

// removeNotice says what removing a subagent deletes. Its workspace goes with
// the Capsule, so changed files are named; the Thread history is kept.
export function removeNotice(link: Pick<ThreadLink, "name" | "filesChanged">) {
  const files = link.filesChanged ?? 0;
  const work = files > 0
    ? ` Its workspace goes with it, including ${files === 1 ? "1 changed file" : `${files} changed files`}.`
    : "";
  return `Stop ${link.name} and delete its Capsule?${work} The lead can still start new subagents.`;
}

function SubagentRow({ link, request }: { link: ThreadLink; request?: CoordinationRequest }) {
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  const remove = useMutation({
    mutationFn: () => api.removeSubagent(link),
    onSuccess: () => setConfirm(false),
    onSettled: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["thread-links", link.parentThreadId] }),
        queryClient.invalidateQueries({ queryKey: ["board"] }),
      ]);
    },
  });
  const needsYou = link.state === "pending_approval" || link.phase === "awaiting_operator";
  const removed = Boolean(link.capsuleRemoved);
  const removable = !removed && !link.promotedAt;
  const running = !removed && (link.phase === "working" || link.phase === "provisioning");
  const details = [
    harnessName(link.harness),
    subagentModel(link),
    link.reportSeq === 1 ? "1 report" : `${link.reportSeq} reports`,
    link.lastReportStatus ? subagentReportStatusLabel(link.lastReportStatus) : "",
    subagentDiffstat(link),
  ].filter(Boolean);
  return (
    <li className={`subagent-row${removed ? " removed" : ""}`}>
      <span className="subagent-activity" aria-hidden="true">
        {running && <span className="spinner" />}
      </span>
      <HarnessMark harness={link.harness} size="md" />
      <div className="subagent-main">
        <div className="subagent-title">
          <Link to={subagentHref(link)} className="subagent-name">{link.name}</Link>
          {link.promotedAt && (
            <span className="subagent-tag">{link.closeReason === "detached" ? "Promoted, detached" : "Promoted"}</span>
          )}
          <span className={`subagent-phase phase-${removed ? "removed" : link.phase}${needsYou && !removed ? " needs-you" : ""}`}>
            {removed
              ? "Removed"
              : link.state === "pending_approval"
                ? "Waiting for approval"
                : subagentPhaseLabels[link.phase]}
          </span>
          {removable && (
            <button
              type="button"
              className="subagent-remove"
              aria-label={`Stop and remove ${link.name}`}
              title="Stop and remove"
              aria-expanded={confirm}
              disabled={remove.isPending}
              onClick={() => setConfirm((value) => !value)}
            >
              <StopIcon />
            </button>
          )}
        </div>
        <p className="subagent-details">
          {details.map((detail, index) => (
            <span key={index} title={detail === subagentModel(link) ? "Model the subagent last reported" : undefined}>
              {detail}
            </span>
          ))}
        </p>
        {confirm && removable && (
          <div className="subagent-remove-confirm" role="alertdialog" aria-label={`Remove ${link.name}`}>
            <p>{removeNotice(link)}</p>
            <div className="subagent-remove-actions">
              <button
                type="button"
                className="subagent-remove-yes"
                disabled={remove.isPending}
                onClick={() => remove.mutate()}
              >
                {remove.isPending ? "Removing…" : "Remove"}
              </button>
              <button type="button" className="subagent-remove-no" onClick={() => setConfirm(false)}>
                Keep
              </button>
            </div>
          </div>
        )}
        {remove.isError && (
          <p role="alert" className="inline-error">{normalizeAPIError(remove.error).message}</p>
        )}
        {request && <SpawnDecision request={request} />}
      </div>
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
  { approval = true, live = true }: { approval?: boolean; live?: boolean } = {},
) {
  const total = Math.min(grant.maxChildren, grant.maxDescendants);
  const parts = [`${grant.descendantsCreated} of ${total} started`];
  if (live) parts.push(`up to ${grant.maxLiveCapsules} at a time`);
  if (active) parts.push(untilDeadline(grant.deadlineAt, now));
  if (approval && grant.spawnApproval === "operator") parts.push("approve each spawn");
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
  const current = (links.data?.items ?? []).filter((link) => !link.archivedAt && !link.capsuleRemoved);
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

// The daemon allows 1 to 16 subagent Capsules at a time.
const AT_ONCE_CHOICES = Array.from({ length: 16 }, (_, index) => index + 1);

// How long a finished subagent idles before it is archived and, when nothing
// in its workspace is unsaved, its Capsule deleted. 0 turns both off.
const CLEANUP_CHOICES: { seconds: number; label: string }[] = [
  { seconds: 15 * 60, label: "15 minutes" },
  { seconds: 60 * 60, label: "1 hour" },
  { seconds: 4 * 60 * 60, label: "4 hours" },
  { seconds: 24 * 60 * 60, label: "1 day" },
  { seconds: 0, label: "Never" },
];

export function cleanupChoices(current: number) {
  if (CLEANUP_CHOICES.some((choice) => choice.seconds === current)) return CLEANUP_CHOICES;
  const hours = current / 3600;
  const label = Number.isInteger(hours) ? `${hours} hours` : `${Math.round(current / 60)} minutes`;
  return [...CLEANUP_CHOICES, { seconds: current, label }].sort(
    (a, b) => (a.seconds || Infinity) - (b.seconds || Infinity),
  );
}

const grantStateLabels: Record<CoordinationGrant["state"], string> = {
  active: "Active",
  revoked: "Stopped",
  expired: "Ended",
};

export function SubagentsPanel({ thread, grantId }: { thread: Thread; grantId: string }) {
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  const [settings, setSettings] = useState(false);
  const settingsId = useId();
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
  const cleanup = useMutation({
    mutationFn: (archiveAfterSeconds: number) => {
      if (!grant.data) throw new Error("Grant is not loaded");
      return api.updateCoordinationGrant(grant.data, { archiveAfterSeconds });
    },
    onSettled: async () => {
      await queryClient.invalidateQueries({ queryKey: ["coordination-grant", grantId] });
    },
  });
  const atOnce = useMutation({
    mutationFn: (maxLiveCapsules: number) => {
      if (!grant.data) throw new Error("Grant is not loaded");
      return api.updateCoordinationGrant(grant.data, { maxLiveCapsules });
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
  const current = items.filter((link) => !link.archivedAt && !link.capsuleRemoved);
  const archived = items.filter((link) => link.archivedAt && !link.capsuleRemoved);
  const removed = items.filter((link) => link.capsuleRemoved);
  const active = grant.data?.state === "active";
  const approvalId = `grant-spawn-approval-${grantId}`;
  const atOnceId = `grant-at-once-${grantId}`;
  const cleanupId = `grant-cleanup-${grantId}`;
  return (
    <section className="thread-subagents" aria-labelledby="thread-subagents-title">
      <header className="subagents-header">
        <div className="subagents-heading">
          <h2 id="thread-subagents-title">Subagents</h2>
          {grant.data && (
            <span className={`subagents-state state-${grant.data.state}`}>{grantStateLabels[grant.data.state]}</span>
          )}
          {active && grant.data?.spawnApproval === "operator" && (
            <span className="subagents-approving">Approving spawns</span>
          )}
          <span className="subagents-tools">
            {grant.data && (
              <button
                type="button"
                className="subagents-settings-toggle"
                aria-label="Subagent settings"
                title="Limits and spawn approval"
                aria-expanded={settings}
                aria-controls={settings ? settingsId : undefined}
                onClick={() => setSettings((value) => !value)}
              >
                <SettingsIcon />
              </button>
            )}
            {active && !confirm && (
              <button type="button" className="subagents-stop" onClick={() => setConfirm(true)}>
                Stop subagents
              </button>
            )}
          </span>
        </div>
      </header>
      {settings && grant.data && (
        <div className="subagents-settings" id={settingsId} role="group" aria-label="Subagent settings">
          {active && (
            <label className="subagents-setting" htmlFor={atOnceId}>
              <span className="subagents-switch-text">
                <span>Run at once</span>
                <small>Subagents the lead can run at a time.</small>
              </span>
              <select
                id={atOnceId}
                value={grant.data.maxLiveCapsules}
                disabled={atOnce.isPending}
                onChange={(event) => atOnce.mutate(Number(event.currentTarget.value))}
              >
                {AT_ONCE_CHOICES.map((count) => (
                  <option key={count} value={count}>{count}</option>
                ))}
              </select>
            </label>
          )}
          {active && (
            <label className="subagents-setting" htmlFor={cleanupId}>
              <span className="subagents-switch-text">
                <span>Clean up finished subagents</span>
                <small>Deletes a done subagent's Capsule after this long idle, unless it has unsaved changes.</small>
              </span>
              <select
                id={cleanupId}
                className="subagents-cleanup"
                value={grant.data.archiveAfterSeconds}
                disabled={cleanup.isPending}
                onChange={(event) => cleanup.mutate(Number(event.currentTarget.value))}
              >
                {cleanupChoices(grant.data.archiveAfterSeconds).map((choice) => (
                  <option key={choice.seconds} value={choice.seconds}>{choice.label}</option>
                ))}
              </select>
            </label>
          )}
          {active && (
            <label className="subagents-switch" htmlFor={approvalId}>
              <input
                id={approvalId}
                type="checkbox"
                role="switch"
                checked={grant.data.spawnApproval === "operator"}
                disabled={approval.isPending}
                onChange={(event) => approval.mutate(event.currentTarget.checked ? "operator" : "auto")}
              />
              <span className="subagents-switch-text">
                <span>Approve each spawn</span>
                <small>When off, the lead starts subagents on its own.</small>
              </span>
            </label>
          )}
          <p
            className="subagents-limits"
            title={active ? `Ends ${new Date(grant.data.deadlineAt).toLocaleString()}` : undefined}
          >
            {grantLimits(grant.data, active, Date.now(), { approval: false, live: !active })}
          </p>
        </div>
      )}
      {confirm && (
        <div className="subagents-confirm" role="alertdialog" aria-label="Stop subagents">
          <p>The lead can no longer start subagents, and running subagents are cancelled and paused.</p>
          <div className="subagents-confirm-actions">
            <button type="button" className="danger" disabled={revoke.isPending} onClick={() => revoke.mutate()}>
              Confirm stop
            </button>
            <button type="button" className="secondary" onClick={() => setConfirm(false)}>Keep running</button>
          </div>
        </div>
      )}
      {revoke.isError && (
        <p role="alert" className="inline-error">{normalizeAPIError(revoke.error).message}</p>
      )}
      {approval.isError && (
        <p role="alert" className="inline-error">{normalizeAPIError(approval.error).message}</p>
      )}
      {cleanup.isError && (
        <p role="alert" className="inline-error">{normalizeAPIError(cleanup.error).message}</p>
      )}
      {atOnce.isError && (
        <p role="alert" className="inline-error">{normalizeAPIError(atOnce.error).message}</p>
      )}
      {items.length === 0 ? (
        <div className="subagents-empty">
          {links.isPending ? (
            <p>Loading subagents…</p>
          ) : (
            <>
              <p className="subagents-empty-title">No subagents yet</p>
              <p>
                {active
                  ? "Ask the lead to split the work. Each subagent it starts runs in its own Capsule and shows up here."
                  : "This lead did not start any subagents."}
              </p>
            </>
          )}
        </div>
      ) : (
        <ul className="subagent-list">
          {current.map((link) => (
            <SubagentRow key={link.id} link={link} request={pendingByLink.get(link.id)} />
          ))}
          {archived.length > 0 && (
            <li className="subagent-archived">
              <details>
                <summary>Archived ({archived.length})</summary>
                <ul className="subagent-list">
                  {archived.map((link) => <SubagentRow key={link.id} link={link} />)}
                </ul>
              </details>
            </li>
          )}
          {removed.length > 0 && (
            <li className="subagent-archived">
              <details>
                <summary>Removed ({removed.length})</summary>
                <ul className="subagent-list">
                  {removed.map((link) => <SubagentRow key={link.id} link={link} />)}
                </ul>
              </details>
            </li>
          )}
        </ul>
      )}
    </section>
  );
}
