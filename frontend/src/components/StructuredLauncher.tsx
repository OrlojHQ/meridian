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
import type { PinnedLead } from "../shell/pins";
import { capsuleNameFromMessage } from "../shell/threadTitle";
import { PreparationProgress } from "./PreparationProgress";
import { RepositoryAccessNotice } from "./RepositoryAccess";
import { isRepositoryAccessFailure } from "./startup";
import { connectableHarnesses, connectionModelNote, connectionSupports, preferredConnectionId } from "./ProviderConnections";

function accessIntent(intent: ProjectThreadIntent) {
  return intent.state === "failed" && isRepositoryAccessFailure({
    failure: intent.failureMessage,
    failureCode: intent.failureCode,
  });
}

// Sending to a pinned Thread is an ordinary operator message to it, so a
// lead's grant counts it as the operator being back (ADR 0059). A paused
// Thread is resumed first, as its Resume session button does.
async function sendToPinned(target: PinnedLead, prompt: string) {
  let thread = await api.thread(target.thread.id);
  if (thread.state === "paused") {
    thread = (await api.threadSession("resume", thread.id, thread.resourceVersion)).thread;
  }
  return (await api.sendThreadMessage(thread.id, thread.resourceVersion, prompt)).thread;
}

function pinnedLabel({ capsule, thread }: PinnedLead) {
  const lead = Boolean(thread.coordinationGrantId) && !thread.parentThreadId;
  return lead ? `${capsule.name} (lead)` : capsule.name;
}

