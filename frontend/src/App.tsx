import { HarnessSetups } from "./components/HarnessSetups";
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { lazy, Suspense, useEffect, useMemo, useState, type FormEvent } from "react";
import {
  Link,
  Route,
  Routes,
  useParams,
} from "react-router-dom";

import { api, normalizeAPIError } from "./api/client";
import { followRunEvents, type StreamStatus } from "./api/events";
import type {
  Capsule,
  CreateDeliveryRequest,
  Delivery,
  GitResult,
  Moment,
  PreviewTicket,
  Run,
  RunEvent,
  TimelineView,
} from "./api/generated/types.gen";
import { queries } from "./api/queries";
import {
  CapsuleThreads,
  ThreadDetail,
  ThreadFleet,
} from "./components/Threads";
import { CodeViewer } from "./components/SyntaxCode";
import { StateBadge } from "./components/ui/StateBadge";
import { AppShell } from "./shell/AppShell";
const TerminalView = lazy(() =>
  import("./components/Terminal").then((module) => ({
    default: module.TerminalView,
  })),
);

function age(value: string) {
  const seconds = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / 1_000));
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3_600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86_400) return `${Math.floor(seconds / 3_600)}h`;
  return `${Math.floor(seconds / 86_400)}d`;
}

function ErrorState({ error }: { error: unknown }) {
  const normalized = normalizeAPIError(error);
  if (normalized.status === 401) return <AuthenticationState />;
  return (
    <section className="error-state" role="alert">
      <h2>Request failed</h2>
      <p>{normalized.message}</p>
      <button type="button" onClick={() => window.location.reload()}>
        Retry
      </button>
    </section>
  );
}

export function AuthenticationState() {
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const hostname = window.location.hostname;
  const safeOrigin =
    window.location.protocol === "https:" ||
    hostname === "localhost" ||
    hostname === "127.0.0.1" ||
    hostname === "::1" ||
    hostname === "[::1]";
  if (!safeOrigin) {
    return (
      <section className="error-state" role="alert">
        <h2>HTTPS required</h2>
        <p>
          Remote browser authentication is available only over HTTPS. Use a
          loopback port-forward or configure an operator-controlled TLS proxy.
        </p>
      </section>
    );
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setSubmitting(true);
    const form = event.currentTarget;
    const token = new FormData(form).get("token");
    try {
      if (typeof token !== "string" || token === "") {
        throw new Error("Enter the installation API token");
      }
      await api.authenticateBrowser(token);
      form.reset();
      window.location.reload();
    } catch (value) {
      form.reset();
      setError(normalizeAPIError(value).message);
      setSubmitting(false);
    }
  }
  return (
    <section className="error-state" role="alert">
      <h2>Authentication required</h2>
      <p>
        Enter the installation API token. Meridian exchanges it for an HttpOnly
        same-origin session and does not store it in browser storage.
      </p>
      <form onSubmit={submit}>
        <label htmlFor="installation-token">Installation token</label>
        <input
          id="installation-token"
          name="token"
          type="password"
          autoComplete="off"
          required
        />
        <button type="submit" disabled={submitting}>
          {submitting ? "Authenticating…" : "Authenticate"}
        </button>
      </form>
      {error ? <p>{error}</p> : null}
    </section>
  );
}

function CapsuleSummary({
  capsule,
  run,
  moment,
}: {
  capsule: Capsule;
  run?: Run;
  moment?: Moment;
}) {
  const active = run && ["Queued", "Starting", "Running", "Cancelling"].includes(run.state);
  return (
    <article className="card capsule-card">
      <header>
        <div>
          <h2>
            <Link to={`/ui/capsules/${encodeURIComponent(capsule.id)}`}>
              {capsule.name}
            </Link>
          </h2>
          <p className="mono">{capsule.id}</p>
        </div>
        <StateBadge state={capsule.state} />
      </header>
      <dl className="facts compact">
        <div>
          <dt>Desired state</dt>
          <dd>{capsule.desiredState}</dd>
        </div>
        <div>
          <dt>Age</dt>
          <dd>{age(capsule.createdAt)}</dd>
        </div>
        <div>
          <dt>Immutable</dt>
          <dd>{capsule.state === "Sealed" ? "Sealed" : "No"}</dd>
        </div>
        <div>
          <dt>Maintenance</dt>
          <dd>{capsule.maintenance || "None"}</dd>
        </div>
        <div>
          <dt>{active ? "Active Run" : "Latest Run"}</dt>
          <dd>
            {run ? (
              <Link to={`/ui/runs/${encodeURIComponent(run.id)}`}>{run.state}</Link>
            ) : (
              "None"
            )}
          </dd>
        </div>
        <div>
          <dt>Latest Moment</dt>
          <dd>
            {moment ? (
              <Link to={`/ui/moments/${encodeURIComponent(moment.id)}`}>
                {moment.name}
              </Link>
            ) : (
              "None"
            )}
          </dd>
        </div>
        <div>
          <dt>Timeline</dt>
          <dd>
            <Link to={`/ui/timelines/${encodeURIComponent(capsule.timelineId)}`}>
              View lineage
            </Link>
          </dd>
        </div>
        <div>
          <dt>Provider metrics</dt>
          <dd>Unavailable</dd>
        </div>
      </dl>
    </article>
  );
}

