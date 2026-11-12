import { HarnessSetups } from "./components/HarnessSetups";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from "@tanstack/react-query";
import { lazy, Suspense, useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { createPortal } from "react-dom";
import {
  Link,
  Navigate,
  Route,
  Routes,
  useLocation,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";

import { api, normalizeAPIError } from "./api/client";
import { followRunEvents, type StreamStatus } from "./api/events";
import type {
  Capabilities,
  Capsule,
  CreateDeliveryRequest,
  Delivery,
  GitResult,
  CapsulePage,
  Moment,
  PreviewTicket,
  ProjectPage,
  Run,
  RunEvent,
  RunPage,
  Thread,
  TimelineView,
} from "./api/generated/types.gen";
import { queries } from "./api/queries";
import { olderPackRelease } from "./api/harnessPacks";
import { Home } from "./components/Home";
import { ProjectSettings } from "./components/ProjectSettings";
import { CapsuleStartup } from "./components/CapsuleStartup";
import { DeliveryDecision } from "./components/DeliveryDecision";
import { isStarting } from "./components/startup";
import { WorkspaceSkeleton } from "./components/ui/loading";
import {
  ThreadCreate,
  ThreadDetail,
  ThreadFleet,
} from "./components/Threads";
import { PendingReviewNotice } from "./components/ReviewComments";
import { Menu as ActionsMenu, type MenuItem as ActionsMenuItem } from "./components/ui/Menu";
import { HarnessMark } from "./components/ui/HarnessMark";
import { PlusIcon, ThreadIcon } from "./components/ui/Icons";
import { StateBadge } from "./components/ui/StateBadge";
import {
  ActivityDot,
  AppShell,
  sessionPath,
} from "./shell/AppShell";
import {
  capsuleSessions,
  defaultSession,
  destinationAfterCapsuleDelete,
  describeCapsule,
  harnessName,
  pinnedSetupText,
  relativeTime,
  TERMINAL_SESSION,
} from "./shell/capsuleActivity";
import { requestedCapsuleAction } from "./shell/paletteCommands";
const TerminalView = lazy(() =>
  import("./components/Terminal").then((module) => ({
    default: module.TerminalView,
  })),
);

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

const activeRunStates: Run["state"][] = ["Queued", "Starting", "Running", "Cancelling"];

export const isActiveRun = (run: Run) => activeRunStates.includes(run.state);

// A structured Thread's Run is stopped through its Thread session so the
// adapter shuts down cleanly; any other Run is cancelled directly.
async function stopRun(run: Run, threads: Thread[]): Promise<void> {
  const thread = threads.find((item) => item.currentRunId === run.id);
  if (thread) {
    await api.threadSession("cancel", thread.id, thread.resourceVersion);
    return;
  }
  await api.cancelRun(run.id, run.resourceVersion);
}

function ActiveRuns({
  runs,
  threads,
}: {
  runs: Run[];
  threads: Thread[];
}) {
  const queryClient = useQueryClient();
  const stop = useMutation({
    mutationFn: (run: Run) => stopRun(run, threads),
    onSettled: async (_result, _error, run) => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["runs", run.capsuleId] }),
        queryClient.invalidateQueries({ queryKey: ["threads", run.capsuleId] }),
      ]);
    },
  });
  return (
    <>
      <ul className="active-runs" aria-label="Running sessions">
        {runs.map((run) => {
          const stopping =
            run.state === "Cancelling" || (stop.isPending && stop.variables?.id === run.id);
          return (
            <li key={run.id}>
              <HarnessMark harness={run.harness} />
              <span>
                {harnessName(run.harness)}
                {threads.some((thread) => thread.currentRunId === run.id)
                  ? " task session"
                  : " terminal"}
              </span>
              <button
                type="button"
                disabled={stopping}
                onClick={() => stop.mutate(run)}
              >
                {stopping ? "Stopping…" : "Stop"}
              </button>
            </li>
          );
        })}
      </ul>
      {stop.isError && <ErrorState error={stop.error} />}
    </>
  );
}

