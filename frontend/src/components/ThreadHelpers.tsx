import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { Thread, ThreadLink } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { harnessName } from "../shell/capsuleActivity";
import { StateBadge } from "./ui/StateBadge";

const phaseLabels: Record<ThreadLink["phase"], string> = {
  provisioning: "Starting",
  working: "Working",
  idle: "Reported",
  awaiting_operator: "Needs you",
  ended: "Ended",
  failed: "Failed",
};

function helperHref(link: Pick<ThreadLink, "childCapsuleId" | "childThreadId">) {
  return `/ui/capsules/${encodeURIComponent(link.childCapsuleId)}?session=${encodeURIComponent(link.childThreadId)}`;
}

// Lead Threads list their helpers and can stop the whole tree; helper
// Threads name their lead. Neither view shows report text: that lives only in
// each helper's encrypted transcript.
export function ThreadHelpers({ thread }: { thread: Thread }) {
  if (!thread.coordinationGrantId) return null;
  if (thread.parentThreadId) return <HelperOf thread={thread} />;
  return <LeadHelpers thread={thread} grantId={thread.coordinationGrantId} />;
}

function HelperOf({ thread }: { thread: Thread }) {
  const lead = useQuery(queries.thread(thread.parentThreadId ?? ""));
  return (
    <p className="notice helper-of" role="status">
      Helper Thread started by a lead.{" "}
      {lead.data ? (
        <Link to={`/ui/capsules/${encodeURIComponent(lead.data.capsuleId)}?session=${encodeURIComponent(lead.data.id)}`}>
          Open the lead
        </Link>
      ) : null}
      . Its permission and input requests come to you, not the lead.
    </p>
  );
}

function LeadHelpers({ thread, grantId }: { thread: Thread; grantId: string }) {
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  const grant = useQuery(queries.coordinationGrant(grantId));
  const links = useQuery(queries.threadLinks(thread.id));
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
  const active = grant.data?.state === "active";
  return (
    <section className="thread-helpers" aria-labelledby="thread-helpers-title">
      <header>
        <h2 id="thread-helpers-title">Helpers</h2>
        {grant.data && (
          <span className="muted-copy">
            {grant.data.descendantsCreated} of {grant.data.maxDescendants} started
            {active ? ` · until ${new Date(grant.data.deadlineAt).toLocaleString()}` : ""}
          </span>
        )}
        {grant.data && <StateBadge state={grant.data.state} />}
        {active && !confirm && (
          <button type="button" className="danger" onClick={() => setConfirm(true)}>
            Stop helpers
          </button>
        )}
      </header>
      {confirm && (
        <div className="actions" role="alertdialog" aria-label="Stop helpers">
          <span>The lead can no longer start helpers, and running helpers are cancelled and paused.</span>
          <button type="button" className="danger" disabled={revoke.isPending} onClick={() => revoke.mutate()}>
            Confirm stop
          </button>
          <button type="button" onClick={() => setConfirm(false)}>Keep running</button>
        </div>
      )}
      {revoke.isError && (
        <p role="alert" className="inline-error">{normalizeAPIError(revoke.error).message}</p>
      )}
      {items.length === 0 ? (
        <p className="muted-copy">{links.isPending ? "Loading helpers…" : "No helpers yet."}</p>
      ) : (
        <ul className="helper-list">
          {items.map((link) => (
            <li key={link.id}>
              <Link to={helperHref(link)} className="helper-name">{link.name}</Link>
              <span className="muted-copy">{harnessName(link.harness)}</span>
              <span className={`badge helper-phase phase-${link.phase}`}>{phaseLabels[link.phase]}</span>
              <span className="muted-copy">
                {link.reportSeq === 1 ? "1 report" : `${link.reportSeq} reports`}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
