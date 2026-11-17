import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import type { Capsule } from "../api/generated/types.gen";
import { checkBanner } from "../shell/checks";

export function CapsuleChecks({ capsule, tree }: { capsule: Capsule; tree?: string }) {
  const checks = capsule.checks;
  const client = useQueryClient();
  const runs = useQuery({
    queryKey: ["check-runs", capsule.id],
    queryFn: ({ signal }) => api.checkRuns(capsule.id, signal),
    enabled: Boolean(checks?.configured),
    refetchInterval: checks?.phase === "queued" || checks?.phase === "running" || checks?.phase === "reworking" ? 2000 : false,
  });
  const run = useMutation({
    mutationFn: () => api.createCheckRun(capsule.id),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["check-runs", capsule.id] });
      await client.invalidateQueries({ queryKey: ["capsule", capsule.id] });
    },
  });
  const auto = useMutation({
    mutationFn: (enabled: boolean) => api.setCheckAutoRework(capsule.id, { enabled }),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["capsule", capsule.id] });
    },
  });
  if (!checks?.configured) return null;
  const latest = runs.data?.items[0];
  const banner = checkBanner(checks, tree);
  return (
    <section className="capsule-checks" aria-label="Checks">
      <div className="section-heading">
        <h2>Checks</h2>
        {banner && <p className={`check-banner check-banner-${checks.phase || "idle"}`}>{banner}</p>}
      </div>
      <div className="check-actions">
        <button type="button" onClick={() => run.mutate()} disabled={run.isPending || checks.phase === "running" || checks.phase === "queued"}>
          {run.isPending ? "Starting…" : "Run checks"}
        </button>
        {checks.autoRework ? (
          <button type="button" onClick={() => auto.mutate(false)} disabled={auto.isPending}>Stop auto-fix</button>
        ) : (
          <button type="button" onClick={() => auto.mutate(true)} disabled={auto.isPending}>Turn auto-fix back on</button>
        )}
      </div>
      {checks.stopped && <p>Auto-fix is off for this Capsule. Checks still run, and you see the result.</p>}
      {latest && (
        <ol className="check-results">
          {latest.results.map((result) => {
            const failed = result.timedOut || result.exitCode !== 0;
            return (
              <li key={result.name} className={failed ? "check-result failed" : "check-result passed"}>
                <strong>{failed ? "Failed" : "Passed"}</strong> {result.name}{" "}
                <code>{result.argv.join(" ")}</code>
                {result.timedOut ? " · timed out" : result.exitCode !== undefined ? ` · exit ${result.exitCode}` : ""}
                {" · "}
                {Math.max(1, Math.round(result.durationMs / 1000))}s
                {latest.attempt && latest.maxAttempts ? ` · attempt ${latest.attempt} of ${latest.maxAttempts}` : ""}
                {failed && result.output && (
                  <details>
                    <summary>Output</summary>
                    <pre>{result.output}</pre>
                  </details>
                )}
              </li>
            );
          })}
        </ol>
      )}
      {(run.isError || auto.isError || runs.isError) && (
        <p role="alert">{normalizeAPIError(run.error ?? auto.error ?? runs.error).message}</p>
      )}
    </section>
  );
}