function CapsuleMenu({
  capsule,
  pauseSupported = true,
  snapshotSupported = true,
  capabilitiesKnown = true,
  runs = [],
  threads = [],
}: {
  capsule: Capsule;
  pauseSupported?: boolean;
  snapshotSupported?: boolean;
  capabilitiesKnown?: boolean;
  runs?: Run[];
  threads?: Thread[];
}) {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const handledRequest = useRef("");
  const [pendingAction, setPendingAction] = useState<"delete" | "seal">();
  // Same list the sidebar groups under the Project. Kept warm so a delete can
  // choose a sibling from cache the moment the daemon accepts it.
  const siblings = useQuery(queries.mainCapsules(capsule.projectId));
  const projects = useQuery(queries.projects());
  const mutation = useMutation<unknown, Error, "pause" | "resume" | "delete" | "seal">({
    mutationFn: (action: "pause" | "resume" | "delete" | "seal") => {
      if (action === "seal") {
        return api.seal(capsule.id, capsule.resourceVersion);
      }
      return api.lifecycle(action, capsule.id, capsule.resourceVersion);
    },
    onSuccess: async (_value, action) => {
      // Leave on the accepted delete, while the Capsule is still Deleting.
      // Waiting for state Deleted would keep this page up, and would also
      // send a direct visit to an already-deleted Capsule away.
      if (action === "delete") {
        const siblingKey = queries.mainCapsules(capsule.projectId).queryKey;
        let items =
          queryClient.getQueryData<CapsulePage>(siblingKey)?.items ?? siblings.data?.items;
        if (!items) {
          try {
            items = (await queryClient.ensureQueryData(queries.mainCapsules(capsule.projectId)))
              .items;
          } catch {
            items = [];
          }
        }
        let knownProjects =
          queryClient.getQueryData<ProjectPage>(queries.projects().queryKey)?.items ??
          projects.data?.items;
        if (!knownProjects) {
          try {
            knownProjects = (await queryClient.ensureQueryData(queries.projects())).items;
          } catch {
            knownProjects = [];
          }
        }
        navigate(
          destinationAfterCapsuleDelete(
            capsule,
            items,
            knownProjects.some((item) => item.id === capsule.projectId),
          ),
          { replace: true },
        );
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["capsule", capsule.id] }),
        queryClient.invalidateQueries({ queryKey: ["capsules", capsule.projectId] }),
      ]);
    },
  });
  const canPause = pauseSupported && capsule.state === "Ready";
  const canResume = pauseSupported && capsule.state === "Paused";
  const canSeal = snapshotSupported && ["Ready", "Paused"].includes(capsule.state);
  const canDelete = !["Sealed", "Deleting", "Deleted"].includes(capsule.state);
  // meridiand refuses to delete a Capsule while any Run is active.
  const activeRuns = runs.filter(isActiveRun);
  const blockedByRuns = pendingAction === "delete" && activeRuns.length > 0;

  // The command palette asks for lifecycle actions through history state so
  // this menu stays their only owner. Seal and Delete open the confirmation
  // and never run from the request itself.
  const requested = requestedCapsuleAction(location.state);
  useEffect(() => {
    if (!requested || !capabilitiesKnown || handledRequest.current === location.key) return;
    handledRequest.current = location.key;
    navigate(
      { pathname: location.pathname, search: location.search, hash: location.hash },
      { replace: true, state: null },
    );
    if (requested === "pause" && canPause) mutation.mutate("pause");
    else if (requested === "resume" && canResume) mutation.mutate("resume");
    else if (requested === "seal" && canSeal) setPendingAction("seal");
    else if (requested === "delete" && canDelete) setPendingAction("delete");
  }, [
    requested,
    capabilitiesKnown,
    location,
    navigate,
    mutation,
    canPause,
    canResume,
    canSeal,
    canDelete,
  ]);

  const items: ActionsMenuItem[] = [];
  if (canPause) {
    items.push({ label: "Pause", onSelect: () => mutation.mutate("pause") });
  }
  if (canResume) {
    items.push({ label: "Resume", onSelect: () => mutation.mutate("resume") });
  }
  items.push({
    label: "Open lineage",
    onSelect: () => navigate(`/ui/timelines/${encodeURIComponent(capsule.timelineId)}`),
  });
  if (canSeal) {
    items.push({ label: "Seal…", danger: true, onSelect: () => setPendingAction("seal") });
  }
  if (canDelete) {
    items.push({ label: "Delete…", danger: true, onSelect: () => setPendingAction("delete") });
  }
  return (
    <>
      <ActionsMenu
        label="Capsule actions"
        items={items.map((item) => ({ ...item, disabled: mutation.isPending }))}
      />
      {(mutation.isError || pendingAction) && (
        <div className="capsule-bar-notice">
          {mutation.isError && <ErrorState error={mutation.error} />}
          {/* The capsule bar's backdrop filter would otherwise become the
              fixed confirmation's containing block and clip it. */}
          {pendingAction && createPortal(
            <section
              className="confirm-dialog"
              role="alertdialog"
              aria-modal="true"
              aria-labelledby="capsule-action-title"
              onKeyDown={(event) => {
                if (event.key === "Escape") setPendingAction(undefined);
              }}
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
              {blockedByRuns && (
                <>
                  <p className="notice" role="status">
                    Stop {activeRuns.length === 1 ? "this session" : "these sessions"} before
                    deleting. Anything the agent hasn't saved in the workspace is lost.
                  </p>
                  <ActiveRuns runs={activeRuns} threads={threads} />
                </>
              )}
              <div className="actions">
                <button
                  type="button"
                  className="danger"
                  disabled={mutation.isPending || blockedByRuns}
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
                  autoFocus
                  onClick={() => setPendingAction(undefined)}
                >
                  Keep Capsule
                </button>
              </div>
            </section>,
            document.body,
          )}
        </div>
      )}
    </>
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

function TerminalSession({
  capsule,
  runs,
  capabilities,
}: {
  capsule: Capsule;
  runs: UseQueryResult<RunPage>;
  capabilities: UseQueryResult<Capabilities>;
}) {
  const queryClient = useQueryClient();
  const harness = capsule.harness ?? "";
  const startNative = useMutation({
    mutationFn: () => api.startRun(capsule.id, harness),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["runs", capsule.id] });
    },
  });
  const currentRun = runs.data?.items.find((run) =>
    ["Queued", "Starting", "Running", "Cancelling"].includes(run.state),
  ) ?? runs.data?.items[0];
  const reconnect = currentRun
    ? ["Queued", "Starting", "Running", "Cancelling"].includes(currentRun.state)
    : false;
  const canStartNative =
    capsule.state === "Ready" &&
    capabilities.data?.run === true &&
    !reconnect;
  const [confirmStop, setConfirmStop] = useState(false);
  const stopNative = useMutation({
    mutationFn: (run: Run) => api.cancelRun(run.id, run.resourceVersion),
    onSettled: async () => {
      setConfirmStop(false);
      await queryClient.invalidateQueries({ queryKey: ["runs", capsule.id] });
    },
  });
  const canStop =
    currentRun !== undefined &&
    ["Queued", "Starting", "Running"].includes(currentRun.state);
  return (
    <section className="primary-session" aria-label="Native harness terminal">
      <div className="section-heading">
        <div className="session-heading-title">
          <h2>{harnessName(currentRun?.harness ?? harness)} terminal</h2>
          {currentRun && <StateBadge state={currentRun.state} />}
        </div>
        <div className="session-heading-actions">
          {canStop && currentRun && !confirmStop && (
            <button type="button" onClick={() => setConfirmStop(true)}>
              Stop
            </button>
          )}
          {canStop && currentRun && confirmStop && (
            <span className="inline-confirm" role="group" aria-label="Confirm stop">
              <span>Stop {harnessName(harness)}?</span>
              <button
                type="button"
                className="danger"
                disabled={stopNative.isPending}
                onClick={() => stopNative.mutate(currentRun)}
              >
                {stopNative.isPending ? "Stopping…" : "Stop"}
              </button>
              <button type="button" className="secondary" onClick={() => setConfirmStop(false)}>
                Keep running
              </button>
            </span>
          )}
          {currentRun?.state === "Cancelling" && <span className="muted-copy">Stopping…</span>}
          {canStartNative && (
            <button
              type="button"
              className="primary-button"
              disabled={startNative.isPending}
              onClick={() => startNative.mutate()}
            >
              {startNative.isPending ? `Starting ${harness}…` : `Start ${harness}`}
            </button>
          )}
        </div>
      </div>
      {runs.isPending || capabilities.isPending ? (
        <WorkspaceSkeleton label="Preparing terminal…" />
      ) : runs.isError ? (
        <ErrorState error={runs.error} />
      ) : capabilities.isError ? (
        <ErrorState error={capabilities.error} />
      ) : !currentRun ? (
        isStarting(capsule) ? (
          <WorkspaceSkeleton label="Starting the terminal" />
        ) : (
          <p className="tool-empty">
            {harnessName(harness)} is not running yet. Start it to open its
            interactive terminal.
          </p>
        )
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
      {stopNative.isError && <ErrorState error={stopNative.error} />}
    </section>
  );
}

const NEW_SESSION = "new";

export function CapsuleDetail() {
  const { capsuleId = "" } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const [shipOpen, setShipOpen] = useState(false);
  const capsule = useQuery(queries.capsule(capsuleId));
  const runs = useQuery(queries.runs(capsuleId));
  const capabilities = useQuery(queries.capabilities());
  const projects = useQuery(queries.projects());
  const structured =
    capabilities.data?.structured === true || (capsule.isSuccess && !capsule.data.harness);
  const threads = useQuery({
    ...queries.threads(capsuleId),
    enabled: Boolean(capsuleId) && structured,
  });
  // The installation capability says structured sessions exist somewhere;
  // only the Capsule's own harness profiles say whether it can start one.
  const profiles = useQuery({
    ...queries.harnessProfiles(capsuleId),
    enabled: Boolean(capsuleId) && structured && capsule.data?.state === "Ready",
  });
  const deliveries = useQuery({
    ...queries.deliveries(capsuleId),
    enabled: Boolean(capsuleId) && capabilities.data?.delivery === true,
  });
  const inspection = useQuery({
    ...queries.deliveryInspection(capsuleId),
    enabled:
      capabilities.data?.delivery === true &&
      capabilities.data?.git === true &&
      capsule.data?.state === "Ready",
    staleTime: 30_000,
    refetchOnWindowFocus: false,
  });
  if (capsule.isPending) return <WorkspaceSkeleton label="Loading Capsule…" />;
  if (capsule.isError) return <ErrorState error={capsule.error} />;
  const value = capsule.data;
  const sessions = capsuleSessions(value, runs.data?.items, threads.data?.items);
  const canCreateThread =
    structured &&
    value.state === "Ready" &&
    profiles.data?.items.some((profile) => profile.structured) === true;
  const requested = searchParams.get("session") ?? "";
  // A Thread started from the New Capsule dialog exists only once its
  // Capsule is Ready, so the page can ask for it before it is listed. Wait
  // for it rather than offering a second way to start one.
  const awaitingThread =
    requested !== "" &&
    requested !== NEW_SESSION &&
    requested !== TERMINAL_SESSION &&
    !sessions.some((session) => session.id === requested || session.kind === "thread") &&
    (value.state === "Ready" || isStarting(value));
  const selected =
    (requested === NEW_SESSION && canCreateThread
      ? NEW_SESSION
      : sessions.find((session) => session.id === requested)?.id) ??
    defaultSession(sessions)?.id ??
    (canCreateThread && !awaitingThread ? NEW_SESSION : "");
  const selectSession = (id: string) =>
    setSearchParams({ session: id }, { replace: true });
  const projectName = projects.data?.items.find(
    (project) => project.id === value.projectId,
  )?.name;
  const harness = value.harness || sessions[0]?.harness;
  // New Capsules follow the daemon's release (ADR 0041); this one keeps its image.
  const packRelease = olderPackRelease(
    value.harness,
    value.imageReference,
    capabilities.data?.harnessImages ?? [],
  );
  const chatSelected =
    selected !== "" && selected !== TERMINAL_SESSION && selected !== NEW_SESSION;
  const cliSelected = selected === TERMINAL_SESSION;
  const activity = describeCapsule(value, runs.data?.items, threads.data?.items);
  const pullRequest = deliveries.data?.items.find((item) => item.resultPullRequestUrl);
  const threadTarget = threads.data?.items.find(
    (item) => item.state !== "deleted" && item.state !== "archived",
  );

  return (
    <article className={`capsule-workspace${chatSelected ? " chat-session" : ""}${cliSelected ? " cli-session" : ""}${sessions.length <= 1 ? " solo-session" : ""}`}>
      <header className="capsule-bar">
        <div className="capsule-bar-title">
          {harness && <HarnessMark harness={harness} size="md" />}
          <h1>{value.name}</h1>
          <StateBadge state={value.state} />
          {packRelease && harness && (
            <span
              className="pill pack-release-pill"
              title={`New ${harnessName(harness)} Capsules use ${packRelease.current}. This Capsule keeps the image it was created with until you delete it.`}
            >
              Built on {packRelease.release}
              <span className="sr-only">
                ; new {harnessName(harness)} Capsules use {packRelease.current}
              </span>
            </span>
          )}
          {inspection.data?.branch && (
            <span className="capsule-branch mono">{inspection.data.branch}</span>
          )}
          {pullRequest?.resultPullRequestUrl && (
            <a className="capsule-pr" href={pullRequest.resultPullRequestUrl} target="_blank" rel="noreferrer">
              PR #{pullRequest.resultPullRequestNumber ?? ""}
            </a>
          )}
          {cliSelected && (canCreateThread || threadTarget) && (
            <button
              type="button"
              className="to-thread"
              onClick={() => selectSession(threadTarget?.id ?? NEW_SESSION)}
            >
              <ThreadIcon />
              Thread
            </button>
          )}
          <span className={`pill activity-pill activity-pill-${activity.group}`}>
            <ActivityDot group={activity.group} />
            {activity.label}
          </span>
          <span className="capsule-bar-meta">
            {[projectName, `updated ${relativeTime(value.updatedAt)}`]
              .filter(Boolean)
              .join(" · ")}
          </span>
        </div>
        <div className="capsule-bar-actions">
          {capabilities.data?.delivery === true && (
            <button
              type="button"
              aria-expanded={shipOpen}
              onClick={() => setShipOpen((current) => !current)}
            >
              Ship…
            </button>
          )}
          <CapsuleMenu
            capsule={value}
            pauseSupported={capabilities.data?.pause === true}
            snapshotSupported={capabilities.data?.snapshot === true}
            capabilitiesKnown={capabilities.isSuccess}
            runs={runs.data?.items}
            threads={threads.data?.items}
          />
        </div>
        {value.pinnedHarnessSetups && value.pinnedHarnessSetups.length > 0 && (
          <p className="capsule-setup-pin">{pinnedSetupText(value.pinnedHarnessSetups)}</p>
        )}
      </header>
      {(isStarting(value) || value.startup || value.failureCode) && (
        <CapsuleStartup capsule={value} />
      )}
      {value.failure && !value.startup && !value.failureCode && (
        <p className="error-state" role="alert">{value.failure}</p>
      )}
      {deliveries.data?.items[0]?.state === "proposed" && (
        <section className="workspace-drawer" aria-label="Proposed Delivery">
          <DeliveryDecision capsuleId={value.id} deliveryId={deliveries.data.items[0].id} />
        </section>
      )}
      {shipOpen && (
        <section className="workspace-drawer" aria-label="Ship reviewed changes">
          <ShipPanel capsule={value} />
        </section>
      )}
      {(sessions.length > 0 || canCreateThread) && (
        <div className="session-tabs" role="tablist" aria-label="Sessions">
          {sessions.map((session) => (
            <button
              key={session.id}
              type="button"
              role="tab"
              id={`session-tab-${session.id}`}
              aria-selected={selected === session.id}
              aria-controls="session-panel"
              className={session.archived ? "archived" : undefined}
              onClick={() => selectSession(session.id)}
            >
              <ActivityDot group={session.group} />
              <HarnessMark harness={session.harness} />
              <span>{harnessName(session.harness)}</span>
              <small>{session.kind === "terminal" ? "Terminal" : session.label}</small>
            </button>
          ))}
          {canCreateThread && (
            <button
              type="button"
              role="tab"
              id={`session-tab-${NEW_SESSION}`}
              aria-selected={selected === NEW_SESSION}
              aria-controls="session-panel"
              aria-label="New session"
              title="New session"
              className="session-tab-new"
              onClick={() => selectSession(NEW_SESSION)}
            >
              <PlusIcon />
            </button>
          )}
        </div>
      )}
      <section
        id="session-panel"
        className="session-panel"
        role={selected ? "tabpanel" : undefined}
        aria-labelledby={selected ? `session-tab-${selected}` : undefined}
      >
        {selected === TERMINAL_SESSION ? (
          <TerminalSession capsule={value} runs={runs} capabilities={capabilities} />
        ) : selected === NEW_SESSION ? (
          <div className="session-new">
            <h2>Start a session</h2>
            <p className="muted-copy">
              Send the agent a task. The conversation runs in this Capsule and
              is kept encrypted.
            </p>
            <ThreadCreate
              capsule={value}
              onCreated={async (thread) => {
                await queryClient.invalidateQueries({ queryKey: ["threads", value.id] });
                navigate(sessionPath(value.id, thread.id), { replace: true });
              }}
            />
          </div>
        ) : selected ? (
          <ThreadDetail key={selected} threadId={selected} />
        ) : threads.isPending && structured ? (
          <WorkspaceSkeleton label="Loading sessions…" />
        ) : threads.isError ? (
          <ErrorState error={threads.error} />
        ) : isStarting(value) || awaitingThread ? (
          <WorkspaceSkeleton label="Starting the session" />
        ) : (
          <p className="tool-empty">
            {value.state === "Ready"
              ? "No sessions in this Capsule."
              : `Sessions can start once the Capsule is Ready. It is ${value.state}.`}
          </p>
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
        Changes are open in the Capsule tools pane. Comment on lines there and
        send the review to the agent, or inspect the exact Git state here before
        shipping.
      </p>
      <PendingReviewNotice capsuleId={capsuleId} />
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
        <Route index element={<Home />} />
        <Route path="settings/harnesses" element={<HarnessSetups />} />
        <Route path="capsules/:capsuleId" element={<CapsuleDetail />} />
        <Route path="capsules/:capsuleId/diff" element={<DiffPage />} />
        <Route path="projects/:projectId" element={<ProjectSettings />} />
        <Route path="board" element={<Navigate to="/ui/" replace />} />
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