export function CapsuleList() {
  const projects = useQuery(queries.projects());
  const projectItems = projects.data?.items ?? [];
  const capsuleResults = useQueries({
    queries: projectItems.map((project) => queries.capsules(project.id)),
  });
  const capsules = capsuleResults
    .flatMap((result) => result.data?.items ?? [])
    .filter((capsule) => capsule.state !== "Deleted");
  const runResults = useQueries({
    queries: capsules.map((capsule) => queries.runs(capsule.id)),
  });
  const momentResults = useQueries({
    queries: capsules.map((capsule) => queries.moments(capsule.id)),
  });

  if (projects.isPending) return <p className="loading" role="status">Loading Capsules…</p>;
  if (projects.isError) return <ErrorState error={projects.error} />;
  if (projectItems.length === 0) {
    return (
      <section className="empty-state">
        <h1>Capsules</h1>
        <p>No projects exist yet. Create one with the scriptable Meridian CLI.</p>
      </section>
    );
  }
  if (capsuleResults.some((result) => result.isPending)) {
    return <p className="loading" role="status">Loading Capsules…</p>;
  }
  const capsuleError = capsuleResults.find((result) => result.isError);
  if (capsuleError) return <ErrorState error={capsuleError.error} />;
  if (capsules.length === 0) {
    return (
      <section className="fleet-view">
        <header className="workspace-view-header">
          <div>
            <p className="eyebrow">Activity</p>
            <h1>Nothing running yet</h1>
          </div>
        </header>
        <div className="empty-state">
          <p>
            Use <strong>New</strong> in the activity sidebar to start a native
            harness or structured Thread.
          </p>
          <span>{projectItems.length} Project(s) ready</span>
        </div>
      </section>
    );
  }
  return (
    <section className="fleet-view">
      <header className="workspace-view-header">
        <div>
          <p className="eyebrow">Activity</p>
          <h1>Your working set</h1>
          <p className="view-description">
            Watch active harnesses, return to paused work, and review recent
            Capsules.
          </p>
        </div>
        <span className="count-label">{capsules.length} Capsules</span>
      </header>
      <div className="fleet-list">
        {capsules.map((capsule, index) => {
          const runs = runResults[index]?.data?.items ?? [];
          const moments = momentResults[index]?.data?.items ?? [];
          return (
            <CapsuleSummary
              key={capsule.id}
              capsule={capsule}
              run={runs[0]}
              moment={moments[0]}
            />
          );
        })}
      </div>
    </section>
  );
}

export function PreviewCard({
  capsuleId = "",
  capsuleState = "Ready",
}: {
  capsuleId?: string;
  capsuleState?: Capsule["state"];
}) {
  const capabilities = useQuery(queries.capabilities());
  const ports = useQuery({
    ...queries.previewPorts(capsuleId),
    enabled: Boolean(capsuleId) && capabilities.data?.preview === true,
  });
  const [ticket, setTicket] = useState<PreviewTicket & { port: number }>();
  const [expired, setExpired] = useState(false);
  const issue = useMutation({
    mutationFn: (port: number) => api.previewTicket(capsuleId, port),
    onSuccess: (value, port) => {
      setTicket({ port, ...value });
      setExpired(Date.parse(value.expiresAt) <= Date.now());
    },
  });
  useEffect(() => {
    if (!ticket) return;
    const delay = Math.max(0, Date.parse(ticket.expiresAt) - Date.now());
    const timer = window.setTimeout(() => setExpired(true), delay);
    return () => window.clearTimeout(timer);
  }, [ticket]);
  if (capabilities.isPending) return <p role="status">Checking preview capability…</p>;
  if (capabilities.isError) return <ErrorState error={capabilities.error} />;
  return (
    <section className="panel">
      <h2>Previews</h2>
      {!capabilities.data.preview ? (
        <p className="notice">
          Unavailable. This provider does not expose bounded port discovery and
          Capsule-scoped proxying.
        </p>
      ) : capsuleState !== "Ready" ? (
        <p className="warning" role="alert">
          Preview links are revoked when a Capsule is sealed or deletion starts.
          This Capsule is {capsuleState}.
        </p>
      ) : ports.isPending ? (
        <p role="status">Discovering Capsule ports…</p>
      ) : ports.isError ? (
        <ErrorState error={ports.error} />
      ) : ports.data.items.length === 0 ? (
        <p className="notice">No loopback-reachable HTTP ports are listening in this Capsule.</p>
      ) : (
        <>
          <ul className="resource-list" aria-label="Discovered preview ports">
            {ports.data.items.map(({ port }) => (
              <li key={port}>
                <span className="mono">Port {port}</span>
                <button
                  type="button"
                  disabled={issue.isPending}
                  onClick={() => issue.mutate(port)}
                >
                  Create preview link
                </button>
              </li>
            ))}
          </ul>
          {issue.isError && <ErrorState error={issue.error} />}
          {ticket && (
            expired ? (
              <p className="warning" role="alert">
                Preview link for port {ticket.port} expired. Create a new link.
              </p>
            ) : (
              <p className="notice">
                <a href={ticket.url} target="_blank" rel="noreferrer">
                  Open port {ticket.port} preview
                </a>{" "}
                · reusable until {new Date(ticket.expiresAt).toLocaleTimeString()}
              </p>
            )
          )}
        </>
      )}
    </section>
  );
}