export function StructuredLauncher({
  project,
  harness,
  pinned = [],
  onStarted,
}: {
  project: Project;
  harness: string;
  /** Pinned Threads in this Project the task can go to instead. */
  pinned?: PinnedLead[];
  onStarted?: () => void;
}) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ProjectThreadIntent>();
  // The default is always a new Thread; a pinned Thread is an explicit choice.
  const [targetId, setTargetId] = useState("");
  const target = pinned.find((item) => item.thread.id === targetId);
  const [sentTo, setSentTo] = useState<{ capsuleId: string; threadId: string }>();
  // New Threads lead by default: the agent decides when to delegate.
  const [lead, setLead] = useState(true);
  const [operatorApproval, setOperatorApproval] = useState(false);
  const capabilities = useQuery(queries.capabilities());
  const coordinationSupported = Boolean(
    capabilities.data?.coordination?.supported,
  );
  const [connection, setConnection] = useState<string>();
  const connections = useQuery({queryKey: ["provider-connections"], queryFn: api.providerConnections});
  const grant = useQuery({queryKey: ["project-connection", project.id, harness], queryFn: () => api.projectConnection(project.id, harness), enabled: !!harness});
  const supportsConnections = connectableHarnesses.includes(harness);
  const connectable = supportsConnections && !!connections.data?.enabled;
  const availableConnections = connections.data?.items.filter(item => !item.revoked && connectionSupports(harness, item)) ?? [];
  const selectedConnection = preferredConnectionId(connection, grant.isSuccess || grant.isError, grant.data?.connectionId, availableConnections);
  const usableConnections = availableConnections.filter(item => !item.needsSignIn);
  const soleConnection = usableConnections.length === 1 ? usableConnections[0] : undefined;
  const modelNote = connectionModelNote(availableConnections.find(item => item.id === selectedConnection));
  const missingConnection = connectable && usableConnections.length === 0;
  const connectionBlocked = connectable && !usableConnections.some(item => item.id === selectedConnection);
  const waitingForConnections = supportsConnections && (connections.isPending || grant.isPending);
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

  const send = useMutation({
    mutationFn: ({ target, prompt }: { target: PinnedLead; prompt: string }) =>
      sendToPinned(target, prompt),
    onSuccess: async (thread) => {
      await queryClient.invalidateQueries({ queryKey: ["threads", thread.capsuleId] });
      setSentTo({ capsuleId: thread.capsuleId, threadId: thread.id });
      if (onStarted) {
        navigate(sessionPath(thread.capsuleId, thread.id));
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
    if (target) {
      send.mutate({ target, prompt: String(data.get("prompt") ?? "") });
      return;
    }
    if (connectionBlocked) return;
    // With one level of subagents the tree total is the child total, so it
    // follows the field instead of silently capping it at its default.
    const maxChildren = Number(data.get("maxChildren") ?? 4);
    const prompt = String(data.get("prompt") ?? "");
    mutation.mutate({
      projectId: project.id,
      harness,
      prompt,
      // Without a name, the Capsule is named from the task, as the composer
      // names a Thread from its first message.
      name: String(data.get("name") ?? "").trim() || capsuleNameFromMessage(prompt) || undefined,
      coordination:
        coordinationSupported && lead
          ? {
              maxChildren,
              maxDescendants: maxChildren,
              unattendedLimitSeconds: Number(data.get("unattendedHours") ?? 24) * 3600,
              ...(operatorApproval ? { spawnApproval: "operator" as const } : {}),
            }
          : undefined,
    });
  }

  return (
    <section className="launcher-pane">
      <p className="muted-copy">
        {target
          ? `This task goes to ${target.capsule.name} as a message in its Thread.`
          : `${harnessName(harness)} starts in a fresh Capsule with this task.`}{" "}
        The conversation is kept encrypted.
      </p>
      <form className="launcher-form" onSubmit={submit}>
        {pinned.length > 0 && (
          <label htmlFor="spawn-target">
            Send to
            <select
              id="spawn-target"
              value={targetId}
              onChange={(event) => setTargetId(event.target.value)}
              disabled={mutation.isPending || send.isPending || Boolean(result) || Boolean(sentTo)}
            >
              <option value="">New Thread</option>
              {pinned.map((item) => (
                <option key={item.thread.id} value={item.thread.id}>
                  {pinnedLabel(item)}
                </option>
              ))}
            </select>
          </label>
        )}
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
        {!target && (
          <>
            {connectable && (
              <label htmlFor="spawn-connection">
                Provider connection
                <select id="spawn-connection" value={selectedConnection} onChange={(event) => setConnection(event.target.value)} disabled={mutation.isPending || Boolean(result)}>
                  <option value="">None</option>
                  {availableConnections.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
                </select>
              </label>
            )}
            {modelNote && <p>{modelNote}</p>}
            {soleConnection && selectedConnection === soleConnection.id && <p>Threads will use this; they cannot show a browser login.</p>}
            {!result && selectedConnection && selectedConnection !== grant.data?.connectionId && <p>Starting this task authorizes this API connection for {project.name} and future {harnessName(harness)} sessions.</p>}
            {missingConnection && <p>Add a provider connection first.</p>}
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
                  The agent hands independent or long-running work to subagent
                  Threads on its own, each in its own Capsule, and collects their
                  results. Subagents run {harnessName(harness)}{" "}
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
                    <label htmlFor="spawn-unattended">
                      Pause without you (hours)
                      <input
                        id="spawn-unattended"
                        name="unattendedHours"
                        type="number"
                        min={1}
                        max={72}
                        defaultValue={24}
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
          </>
        )}
        {target ? (
          <button type="submit" disabled={send.isPending || Boolean(sentTo)}>
            {send.isPending ? "Sending…" : `Send to ${target.capsule.name}`}
          </button>
        ) : (
          <button
            type="submit"
            disabled={mutation.isPending || Boolean(result) || !harness || waitingForConnections || connectionBlocked}
          >
            {mutation.isPending ? "Starting…" : "Start task"}
          </button>
        )}
      </form>
      {mutation.isError ? (
        <p role="alert" className="inline-error">
          {normalizeAPIError(mutation.error).message}
        </p>
      ) : null}
      {send.isError ? (
        <p role="alert" className="inline-error">
          {normalizeAPIError(send.error).message}
        </p>
      ) : null}
      {sentTo && !onStarted && (
        <p role="status" className="launch-result">
          <Link to={sessionPath(sentTo.capsuleId, sentTo.threadId)}>Open Thread</Link>
        </p>
      )}
      {capsuleQuery.data && <PreparationProgress capsule={capsuleQuery.data} />}
      {intent && accessIntent(intent) && !isRepositoryAccessFailure(capsuleQuery.data ?? {}) && (
        <RepositoryAccessNotice message={intent.failureMessage} />
      )}
      {intent && !accessIntent(intent) && (
        <p role="status" className="launch-result">
          {sessionReady ? <Link to={`/ui/threads/${encodeURIComponent(intent.threadId)}`}>Open Thread</Link> : intent.state === "failed" ? intent.failureMessage || "Session preparation failed" : "Preparing your session…"}
        </p>
      )}
    </section>
  );
}
