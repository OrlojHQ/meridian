import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Link } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type {
  Project,
  ProjectThreadIntent,
} from "../api/generated/types.gen";

import { queries } from "../api/queries";
import { harnessName } from "../shell/capsuleActivity";
import { PreparationProgress } from "./PreparationProgress";

export function StructuredLauncher({
  project,
  harness,
}: {
  project: Project;
  harness: string;
}) {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ProjectThreadIntent>();
  const mutation = useMutation({
    mutationFn: ({
      projectId,
      harness,
      prompt,
      name,
    }: {
      projectId: string;
      harness: string;
      prompt: string;
      name?: string;
    }) => api.createProjectThread(projectId, harness, prompt, name),
    onSuccess: async (value) => {
      setResult(value);
      await queryClient.invalidateQueries({
        queryKey: ["capsules", value.projectId],
      });
    },
  });

  const intentQuery = useQuery({queryKey: ["project-thread-intent", result?.id], queryFn: ({signal}) => api.projectThreadIntent(result!.id,signal), enabled: Boolean(result?.id), refetchInterval: 3000});
  const intent = intentQuery.data ?? result;
  const capsuleQuery = useQuery(queries.capsule(intent?.capsuleId ?? ""));
  const runsQuery = useQuery(queries.runs(intent?.capsuleId ?? ""));
  const run = runsQuery.data?.items.find(item => item.id === intent?.runId);
  const sessionReady = intent?.state === "ready" && run && run.state !== "Queued" && run.state !== "Starting";

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    mutation.mutate({
      projectId: project.id,
      harness,
      prompt: String(data.get("prompt") ?? ""),
      name: String(data.get("name") ?? "") || undefined,
    });
  }

  return (
    <section className="launcher-pane">
      <p className="muted-copy">
        {harnessName(harness)} starts in a fresh Capsule with this task. The
        conversation is kept encrypted.
      </p>
      <form className="launcher-form" onSubmit={submit}>
        <label htmlFor="spawn-prompt">
          Task
          <textarea
            id="spawn-prompt"
            name="prompt"
            required
            rows={6}
            maxLength={131072}
            placeholder="Describe the work you want the agent to do…"
          />
        </label>
        <label htmlFor="spawn-name">
          Capsule name <span className="optional-label">Optional</span>
          <input id="spawn-name" name="name" maxLength={128} />
        </label>
        <button
          type="submit"
          disabled={mutation.isPending || Boolean(result) || !harness}
        >
          {mutation.isPending ? "Starting…" : "Start task"}
        </button>
      </form>
      {mutation.isError ? (
        <p role="alert" className="inline-error">
          {normalizeAPIError(mutation.error).message}
        </p>
      ) : null}
      {capsuleQuery.data && <PreparationProgress capsule={capsuleQuery.data} />}
      {intent && (
        <p role="status" className="launch-result">
          {sessionReady ? <Link to={`/ui/threads/${encodeURIComponent(intent.threadId)}`}>Open Thread</Link> : intent.state === "failed" ? intent.failureMessage || "Session preparation failed" : "Preparing your session…"}
        </p>
      )}
    </section>
  );
}