function CapsuleActions({
  capsule,
  pauseSupported = true,
  snapshotSupported = true,
}: {
  capsule: Capsule;
  pauseSupported?: boolean;
  snapshotSupported?: boolean;
}) {
  const queryClient = useQueryClient();
  const [pendingAction, setPendingAction] = useState<"delete" | "seal">();
  const mutation = useMutation<unknown, Error, "pause" | "resume" | "delete" | "seal">({
    mutationFn: (action: "pause" | "resume" | "delete" | "seal") => {
      if (action === "seal") {
        return api.seal(capsule.id, capsule.resourceVersion);
      }
      return api.lifecycle(action, capsule.id, capsule.resourceVersion);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["capsule", capsule.id] });
    },
  });
  const button = (
    action: "pause" | "resume" | "delete" | "seal",
    label: string,
    danger = false,
  ) => (
    <button
      type="button"
      className={danger ? "danger" : undefined}
      disabled={mutation.isPending}
      onClick={() => {
        if (danger) {
          setPendingAction(action as "delete" | "seal");
          return;
        }
        mutation.mutate(action);
      }}
    >
      {label}
    </button>
  );
  return (
    <section className="capsule-actions">
      <div className="actions">
        {pauseSupported && capsule.state === "Ready" && button("pause", "Pause")}
        {pauseSupported && capsule.state === "Paused" && button("resume", "Resume")}
        {snapshotSupported &&
          ["Ready", "Paused"].includes(capsule.state) &&
          button("seal", "Seal", true)}
        {!["Sealed", "Deleting", "Deleted"].includes(capsule.state) &&
          button("delete", "Delete", true)}
      </div>
      {capsule.state === "Sealed" && (
        <p className="notice">This Capsule is immutable and permanently sealed.</p>
      )}
      {mutation.isError && <ErrorState error={mutation.error} />}
      {pendingAction && (
        <section
          className="confirm-dialog"
          role="alertdialog"
          aria-modal="true"
          aria-labelledby="capsule-action-title"
        >
          <h2 id="capsule-action-title">
            {pendingAction === "seal"
              ? `Seal ${capsule.name}?`
              : `Delete ${capsule.name}?`}
          </h2>
          <p>
            {pendingAction === "seal"
              ? "Sealing captures a final Moment and makes this Capsule immutable."
              : "Deletion stops the Capsule and revokes its active preview links."}
          </p>
          <div className="actions">
            <button
              type="button"
              className="danger"
              disabled={mutation.isPending}
              onClick={() => {
                const action = pendingAction;
                setPendingAction(undefined);
                mutation.mutate(action);
              }}
            >
              Confirm {pendingAction}
            </button>
            <button
              type="button"
              className="secondary"
              onClick={() => setPendingAction(undefined)}
            >
              Keep Capsule
            </button>
          </div>
        </section>
      )}
    </section>
  );
}

function decodeWorkspaceText(content: string): string | undefined {
  try {
    const binary = Uint8Array.from(atob(content), (value) => value.charCodeAt(0));
    const text = new TextDecoder("utf-8", { fatal: true }).decode(binary);
    const controls = Array.from(text).filter((value) => {
      const code = value.charCodeAt(0);
      return code === 0 || (code < 32 && value !== "\n" && value !== "\r" && value !== "\t");
    }).length;
    return text.includes("\0") || controls > Math.max(4, text.length / 100) ? undefined : text;
  } catch {
    return undefined;
  }
}

