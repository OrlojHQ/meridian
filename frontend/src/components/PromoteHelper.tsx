import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api, normalizeAPIError } from "../api/client";
import type { Capsule } from "../api/generated/types.gen";

// Promotion is permanent and grants no coordination authority. Detaching
// closes the lead's link for good.
export function PromoteHelper({ capsule }: { capsule: Capsule }) {
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  const promote = useMutation({
    mutationFn: (detach: boolean) => api.promoteCapsule(capsule, detach),
    onSettled: async () => {
      setConfirm(false);
      await Promise.all(
        ["capsule", "capsules", "board", "thread-links", "thread"].map((key) =>
          queryClient.invalidateQueries({ queryKey: [key] }),
        ),
      );
    },
  });
  const promoted = capsule.role === "main";
  if (promoted && !capsule.linkId) return null;
  const title = promoted ? "Detach from lead" : "Promote to main";
  return (
    <div className="promote-helper">
      {confirm ? (
        <div className="actions" role="alertdialog" aria-label={title}>
          <span>
            {promoted
              ? "The lead can no longer message this Capsule or hear from it."
              : "It joins your sidebar and is never auto-archived or stopped by its lead. The lead can still message it unless you detach."}
          </span>
          {!promoted && (
            <button type="button" disabled={promote.isPending} onClick={() => promote.mutate(false)}>
              Promote
            </button>
          )}
          <button
            type="button"
            className={promoted ? "danger" : "secondary"}
            disabled={promote.isPending}
            onClick={() => promote.mutate(true)}
          >
            {promoted ? "Detach" : "Promote and detach"}
          </button>
          <button type="button" className="secondary" onClick={() => setConfirm(false)}>
            Cancel
          </button>
        </div>
      ) : (
        <button type="button" className="secondary" onClick={() => setConfirm(true)}>
          {title}…
        </button>
      )}
      {promote.isError && (
        <p role="alert" className="inline-error">
          {normalizeAPIError(promote.error).message}
        </p>
      )}
    </div>
  );
}
