import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Link } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type {
  Project,
  ProjectThreadIntent,
} from "../api/generated/types.gen";

import { queries } from "../api/queries";
import { PreparationProgress } from "./PreparationProgress";

export function StructuredLauncher({ project }: { project: Project }) {
  const queryClient = useQueryClient();
  const packs = project.harnessImages ?? [];
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
      harness: String(data.get("harness") ?? ""),
      prompt: String(data.get("prompt") ?? ""),
      name: String(data.get("name") ?? "") || undefined,
    });
  }

  return (
    <section className="launcher-pane">
      <header className="launcher-pane-heading">
        <div>
          <p className="eyebrow">Structured session</p>
          <h2>Start with a task</h2>
        </div>
        <span className="launcher-kind">Thread</span>
      </header>
      <p className="muted-copy">
        Provision a fresh Capsule and retain an encrypted structured transcript.
      </p>
      <form className="launcher-form" onSubmit={submit}>
        <label htmlFor="spawn-harness">
          Structured harness
          <select
            id="spawn-harness"
            name="harness"
            required
            defaultValue={packs[0]?.name}
            disabled={mutation.isPending || Boolean(result) || packs.length === 0}
          >
            {packs.map((pack) => (
              <option key={pack.name} value={pack.name}>
                {pack.name}
              </option>
            ))}
          </select>
        </label>
        {packs.length === 0 && (
          <p className="notice">
            This Project has no applied harness packs. Apply one before starting
            a structured Thread.
          </p>
        )}
        <label htmlFor="spawn-name">
          Capsule name <span className="optional-label">Optional</span>
          <input id="spawn-name" name="name" maxLength={128} />
        </label>
        <label htmlFor="spawn-prompt">
          First task
          <textarea
            id="spawn-prompt"
            name="prompt"
            required
            rows={6}
            maxLength={131072}
            placeholder="Describe the work you want the agent to do…"
          />
        </label>
        <button
          type="submit"
          disabled={mutation.isPending || Boolean(result) || packs.length === 0}
        >
          {mutation.isPending ? "Starting…" : "Start Thread"}
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