export function WorkspaceBrowser({
  capsuleId,
  ready = true,
}: {
  capsuleId: string;
  ready?: boolean;
}) {
  const [directory, setDirectory] = useState("");
  const [selected, setSelected] = useState("");
  const files = useQuery({
    ...queries.workspaceFiles(capsuleId, directory),
    enabled: ready && Boolean(capsuleId),
  });
  const content = useQuery({
    queryKey: ["workspace-file", capsuleId, selected],
    queryFn: ({ signal }) => api.workspaceFile(capsuleId, selected, signal),
    enabled: ready && Boolean(selected),
  });
  const text = content.data ? decodeWorkspaceText(content.data.content) : undefined;
  const enter = (name: string) => {
    setDirectory(directory ? `${directory}/${name}` : name);
    setSelected("");
  };
  const up = () => {
    setDirectory(directory.split("/").slice(0, -1).join("/"));
    setSelected("");
  };
  return (
    <section className="panel">
      <div className="section-heading">
        <div>
          <h2>Workspace files</h2>
          <span className="mono">/{directory}</span>
        </div>
        {directory ? <button type="button" className="secondary" onClick={up}>Up</button> : null}
      </div>
      {!ready ? <p>File browsing is available only while the Capsule is Ready.</p> :
        files.isPending ? <p role="status">Loading workspace files…</p> :
          files.isError ? <ErrorState error={files.error} /> :
            <div className="workspace-browser">
              <ul className="file-tree" aria-label="Workspace file tree">
                {files.data.items.map((entry) => (
                  <li key={entry.name}>
                    {entry.type === "directory" ? (
                      <button type="button" className="file-entry" onClick={() => enter(entry.name)}>
                        {entry.name}/
                      </button>
                    ) : entry.type === "file" ? (
                      <button
                        type="button"
                        className="file-entry"
                        onClick={() => setSelected(directory ? `${directory}/${entry.name}` : entry.name)}
                      >
                        {entry.name}
                      </button>
                    ) : (
                      <span>{entry.name} <small>({entry.type} unsupported)</small></span>
                    )}
                    {entry.type === "file" && entry.size > 1_048_576 ?
                      <small> (too large; unsupported)</small> : null}
                  </li>
                ))}
              </ul>
              <div className="file-viewer" aria-live="polite">
                {!selected ? <p>Select a text file to review it.</p> :
                  content.isPending ? <p role="status">Loading file…</p> :
                    content.isError ? <p role="alert">This file is too large or unsupported.</p> :
                      text === undefined ? <p role="status">Binary content is unsupported.</p> :
                        <CodeViewer content={text} path={selected} />}
              </div>
            </div>}
    </section>
  );
}

export function ShipPanel({ capsule }: { capsule: Capsule }) {
  const [confirmation, setConfirmation] = useState("");
  const [result, setResult] = useState<Delivery>();
  const inspection = useQuery({
    ...queries.deliveryInspection(capsule.id),
    enabled: capsule.state === "Ready",
  });
  const mutation = useMutation({
    mutationFn: (input: CreateDeliveryRequest) => api.createDelivery(capsule.id, input),
    onSuccess: setResult,
  });
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!inspection.data || confirmation !== "ship") return;
    const data = new FormData(event.currentTarget);
    const branch = String(data.get("branch") ?? "").trim();
    const message = String(data.get("message") ?? "").trim();
    const title = String(data.get("title") ?? "").trim();
    const body = String(data.get("body") ?? "");
    const base = String(data.get("base") ?? "").trim();
    const openPullRequest = data.get("openPullRequest") === "on";
    mutation.mutate({
      action: openPullRequest ? "open_pull_request" : "push",
      approved: true,
      expectedResourceVersion: inspection.data.capsuleResourceVersion,
      expectedHead: inspection.data.head,
      expectedTree: inspection.data.tree,
      remoteBranch: branch,
      ...(message ? { commitMessage: message } : {}),
      ...(title ? { pullRequestTitle: title } : {}),
      ...(body ? { pullRequestBody: body } : {}),
      ...(base ? { baseBranch: base } : {}),
    });
  }
  return (
    <section className="panel">
      <h2>Ship reviewed changes</h2>
      {capsule.state !== "Ready" ? <p>Shipping requires a Ready Capsule.</p> :
        inspection.isPending ? <p role="status">Inspecting exact Git state…</p> :
          inspection.isError ? <ErrorState error={inspection.error} /> :
            <form className="ship-form" onSubmit={submit}>
              <dl className="facts compact">
                <div><dt>HEAD</dt><dd className="mono">{inspection.data.head}</dd></div>
                <div><dt>Tree</dt><dd className="mono">{inspection.data.tree}</dd></div>
                <div><dt>Working tree</dt><dd>{inspection.data.dirty ? "Dirty" : "Clean"}</dd></div>
                <div><dt>Current branch</dt><dd>{inspection.data.branch || "Detached"}</dd></div>
              </dl>
              <label>Destination branch<input name="branch" required /></label>
              <label>Commit message<input name="message" required={inspection.data.dirty} /></label>
              <label className="check-label">
                <input name="openPullRequest" type="checkbox" /> Open GitHub pull request
              </label>
              <label>Pull request title<input name="title" /></label>
              <label>Base branch<input name="base" placeholder={inspection.data.defaultBranch} /></label>
              <label>Pull request body<textarea name="body" rows={5} /></label>
              <label>
                Type <span className="mono">ship</span> to approve this exact HEAD and tree
                <input
                  aria-label="Ship confirmation"
                  autoComplete="off"
                  value={confirmation}
                  onChange={(event) => setConfirmation(event.target.value)}
                />
              </label>
              <button type="submit" disabled={confirmation !== "ship" || mutation.isPending}>
                {mutation.isPending ? "Shipping…" : "Ship"}
              </button>
            </form>}
      {mutation.isError ? <ErrorState error={mutation.error} /> : null}
      {result ? (
        <p role="status">
          Delivery {result.id} is {result.state}.
          {result.resultPullRequestUrl ? <> <a href={result.resultPullRequestUrl}>Open pull request</a>.</> : null}
        </p>
      ) : null}
    </section>
  );
}

