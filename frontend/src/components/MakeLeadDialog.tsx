import { useId, useState } from "react";
import { useMutation } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import type { Thread } from "../api/generated/types.gen";
import { harnessName } from "../shell/capsuleActivity";
import { safeInline } from "./threadChat";
import { LEAD_POLICY } from "./NewSessionDialog";
import { Button } from "./ui/Button";
import { Dialog } from "./ui/Dialog";

// Makes an existing plain Thread a lead (ADR 0059). It offers the lead
// choices the new-Thread dialog offers, and says that the agent restarts its
// session to pick up the role, since a harness learns its role only when a
// session starts.
export function MakeLeadDialog({
  thread,
  onClose,
  onDone,
}: {
  thread: Thread;
  onClose: () => void;
  onDone: () => Promise<unknown> | void;
}) {
  const titleId = useId();
  const [approveSpawns, setApproveSpawns] = useState(false);
  const [restarted, setRestarted] = useState<boolean>();
  const mutation = useMutation({
    mutationFn: () =>
      api.makeThreadLead(thread.id, thread.resourceVersion, {
        ...LEAD_POLICY,
        ...(approveSpawns ? { spawnApproval: "operator" as const } : {}),
      }),
    onSuccess: async (result) => {
      setRestarted(result.restarted);
      await onDone();
    },
  });
  const agent = safeInline(harnessName(thread.harness)) || "The agent";
  return (
    <Dialog
      labelledBy={titleId}
      eyebrow="Thread"
      title="Make this a lead"
      closeLabel="Close"
      onClose={onClose}
      busy={mutation.isPending}
      className="make-lead-dialog"
    >
      {restarted === undefined ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            mutation.mutate();
          }}
        >
          <p className="muted-copy">
            {agent} keeps this conversation and can then hand independent or long-running work to
            subagent Threads, each in its own Capsule, and collect their results. Subagents ask you, not
            the lead, for permissions.
          </p>
          <p className="muted-copy">
            The conversation keeps everything it has already read, including repository and tool output,
            and gains the same authority as a new lead.
          </p>
          <fieldset className="lead-options">
            <label className="checkbox-label" htmlFor={`${titleId}-approval`}>
              <input
                id={`${titleId}-approval`}
                type="checkbox"
                checked={approveSpawns}
                disabled={mutation.isPending}
                onChange={(event) => setApproveSpawns(event.currentTarget.checked)}
              />
              Approve each subagent spawn
            </label>
          </fieldset>
          <p className="field-hint">
            The agent restarts its session to pick up the role. A turn in progress finishes first.
          </p>
          {mutation.isError && (
            <p className="inline-error" role="alert">
              {normalizeAPIError(mutation.error).message}
            </p>
          )}
          <div className="dialog-actions">
            <Button variant="secondary" disabled={mutation.isPending} onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={mutation.isPending}>
              {mutation.isPending ? "Making it a lead…" : "Make lead"}
            </Button>
          </div>
        </form>
      ) : (
        <>
          <p role="status">
            {restarted
              ? "This Thread is now a lead. The agent restarted its session to pick up the role."
              : "This Thread is now a lead. The agent restarts its session to pick up the role before your next message."}
          </p>
          <div className="dialog-actions">
            <Button variant="primary" onClick={onClose}>
              Done
            </Button>
          </div>
        </>
      )}
    </Dialog>
  );
}
