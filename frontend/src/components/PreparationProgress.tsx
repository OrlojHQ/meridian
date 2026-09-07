import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, normalizeAPIError } from "../api/client";
import type { Capsule } from "../api/generated/types.gen";

const labels = {
  checkout: "Checking out your repository…",
  preparing: "Preparing project dependencies…",
  restoring: "Restoring your prepared environment…",
  reclone: "Refreshing the checkout before preparing again…",
  saving: "Saving preparation for future Capsules…",
  ready: "Project environment ready.",
};

export function PreparationProgress({ capsule }: { capsule: Capsule }) {
  const client = useQueryClient();
  const retry = useMutation({
    mutationFn: () => api.lifecycle("retry", capsule.id, capsule.resourceVersion),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["capsule", capsule.id] });
      await client.invalidateQueries({ queryKey: ["project-thread-intent"] });
    },
  });
  const preparation = capsule.preparation;
  const failed = capsule.state === "Failed";
  return (
    <div className={failed ? "error-state" : "notice"} role={failed ? "alert" : "status"}>
      <p>{failed ? capsule.failure || "Capsule preparation failed" : preparation ? labels[preparation.stage] : "Starting your environment…"}</p>
      {preparation?.reused && <p>Reused your prepared project environment.</p>}
      {preparation && (
        <details>
          <summary>Preparation details</summary>
          <p>Stage: {preparation.stage}</p>
          {preparation.detail && <p>{preparation.detail}</p>}
          {preparation.sourceRevision && <p>Source: <code>{preparation.sourceRevision.slice(0, 12)}</code></p>}
        </details>
      )}
      {failed && (
        <p>
          {preparation && preparation.stage !== "ready" && (
            <button type="button" disabled={retry.isPending} onClick={() => retry.mutate()}>
              {retry.isPending ? "Retrying…" : "Retry preparation"}
            </button>
          )}{" "}
          <Link to={`/ui/capsules/${encodeURIComponent(capsule.id)}`}>Inspect Capsule</Link>
        </p>
      )}
      {retry.isError && <p role="alert">{normalizeAPIError(retry.error).message}</p>}
    </div>
  );
}