export function EventActivity({ runId }: { runId: string }) {
  const replay = useQuery(queries.events(runId));
  const [live, setLive] = useState<RunEvent[]>([]);
  const [streamStatus, setStreamStatus] = useState<StreamStatus>("connecting");
  const [gap, setGap] = useState<{ expected: number; received: number }>();

  useEffect(() => {
    if (!replay.data) return;
    setLive(replay.data.items);
    const controller = new AbortController();
    let cursor = replay.data.nextCursor;
    void followRunEvents(
      runId,
      cursor,
      controller.signal,
      (event) => {
        if (event.sequence > cursor + 1) {
          setGap({ expected: cursor + 1, received: event.sequence });
        }
        cursor = Math.max(cursor, event.sequence);
        setLive((current) => {
          const bySequence = new Map(current.map((item) => [item.sequence, item]));
          bySequence.set(event.sequence, event);
          return [...bySequence.values()].sort((a, b) => a.sequence - b.sequence);
        });
      },
      setStreamStatus,
    ).catch(() => {
      if (!controller.signal.aborted) setStreamStatus("reconnecting");
    });
    return () => controller.abort();
  }, [replay.isSuccess, runId]);

  if (replay.isPending) return <p role="status">Loading activity…</p>;
  if (replay.isError) return <ErrorState error={replay.error} />;
  return (
    <section className="panel">
      <div className="section-heading">
        <h2>Activity</h2>
        <span className="connection" role="status">
          {streamStatus} · cursor {live.at(-1)?.sequence ?? replay.data.nextCursor}
        </span>
      </div>
      {gap && (
        <p className="warning" role="alert">
          Replay gap: expected sequence {gap.expected}, received {gap.received}.
        </p>
      )}
      {live.length === 0 ? (
        <p className="empty-inline">No Run events yet.</p>
      ) : (
        <ol className="event-list">
          {live.map((event) => (
            <li key={event.sequence}>
              <span className="mono">#{event.sequence}</span>
              <strong>{event.type}</strong>
              <time dateTime={event.timestamp}>{new Date(event.timestamp).toLocaleString()}</time>
              {event.metadata && <code>{JSON.stringify(event.metadata)}</code>}
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

export function CapsuleDetail() {
  const { capsuleId = "" } = useParams();
  const queryClient = useQueryClient();
  const capsule = useQuery(queries.capsule(capsuleId));
  const runs = useQuery(queries.runs(capsuleId));
  const capabilities = useQuery(queries.capabilities());
  const moments = useQuery({
    ...queries.moments(capsuleId),
    enabled: Boolean(capsuleId) && capabilities.data?.snapshot === true,
  });
  const startNative = useMutation({
    mutationFn: () => {
      if (!capsule.data?.harness) {
        throw new Error("This Capsule has no native launcher harness");
      }
      return api.startRun(capsule.data.id, capsule.data.harness);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["runs", capsuleId] });
    },
  });
  if (capsule.isPending) return <p className="loading" role="status">Loading Capsule…</p>;
  if (capsule.isError) return <ErrorState error={capsule.error} />;
  const value = capsule.data;
  const currentRun = runs.data?.items.find((run) =>
    ["Queued", "Starting", "Running", "Cancelling"].includes(run.state),
  ) ?? runs.data?.items[0];
  const reconnect = currentRun
    ? ["Queued", "Starting", "Running", "Cancelling"].includes(currentRun.state)
    : false;
  const canStartNative =
    value.state === "Ready" &&
    capabilities.data?.run === true &&
    !reconnect;
  return (
    <article className="capsule-workspace">
      <header className="workspace-view-header">
        <div>
          <p className="eyebrow">
            {value.harness ? `${value.harness} Capsule` : "Structured Capsule"}
          </p>
          <h1>{value.name}</h1>
        </div>
        <div className="workspace-header-meta">
          <StateBadge state={value.state} />
        </div>
      </header>
      {value.failure && <p className="error-state" role="alert">{value.failure}</p>}
      <CapsuleActions
        capsule={value}
        pauseSupported={capabilities.data?.pause === true}
        snapshotSupported={capabilities.data?.snapshot === true}
      />
      {value.harness ? (
        <section className="primary-session" aria-label="Native harness terminal">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Live session</p>
              <h2>{currentRun ? currentRun.harness : value.harness}</h2>
            </div>
            <div className="session-heading-actions">
              {currentRun && <StateBadge state={currentRun.state} />}
              {canStartNative && (
                <button
                  type="button"
                  className="primary-button"
                  disabled={startNative.isPending}
                  onClick={() => startNative.mutate()}
                >
                  {startNative.isPending
                    ? `Starting ${value.harness}…`
                    : `Start ${value.harness}`}
                </button>
              )}
            </div>
          </div>
          {runs.isPending || capabilities.isPending ? (
            <p className="tool-loading" role="status">Preparing terminal…</p>
          ) : runs.isError ? (
            <ErrorState error={runs.error} />
          ) : capabilities.isError ? (
            <ErrorState error={capabilities.error} />
          ) : !currentRun ? (
            <p className="tool-empty">
              No native Run is active. Start {value.harness} to open its
              interactive terminal.
            </p>
          ) : !reconnect ? (
            <div className="run-ended-state" role={currentRun.failure ? "alert" : "status"}>
              <strong>{currentRun.harness} is not running.</strong>
              <span>
                {currentRun.failure ||
                  `The previous Run ended with state ${currentRun.state}.`}
              </span>
              {currentRun.finishedAt && (
                <time dateTime={currentRun.finishedAt}>
                  Ended {new Date(currentRun.finishedAt).toLocaleString()}
                </time>
              )}
            </div>
          ) : (
            <Suspense fallback={<p role="status">Loading terminal renderer…</p>}>
              <TerminalView
                runId={currentRun.id}
                reconnect={reconnect}
                supported={capabilities.data.attach}
              />
            </Suspense>
          )}
          {startNative.isError && <ErrorState error={startNative.error} />}
        </section>
      ) : (
        <CapsuleThreads capsule={value} />
      )}
      {capabilities.data?.delivery === true && (
        <details className="workspace-drawer">
          <summary>Ship reviewed changes</summary>
          <ShipPanel capsule={value} />
        </details>
      )}
      <section className="workspace-section">
        <div className="section-heading">
          <h2>Moments</h2>
          <Link to={`/ui/timelines/${value.timelineId}`}>Open lineage</Link>
        </div>
        {capabilities.data?.snapshot !== true ? (
          <p className="tool-empty">Filesystem Moments are unsupported by this provider.</p>
        ) : moments.isPending ? <p role="status">Loading Moments…</p> : moments.isError ? (
          <ErrorState error={moments.error} />
        ) : moments.data.items.length === 0 ? <p>No filesystem Moments.</p> : (
          <ul className="resource-list">
            {moments.data.items.map((moment) => (
              <li key={moment.id}>
                <Link to={`/ui/moments/${moment.id}`}>{moment.name}</Link>
                <span>{moment.final ? "Final" : "Checkpoint"}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </article>
  );
}

export function DiffReview({ result }: { result: GitResult }) {
  const files = useMemo(() => {
    const entries: Array<{ path: string; status: string; binary: boolean }> = [];
    const sections = result.content.split(/^diff --git /m).slice(1);
    for (const section of sections) {
      const first = section.split("\n", 1)[0] ?? "";
      const match = first.match(/^a\/(.+?) b\/(.+)$/);
      const filePath = match?.[2] ?? first;
      const status = section.includes("\ndeleted file mode")
        ? "deleted"
        : section.includes("\nnew file mode")
          ? "added"
          : "modified";
      entries.push({ path: filePath, status, binary: /Binary files|GIT binary patch/.test(section) });
    }
    return entries;
  }, [result.content]);
  const binaryOnly = files.length > 0 && files.every((file) => file.binary);
  return (
    <section>
      {result.truncated && (
        <p className="warning" role="alert">
          Diff was truncated at the server response limit. Review with the CLI for
          complete content.
        </p>
      )}
      {files.length === 0 && !result.content ? (
        <p className="empty-state">No working-tree diff.</p>
      ) : (
        <div className="diff-layout">
          <nav className="file-tree" aria-label="Changed files">
            <h2>Files</h2>
            <ul>
              {files.map((file) => (
                <li key={file.path}>
                  <span className={`file-status ${file.status}`}>{file.status}</span>
                  <span>{file.path}</span>
                  {file.binary && <span>binary</span>}
                </li>
              ))}
            </ul>
          </nav>
          <div className="diff-content">
            {binaryOnly ? (
              <p className="notice">Binary changes cannot be displayed as text.</p>
            ) : (
              <pre aria-label="Unified diff">{result.content}</pre>
            )}
          </div>
        </div>
      )}
    </section>
  );
}

function DiffPage() {
  const { capsuleId = "" } = useParams();
  const capabilities = useQuery(queries.capabilities());
  const capsule = useQuery(queries.capsule(capsuleId));
  if (capabilities.isPending || capsule.isPending) {
    return <p className="loading" role="status">Checking review capability…</p>;
  }
  if (capabilities.isError) return <ErrorState error={capabilities.error} />;
  if (capsule.isError) return <ErrorState error={capsule.error} />;
  if (!capabilities.data.git) {
    return <p className="notice">Git review is unsupported by this provider.</p>;
  }
  return (
    <article className="inspector-page diff-page">
      <header className="workspace-view-header">
        <div><p className="eyebrow">Capsule review</p><h1>Working-tree diff</h1></div>
        <Link to={`/ui/capsules/${capsuleId}`}>Back to Capsule</Link>
      </header>
      <p className="notice">
        Changes are open in the Capsule tools pane. Inspect the exact Git state
        here before shipping.
      </p>
      {capabilities.data.delivery ? (
        <ShipPanel capsule={capsule.data} />
      ) : (
        <p className="tool-empty">Delivery is unsupported by this provider.</p>
      )}
    </article>
  );
}

export function RunDetail() {
  const { runId = "" } = useParams();
  const run = useQuery(queries.run(runId));
  const capabilities = useQuery(queries.capabilities());
  const queryClient = useQueryClient();
  const cancellation = useMutation({
    mutationFn: (value: Run) => api.cancelRun(value.id, value.resourceVersion),
    onSuccess: async () => queryClient.invalidateQueries({ queryKey: ["run", runId] }),
  });
  if (run.isPending) return <p className="loading" role="status">Loading Run…</p>;
  if (run.isError) return <ErrorState error={run.error} />;
  const value = run.data;
  const active = ["Queued", "Starting", "Running"].includes(value.state);
  return (
    <article className="inspector-page run-page">
      <header className="workspace-view-header">
        <div><p className="eyebrow">Run</p><h1>{value.harness}</h1><p className="mono">{value.id}</p></div>
        <div className="workspace-header-meta">
          <Link
            className="button-link secondary"
            to={`/ui/capsules/${encodeURIComponent(value.capsuleId)}`}
          >
            Back to workspace
          </Link>
          <StateBadge state={value.state} />
        </div>
      </header>
      <dl className="facts panel">
        <div><dt>Created</dt><dd>{new Date(value.createdAt).toLocaleString()}</dd></div>
        <div><dt>Started</dt><dd>{value.startedAt ? new Date(value.startedAt).toLocaleString() : "Pending"}</dd></div>
        <div><dt>Finished</dt><dd>{value.finishedAt ? new Date(value.finishedAt).toLocaleString() : "Not finished"}</dd></div>
        <div><dt>Exit status</dt><dd>{value.exitStatus ?? "Unavailable"}</dd></div>
        <div><dt>Resource version</dt><dd>{value.resourceVersion}</dd></div>
      </dl>
      <div className="actions">
        {active && <button type="button" disabled={cancellation.isPending} onClick={() => cancellation.mutate(value)}>Cancel Run</button>}
        {capabilities.data?.attach && (
          <Link className="button-link" to={`/ui/runs/${value.id}/terminal`}>Open terminal</Link>
        )}
      </div>
      {value.failure && <p className="error-state" role="alert">{value.failure}</p>}
      {cancellation.isError && <ErrorState error={cancellation.error} />}
      <EventActivity runId={value.id} />
    </article>
  );
}

function MomentDetail() {
  const { momentId = "" } = useParams();
  const moment = useQuery(queries.moment(momentId));
  if (moment.isPending) return <p className="loading" role="status">Loading Moment…</p>;
  if (moment.isError) return <ErrorState error={moment.error} />;
  const value = moment.data;
  return (
    <article className="inspector-page">
      <header className="workspace-view-header">
        <div><p className="eyebrow">Immutable filesystem Moment</p><h1>{value.name}</h1></div>
        <span>{value.final ? "Final" : "Checkpoint"}</span>
      </header>
      <p className="notice">A Moment captures filesystem state only. It does not capture RAM, processes, sockets, or live terminal state.</p>
      <dl className="facts panel">
        <div><dt>Created</dt><dd>{new Date(value.createdAt).toLocaleString()}</dd></div>
        <div><dt>Archive size</dt><dd>{value.archiveSize.toLocaleString()} bytes</dd></div>
        <div><dt>Git branch</dt><dd>{value.gitBranch || "Unavailable"}</dd></div>
        <div><dt>Git HEAD</dt><dd className="mono">{value.gitHead || "Unavailable"}</dd></div>
        <div><dt>Dirty summary</dt><dd>{value.gitDirtySummary || "Clean or unavailable"}</dd></div>
        <div><dt>Image identity</dt><dd className="mono">{value.imageDigest}</dd></div>
        <div><dt>Parent Moment</dt><dd>{value.parentMomentId ? <Link to={`/ui/moments/${value.parentMomentId}`}>{value.parentMomentId}</Link> : "Root"}</dd></div>
        <div><dt>Timeline</dt><dd><Link to={`/ui/timelines/${value.timelineId}`}>{value.timelineId}</Link></dd></div>
      </dl>
    </article>
  );
}

export function Lineage({ view }: { view: TimelineView }) {
  const nodes = [...view.ancestry].reverse();
  if (!nodes.some((node) => node.id === view.timeline.id)) nodes.push(view.timeline);
  return (
    <div className="lineage">
      <svg
        viewBox={`0 0 640 ${Math.max(100, nodes.length * 82)}`}
        role="img"
        aria-labelledby="lineage-title lineage-description"
      >
        <title id="lineage-title">Timeline lineage</title>
        <desc id="lineage-description">Parent-to-child Timeline ancestry. A text alternative follows.</desc>
        {nodes.map((node, index) => {
          const y = 45 + index * 82;
          return (
            <g key={node.id}>
              {index > 0 && <line x1="32" y1={y - 58} x2="32" y2={y - 12} />}
              <circle cx="32" cy={y} r="11" />
              <text x="58" y={y - 5}>{node.reason}</text>
              <text x="58" y={y + 17}>{node.id}</text>
            </g>
          );
        })}
      </svg>
      <ol className="lineage-alternative" aria-label="Timeline ancestry text alternative">
        {nodes.map((node) => (
          <li key={node.id}>
            <strong>{node.reason}</strong> Timeline {node.id}
            {node.forkedFromMomentId && <> from Moment <Link to={`/ui/moments/${node.forkedFromMomentId}`}>{node.forkedFromMomentId}</Link></>}
          </li>
        ))}
      </ol>
    </div>
  );
}

function TimelinePage() {
  const { timelineId = "" } = useParams();
  const timeline = useQuery(queries.timeline(timelineId));
  if (timeline.isPending) return <p className="loading" role="status">Loading lineage…</p>;
  if (timeline.isError) return <ErrorState error={timeline.error} />;
  return (
    <article className="inspector-page">
      <header className="workspace-view-header">
        <div><p className="eyebrow">Timeline</p><h1>Lineage</h1><p className="mono">{timeline.data.timeline.id}</p></div>
      </header>
      <p className="notice">Fork and rewind create descendants; they never overwrite earlier filesystem history or imply process continuity.</p>
      <Lineage view={timeline.data} />
    </article>
  );
}

function TerminalPage() {
  const { runId = "" } = useParams();
  const run = useQuery(queries.run(runId));
  const capabilities = useQuery(queries.capabilities());
  if (run.isPending || capabilities.isPending) return <p className="loading" role="status">Preparing terminal…</p>;
  if (run.isError) return <ErrorState error={run.error} />;
  if (capabilities.isError) return <ErrorState error={capabilities.error} />;
  const reconnect = ["Queued", "Starting", "Running", "Cancelling"].includes(run.data.state);
  return (
    <article className="terminal-page">
      <header className="workspace-view-header">
        <div><p className="eyebrow">Run terminal</p><h1>{run.data.harness}</h1></div>
        <div className="workspace-header-meta">
          <Link to={`/ui/runs/${runId}`}>Run details</Link>
          <Link
            className="button-link secondary"
            to={`/ui/capsules/${encodeURIComponent(run.data.capsuleId)}`}
          >
            Back to workspace
          </Link>
        </div>
      </header>
      <Suspense fallback={<p role="status">Loading terminal renderer…</p>}>
        <TerminalView
          runId={runId}
          reconnect={reconnect}
          supported={capabilities.data.attach}
        />
      </Suspense>
    </article>
  );
}

function NotFound() {
  return <section className="empty-state"><h1>Page not found</h1><Link to="/ui/">Return to Capsules</Link></section>;
}

export function App() {
  return (
    <Routes>
      <Route path="/ui" element={<AppShell />}>
        <Route index element={<CapsuleList />} />
        <Route path="settings/harnesses" element={<HarnessSetups />} />
        <Route path="capsules/:capsuleId" element={<CapsuleDetail />} />
        <Route path="capsules/:capsuleId/diff" element={<DiffPage />} />
        <Route path="threads" element={<ThreadFleet />} />
        <Route path="threads/:threadId" element={<ThreadDetail />} />
        <Route path="runs/:runId" element={<RunDetail />} />
        <Route path="runs/:runId/terminal" element={<TerminalPage />} />
        <Route path="moments/:momentId" element={<MomentDetail />} />
        <Route path="timelines/:timelineId" element={<TimelinePage />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}
