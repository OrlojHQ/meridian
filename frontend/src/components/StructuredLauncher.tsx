import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type {
  CoordinationPolicy,
  Project,
  ProjectThreadIntent,
} from "../api/generated/types.gen";

import { queries } from "../api/queries";
import { harnessName, sessionPath } from "../shell/capsuleActivity";
import { PreparationProgress } from "./PreparationProgress";
import { connectableHarnesses, connectionSupports } from "./ProviderConnections";

export function StructuredLauncher({
  project,
  harness,
  onStarted,
}: {
  project: Project;
  harness: string;
  onStarted?: () => void;
}) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ProjectThreadIntent>();
  const [lead, setLead] = useState(false);
  const [operatorApproval, setOperatorApproval] = useState(false);
  const capabilities = useQuery(queries.capabilities());
  const coordinationSupported = Boolean(
    capabilities.data?.coordination?.supported,
  );
  const [connection, setConnection] = useState<string>();
  const connections = useQuery({queryKey: ["provider-connections"], queryFn: api.providerConnections});
  const grant = useQuery({queryKey: ["project-connection", project.id, harness], queryFn: () => api.projectConnection(project.id, harness), enabled: !!harness});
  const selectedConnection = connection ?? grant.data?.connectionId ?? "";
  const connectable = connectableHarnesses.includes(harness) && !!connections.data?.enabled;
  const availableConnections = connections.data?.items.filter(item => !item.revoked && connectionSupports(harness, item)) ?? [];
  const mutation = useMutation({
    mutationFn: async ({
      projectId,
      harness,
      prompt,
      name,
      coordination,
    }: {
      projectId: string;
      harness: string;
      prompt: string;
      name?: string;
      coordination?: CoordinationPolicy;
    }) => {
      if (selectedConnection !== (grant.data?.connectionId ?? "")) await api.grantConnection(projectId, harness, selectedConnection);
      return api.createProjectThread(projectId, harness, prompt, name, coordination);
    },
    onSuccess: async (value) => {
      setResult(value);
      await queryClient.invalidateQueries({
        queryKey: ["capsules", value.projectId],
      });
      if (onStarted) {
        navigate(sessionPath(value.capsuleId, value.threadId));
        onStarted();
      }
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
      coordination:
        coordinationSupported && lead
          ? {
              maxChildren: Number(data.get("maxChildren") ?? 4),
              deadlineSeconds: Number(data.get("deadlineHours") ?? 8) * 3600,
              ...(operatorApproval ? { spawnApproval: "operator" as const } : {}),
            }
          : undefined,
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
        {connectable && (
          <label htmlFor="spawn-connection">
            Provider connection
            <select id="spawn-connection" value={selectedConnection} onChange={(event) => setConnection(event.target.value)} disabled={mutation.isPending || Boolean(result)}>
              <option value="">None</option>
              {availableConnections.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
          </label>
        )}
        {!result && selectedConnection && selectedConnection !== grant.data?.connectionId && <p>Starting this task authorizes this API connection for {project.name} and future {harnessName(harness)} sessions.</p>}
        {connectable && availableConnections.length === 0 && <Link to="/ui/settings/harnesses">Connect provider</Link>}
        <label htmlFor="spawn-name">
          Capsule name <span className="optional-label">Optional</span>
          <input id="spawn-name" name="name" maxLength={128} />
        </label>
        {coordinationSupported && (
          <fieldset className="lead-options">
            <label className="checkbox-label" htmlFor="spawn-lead">
              <input
                id="spawn-lead"
                type="checkbox"
                checked={lead}
                onChange={(event) => setLead(event.currentTarget.checked)}
              />
              Lead agent with subagents
            </label>
            <p className="muted-copy">
              The agent can start subagent Threads, each in its own Capsule,
              and collect their results. Subagents run {harnessName(harness)}{" "}
              and ask you, not the lead, for permissions.
            </p>
            {lead && (
              <div className="lead-limits">
                <label htmlFor="spawn-max-children">
                  Subagents
                  <input
                    id="spawn-max-children"
                    name="maxChildren"
                    type="number"
                    min={1}
                    max={16}
                    defaultValue={4}
                  />
                </label>
                <label htmlFor="spawn-deadline">
                  Deadline (hours)
                  <input
                    id="spawn-deadline"
                    name="deadlineHours"
                    type="number"
                    min={1}
                    max={72}
                    defaultValue={8}
                  />
                </label>
                <label className="checkbox-label" htmlFor="spawn-approval">
                  <input
                    id="spawn-approval"
                    type="checkbox"
                    checked={operatorApproval}
                    onChange={(event) => setOperatorApproval(event.currentTarget.checked)}
                  />
                  Approve each subagent spawn
                </label>
              </div>
            )}
          </fieldset>
        )}
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
