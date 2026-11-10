import { useMutation, useQueryClient } from "@tanstack/react-query";

import { api, normalizeAPIError } from "../api/client";
import type { Capsule, CapsuleStartupPhase } from "../api/generated/types.gen";
import { ActivityDot, ProgressBar } from "./ui/loading";
import { canRetryStartup, phaseLabels, preparationLabels, pullSummary } from "./startup";

const failureTitles: Record<NonNullable<Capsule["failureCode"]>, string> = {
  registry_unreachable: "Registry unreachable",
  auth_required: "Authentication required",
  image_not_found: "Image not found",
};

function stepState(
  phase: CapsuleStartupPhase,
  current: CapsuleStartupPhase,
  failed: boolean,
  passed: boolean,
): "done" | "active" | "upcoming" | "failed" {
  const order = ["pulling_image", "creating_capsule", "starting_harness", "connecting"];
  const phaseIndex = order.indexOf(phase);
  const currentIndex = order.indexOf(current);
  if (failed && phase === current) return "failed";
  if (phaseIndex < currentIndex || (passed && phaseIndex <= currentIndex)) return "done";
  if (phase === current) return "active";
  return "upcoming";
}

export function CapsuleStartup({ capsule }: { capsule: Capsule }) {
  const client = useQueryClient();
  const startup = capsule.startup;
  const failed = capsule.state === "Failed";
  const retryable = canRetryStartup(capsule);
  const retry = useMutation({
    mutationFn: () => api.lifecycle("retry", capsule.id, capsule.resourceVersion),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["capsule", capsule.id] });
      await client.invalidateQueries({ queryKey: ["project-thread-intent"] });
    },
  });
  const phases = startup?.phases ?? [];
  const current = startup?.phase;
  const passed = capsule.state === "Preparing";
  const preparationLabel =
    passed && capsule.preparation && capsule.preparation.stage !== "ready"
      ? preparationLabels[capsule.preparation.stage]
      : undefined;
  const activeLabel = preparationLabel ?? (current ? phaseLabels[current] : "Starting");
  const pull = current === "pulling_image" ? startup?.pull : undefined;
  const summary = pull
    ? pullSummary(pull.currentBytes, pull.totalBytes)
    : failed
      ? capsule.failure || "The Capsule image could not be pulled."
      : activeLabel;
  const title = failed
    ? (capsule.failureCode ? failureTitles[capsule.failureCode] : "Startup failed")
    : activeLabel;

  if (!startup && !failed) {
    return (
      <section className="startup-tracker" aria-busy="true" aria-label="Capsule startup">
        <p className="sr-only" aria-live="polite">Starting</p>
        <ol className="startup-steps">
          <li className="startup-step is-active">
            <ActivityDot />
            <span>Starting</span>
          </li>
        </ol>
        <ProgressBar label="Starting" />
      </section>
    );
  }
  if (!startup) return null;

  return (
    <section
      className={`startup-tracker${failed ? " is-failed" : ""}`}
      aria-busy={failed ? undefined : true}
      aria-label="Capsule startup"
    >
      <p className="sr-only" aria-live="polite">
        {failed ? `${title}. ${capsule.failure || summary}` : summary}
      </p>
      <div className="startup-heading">
        <h2>{title}</h2>
        {pull && <span className="startup-bytes">{summary}</span>}
      </div>
      <ol className="startup-steps">
        {phases.map((phase) => {
          const state = current
            ? stepState(phase, current, failed, Boolean(preparationLabel))
            : "upcoming";
          return (
            <li key={phase} className={`startup-step is-${state}`}>
              {state === "done" ? (
                <span className="load-check" aria-hidden="true">✓</span>
              ) : (
                <ActivityDot tone={state === "upcoming" ? "upcoming" : state} />
              )}
              <span>{phaseLabels[phase]}</span>
            </li>
          );
        })}
        {preparationLabel && (
          <li className="startup-step is-active">
            <ActivityDot />
            <span>{preparationLabel}</span>
          </li>
        )}
      </ol>
      {pull && (
        <ProgressBar
          label={summary}
          value={pull.totalBytes ? pull.currentBytes : undefined}
          max={pull.totalBytes}
        />
      )}
      {!pull && !failed && current && <ProgressBar label={activeLabel} />}
      {failed && (
        <p className="startup-reason" role="alert">
          {capsule.failure || "The Capsule image could not be pulled."}
        </p>
      )}
      {retryable && (
        <p className="startup-actions">
          <button type="button" disabled={retry.isPending} onClick={() => retry.mutate()}>
            {retry.isPending ? "Retrying…" : "Retry"}
          </button>
        </p>
      )}
      {retry.isError && <p role="alert">{normalizeAPIError(retry.error).message}</p>}
    </section>
  );
}
