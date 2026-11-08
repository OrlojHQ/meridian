import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api, normalizeAPIError } from "../api/client";
import { queries } from "../api/queries";
import { boundedThreadText, safeInline } from "./threadText";

const MAX_PROPOSAL_TITLE = 512;

export function DeliveryDecision({
  capsuleId,
  deliveryId,
}: {
  capsuleId: string;
  deliveryId: string;
}) {
  const queryClient = useQueryClient();
  const delivery = useQuery(queries.delivery(deliveryId));
  const inspection = useQuery({
    ...queries.deliveryInspection(capsuleId),
    enabled: Boolean(capsuleId) && delivery.data?.state === "proposed",
  });
  const [confirmation, setConfirmation] = useState("");
  const invalidate = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["delivery", deliveryId] }),
      queryClient.invalidateQueries({ queryKey: ["deliveries", capsuleId] }),
      queryClient.invalidateQueries({ queryKey: ["board"] }),
      queryClient.invalidateQueries({ queryKey: ["delivery-inspection", capsuleId] }),
    ]);
  };
  const approve = useMutation({
    mutationFn: () => {
      if (!delivery.data || !inspection.data) {
        return Promise.reject(new Error("Inspection is still loading"));
      }
      return api.approveDelivery(delivery.data, {
        expectedResourceVersion: delivery.data.resourceVersion,
        expectedCapsuleVersion: inspection.data.capsuleResourceVersion,
        expectedHead: inspection.data.head,
        expectedTree: inspection.data.tree,
      });
    },
    onSettled: invalidate,
  });
  const reject = useMutation({
    mutationFn: () => {
      if (!delivery.data) {
        return Promise.reject(new Error("Delivery is still loading"));
      }
      return api.rejectDelivery(delivery.data);
    },
    onSettled: invalidate,
  });
  if (delivery.isPending) {
    return <p role="status">Loading proposed Delivery…</p>;
  }
  if (delivery.isError) {
    return <p role="alert" className="inline-error">{normalizeAPIError(delivery.error).message}</p>;
  }
  if (delivery.data.state !== "proposed") {
    return (
      <p role="status">
        Delivery {delivery.data.id} is {delivery.data.state.replaceAll("_", " ")}.
      </p>
    );
  }
  const title = safeInline(delivery.data.pullRequestTitle ?? "").slice(0, MAX_PROPOSAL_TITLE);
  const body = boundedThreadText(delivery.data.pullRequestBody ?? "");
  const pending = approve.isPending || reject.isPending;
  const error = approve.error ?? reject.error;
  return (
    <div className="delivery-decision">
      <p className="inbox-status">Proposed Delivery — review as untrusted text</p>
      <dl className="facts compact">
        <div>
          <dt>Branch</dt>
          <dd className="mono">{safeInline(delivery.data.remoteBranch)}</dd>
        </div>
        <div>
          <dt>Title</dt>
          <dd>{title || "(empty)"}</dd>
        </div>
      </dl>
      {body ? <pre className="proposal-body">{body}</pre> : null}
      {inspection.isPending ? <p role="status">Inspecting exact Git state…</p> : null}
      {inspection.isError ? (
        <p role="alert" className="inline-error">{normalizeAPIError(inspection.error).message}</p>
      ) : null}
      {inspection.data && (
        <dl className="facts compact">
          <div><dt>HEAD</dt><dd className="mono">{inspection.data.head}</dd></div>
          <div><dt>Tree</dt><dd className="mono">{inspection.data.tree}</dd></div>
          <div><dt>Working tree</dt><dd>{inspection.data.dirty ? "Dirty" : "Clean"}</dd></div>
        </dl>
      )}
      <div className="actions spawn-decision">
        <label>
          Type <span className="mono">approve</span> to bind this exact HEAD and tree
          <input
            aria-label="Approve confirmation"
            autoComplete="off"
            value={confirmation}
            onChange={(event) => setConfirmation(event.target.value)}
          />
        </label>
        <button
          type="button"
          disabled={pending || confirmation !== "approve" || !inspection.data}
          onClick={() => approve.mutate()}
        >
          {approve.isPending ? "Approving…" : "Approve Delivery"}
        </button>
        <button type="button" className="danger" disabled={pending} onClick={() => reject.mutate()}>
          Reject
        </button>
      </div>
      {error && (
        <p role="alert" className="inline-error">{normalizeAPIError(error).message}</p>
      )}
    </div>
  );
}
