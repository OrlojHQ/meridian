import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { Capsule, Project, Thread, ThreadBlock } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import {
  followThreadBlocks,
  type ThreadStreamStatus,
} from "../api/threadEvents";
import {
  harnessName,
  relativeTime,
  sessionPath,
  threadSession,
  type CapsuleSession,
} from "../shell/capsuleActivity";
import { startFresh } from "../shell/startFresh";
import { EarlierSessions } from "./EarlierSessions";
import { RepositoryAccessNotice } from "./RepositoryAccess";
import { SessionEnded } from "./SessionEnded";
import { isRepositoryAccessFailure } from "./startup";
import {
  endedSessionState,
  isActiveRunState,
  isBusyAdapterStatus,
  lastOperatorPrompt,
  safeInline,
} from "./threadChat";
import { ThreadConversation } from "./ThreadConversation";
import { ThreadSubagents } from "./ThreadSubagents";
import { ThreadError } from "./ThreadError";
import { pinnableThread, setThreadPinned } from "../shell/pins";
import { ContextMeter } from "./ContextMeter";
import { contextDetail, contextMeterUsage, contextUsage } from "./contextUsage";
import { Menu as ActionsMenu, type MenuItem as ActionsMenuItem } from "./ui/Menu";
import { ListSkeleton } from "./ui/loading";
import { HarnessMark } from "./ui/HarnessMark";

export { sanitizeThreadText } from "./threadChat";

const MAX_BLOCKS = 400;

// The badge reports only trouble with the browser's event stream, when the
// transcript on screen may be out of date. A connected stream shows nothing:
// the agent's own state is the header chip and the transcript's activity. The
// first connect follows a fresh replay, so it shows nothing either.
const streamProblems: Partial<Record<ThreadStreamStatus, { label: string; title: string }>> = {
  reconnecting: {
    label: "Reconnecting…",
    title: "Live updates dropped. New output appears once they reconnect.",
  },
  closed: {
    label: "Disconnected",
    title: "Live updates stopped. Reload to see new output.",
  },
};

export function mergeThreadBlocks(
  current: ThreadBlock[],
  incoming: ThreadBlock[],
): ThreadBlock[] {
  const byId = new Map(current.map((block) => [block.id, block]));
  incoming.forEach((block) => byId.set(block.id, block));
  return [...byId.values()]
    .sort(
      (left, right) =>
        left.messageSequence - right.messageSequence ||
        left.sequence - right.sequence ||
        left.id.localeCompare(right.id),
    )
    .slice(-MAX_BLOCKS);
}

function threadStatus(thread: Thread): Pick<CapsuleSession, "group" | "label"> {
  if (thread.state === "deleted") return { group: "sealed", label: "Deleted" };
  if (thread.structuredSupported === false) return { group: "idle", label: "Unsupported" };
  return threadSession(thread);
}

function ThreadStatusChip({ thread }: { thread: Thread }) {
  const { group, label } = threadStatus(thread);
  return (
    <span className={`thread-status-chip thread-status-${group}`}>
      <span className={`state-dot activity-dot-${group}`} aria-hidden="true" />
      {label}
    </span>
  );
}

// Roles come from the plaintext coordination fields on the Thread; the lead
// is named by its Capsule.
function threadRole(thread: Thread, leadName?: string) {
  if (thread.parentThreadId) {
    return leadName ? `Subagent of ${safeInline(leadName)}` : "Subagent";
  }
  return thread.coordinationGrantId ? "Lead" : undefined;
}

function ThreadMeta({
  thread,
  project,
  role,
  lead,
  children,
}: {
  thread: Thread;
  project?: string;
  role?: string;
  lead?: { name: string; href: string };
  children?: ReactNode;
}) {
  return (
    <p className="thread-meta">
      <span>{safeInline(harnessName(thread.harness)) || "Agent"}</span>
      {project && <span>{safeInline(project)}</span>}
      {lead ? (
        <span className="thread-breadcrumb">
          Subagent of <Link to={lead.href}>{safeInline(lead.name)}</Link>
        </span>
      ) : (
        role && <span>{role}</span>
      )}
      {children}
    </p>
  );
}

type ThreadRow = {
  thread: Thread;
  title: string;
  project?: string;
  role?: string;
  attention: boolean;
};

// Rows are titled from plaintext Capsule and Project names. Transcripts are
// encrypted at rest, so no prompt is decrypted just to label a row.
function threadRows(
  threads: Thread[],
  capsules: Capsule[],
  projects: Project[],
): ThreadRow[] {
  const capsuleById = new Map(capsules.map((capsule) => [capsule.id, capsule]));
  const projectName = new Map(projects.map((project) => [project.id, project.name]));
  const threadById = new Map(threads.map((thread) => [thread.id, thread]));
  const perCapsule = new Map<string, Thread[]>();
  [...threads]
    .sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))
    .forEach((thread) =>
      perCapsule.set(thread.capsuleId, [...(perCapsule.get(thread.capsuleId) ?? []), thread]),
    );
  return threads
    .map((thread) => {
      const capsule = capsuleById.get(thread.capsuleId);
      const siblings = perCapsule.get(thread.capsuleId) ?? [];
      const base = capsule
        ? safeInline(capsule.name)
        : safeInline(harnessName(thread.harness)) || "Agent";
      const lead = thread.parentThreadId ? threadById.get(thread.parentThreadId) : undefined;
      return {
        thread,
        title:
          siblings.length > 1
            ? `${base} · session ${siblings.indexOf(thread) + 1}`
            : base,
        project: capsule && projectName.get(capsule.projectId),
        role: threadRole(thread, lead && capsuleById.get(lead.capsuleId)?.name),
        attention: threadStatus(thread).group === "attention",
      };
    })
    .sort(
      (a, b) =>
        Number(b.attention) - Number(a.attention) ||
        Date.parse(b.thread.updatedAt) - Date.parse(a.thread.updatedAt),
    );
}

function ThreadSummary({ row }: { row: ThreadRow }) {
  const { thread } = row;
  return (
    <li className="thread-summary">
      <HarnessMark harness={thread.harness} size="md" />
      <div className="thread-summary-main">
        <Link to={`/ui/threads/${encodeURIComponent(thread.id)}`}>{row.title}</Link>
        <ThreadMeta thread={thread} project={row.project} role={row.role} />
      </div>
      <ThreadStatusChip thread={thread} />
      <span className="thread-summary-count">
        {thread.messageCount === 1 ? "1 message" : `${thread.messageCount} messages`}
      </span>
      <time
        dateTime={thread.updatedAt}
        title={new Date(thread.updatedAt).toLocaleString()}
      >
        {relativeTime(thread.updatedAt)}
      </time>
    </li>
  );
}

export function ThreadFleet() {
  const projects = useQuery(queries.projects());
  const projectItems = projects.data?.items ?? [];
  const capsuleResults = useQueries({
    queries: projectItems.map((project) => queries.capsules(project.id)),
  });
  const capsules = capsuleResults.flatMap((result) => result.data?.items ?? []);
  const threadResults = useQueries({
    queries: capsules.map((capsule) => queries.threads(capsule.id)),
  });

  if (
    projects.isPending ||
    capsuleResults.some((result) => result.isPending) ||
    threadResults.some((result) => result.isPending)
  ) {
    return <ListSkeleton label="Loading Threads…" />;
  }
  if (projects.isError) return <ThreadError error={projects.error} />;
  const failed =
    capsuleResults.find((result) => result.isError) ??
    threadResults.find((result) => result.isError);
  if (failed?.error) return <ThreadError error={failed.error} />;

  const threads = threadResults.flatMap((result) => result.data?.items ?? []);
  const rows = threadRows(threads, capsules, projectItems);
  return (
    <section className="thread-fleet">
      <header className="workspace-view-header">
        <div>
          <p className="eyebrow">Activity</p>
          <h1>Structured Threads</h1>
          <p className="view-description">
            Retained conversations across every active Capsule.
          </p>
        </div>
        <span className="count-label">{threads.length} retained</span>
      </header>
      {threads.length === 0 ? (
        <div className="empty-state ui-card">
          <h2>No Threads yet</h2>
          <p>Select a Capsule to create a structured Thread and send its first task.</p>
          {capsules[0] && (
            <Link className="button-link" to={`/ui/capsules/${capsules[0].id}`}>
              Open a Capsule
            </Link>
          )}
        </div>
      ) : (
        <ul className="thread-list" aria-label="Retained Threads">
          {rows.map((row) => (
            <ThreadSummary key={row.thread.id} row={row} />
          ))}
        </ul>
      )}
    </section>
  );
}

export function ThreadCreate({
  capsule,
  onCreated,
}: {
  capsule: Pick<Capsule, "id" | "name">;
  onCreated?: (thread: Thread) => void;
}) {
  const profiles = useQuery(queries.harnessProfiles(capsule.id));
  const [harness, setHarness] = useState("");
  const [message, setMessage] = useState("");
  const [start, setStart] = useState(true);
  const mutation = useMutation({
    mutationFn: () =>
      api.createThread(
        capsule.id,
        harness,
        message === "" ? undefined : message,
        start,
      ),
    onSuccess: (result) => onCreated?.(result.thread),
  });
  const structured = profiles.data?.items.filter((profile) => profile.structured) ?? [];
  const fallback = profiles.data?.items.filter((profile) => profile.pty) ?? [];

  useEffect(() => {
    if (!harness && structured[0]) setHarness(structured[0].name);
  }, [harness, structured]);

  if (profiles.isPending) return <p role="status">Loading harness profiles…</p>;
  if (profiles.isError) {
    const error = normalizeAPIError(profiles.error);
    if (error.code === "unsupported" || error.status === 422) {
      return (
        <p className="notice" role="status">
          Structured Threads are unsupported for this Capsule. Use native Runs
          and their separate Terminal route.
        </p>
      );
    }
    return <ThreadError error={profiles.error} />;
  }
  if (structured.length === 0) {
    return (
      <p className="notice" role="status">
        Structured Threads are unsupported for this Capsule.
        {fallback.length > 0
          ? " Native PTY profiles remain available through Runs and the Terminal tab."
          : " No native PTY fallback profile is currently available."}
      </p>
    );
  }
  return (
    <form
      className="thread-create"
      onSubmit={(event) => {
        event.preventDefault();
        if (harness && (!start || message !== "")) mutation.mutate();
      }}
    >
      <label>
        Structured harness
        <select value={harness} onChange={(event) => setHarness(event.target.value)}>
          {structured.map((profile) => (
            <option key={profile.name} value={profile.name}>
              {safeInline(profile.name)} ·{" "}
              {safeInline(profile.adapterKind ?? "adapter")} ·{" "}
              {safeInline(profile.protocol)}
            </option>
          ))}
        </select>
      </label>
      <label>
        First task (optional)
        <textarea
          value={message}
          maxLength={128 * 1024}
          rows={4}
          onChange={(event) => setMessage(event.target.value)}
        />
      </label>
      <label className="check-label">
        <input
          type="checkbox"
          checked={start}
          onChange={(event) => setStart(event.target.checked)}
        />
        Start the session atomically
      </label>
      <button type="submit" disabled={mutation.isPending || !harness || (start && !message)}>
        Create Thread{start ? " and start" : ""}
      </button>
      {start && !message && (
        <p className="sensitive-note">A first task is required when starting immediately.</p>
      )}
      {mutation.isError && <ThreadError error={mutation.error} />}
    </form>
  );
}

// ThreadDetail renders the /ui/threads/:threadId route, or one session tab
// inside a Capsule workspace when embedded with an explicit threadId.
export function ThreadDetail({
  threadId: embeddedThreadId,
  unfold,
}: {
  threadId?: string;
  // An earlier session of this Capsule to show unfolded above the transcript.
  unfold?: string;
} = {}) {
  const params = useParams();
  const threadId = embeddedThreadId ?? params.threadId ?? "";
  const embedded = embeddedThreadId !== undefined;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const threadQuery = useQuery(queries.thread(threadId));
  const replay = useQuery(queries.threadBlocks(threadId));
  const profiles = useQuery(
    queries.harnessProfiles(threadQuery.data?.capsuleId ?? ""),
  );
  // The standalone page names the owning Capsule. The shell already follows
  // this Capsule for its tools pane, so this shares that query.
  const owner = useQuery({
    ...queries.capsule(threadQuery.data?.capsuleId ?? ""),
    enabled: !embedded && Boolean(threadQuery.data?.capsuleId),
  });
  const projects = useQuery({ ...queries.projects(), enabled: !embedded });
  const parentThreadId = threadQuery.data?.parentThreadId ?? "";
  const lead = useQuery({
    ...queries.thread(parentThreadId),
    enabled: !embedded && Boolean(parentThreadId),
  });
  const leadCapsule = useQuery({
    ...queries.capsule(lead.data?.capsuleId ?? ""),
    enabled: !embedded && Boolean(lead.data?.capsuleId),
  });
  const [blocks, setBlocks] = useState<ThreadBlock[]>([]);
  const [status, setStatus] = useState<ThreadStreamStatus>("connecting");
  const [gap, setGap] = useState<{ expected: number; received: number }>();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const [confirmAction, setConfirmAction] = useState<"cancel" | "archive" | "fresh">();
  const [showDetails, setShowDetails] = useState(false);
  const [rawEvents, setRawEvents] = useState(false);
  const ended = threadQuery.data ? endedSessionState(threadQuery.data) : undefined;
  const endedRef = useRef(ended !== undefined);
  endedRef.current = ended !== undefined;
  // Following stops once an ended session's stream closes; a new session
  // follows again.
  const sessionLive = ended === undefined;
  // A terminal Run no longer changes, so its failure text is read once.
  const endedRun = useQuery({
    ...queries.run(threadQuery.data?.currentRunId ?? ""),
    enabled: ended !== undefined && Boolean(threadQuery.data?.currentRunId),
    refetchInterval: false,
    staleTime: Infinity,
  });
  const lastPrompt = useMemo(
    () => (ended ? lastOperatorPrompt(blocks) : undefined),
    [blocks, ended],
  );

  const capsuleId = threadQuery.data?.capsuleId;
  const refreshSummaries = useCallback(
    () => queryClient.invalidateQueries({ queryKey: ["threads", capsuleId] }),
    [capsuleId, queryClient],
  );
  const refresh = useCallback(async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["thread", threadId] }),
      queryClient.invalidateQueries({ queryKey: ["thread-blocks", threadId] }),
      refreshSummaries(),
    ]);
  }, [queryClient, refreshSummaries, threadId]);

  useEffect(() => {
    if (!replay.data) return;
    setBlocks(mergeThreadBlocks([], replay.data.items));
    setGap(undefined);
    const controller = new AbortController();
    let busy = replay.data.items.reduce<boolean | undefined>(
      (current, block) =>
        block.event?.type === "status" || block.event?.type === "end"
          ? block.event.type === "status" && isBusyAdapterStatus(block.event.status)
          : current,
      undefined,
    );
    void followThreadBlocks(
      threadId,
      replay.data.nextCursor,
      controller.signal,
      (block) => {
        setBlocks((current) => mergeThreadBlocks(current, [block]));
        const event = block.event;
        if (event?.type === "status" || event?.type === "end") {
          const next = event.type === "status" && isBusyAdapterStatus(event.status);
          if (busy !== undefined && next !== busy) void refreshSummaries();
          busy = next;
        }
        queryClient.setQueryData(
          ["thread-blocks", threadId],
          (current: typeof replay.data) =>
            current
              ? {
                  ...current,
                  items: mergeThreadBlocks(current.items, [block]),
                  nextCursor: Math.max(current.nextCursor, block.messageSequence),
                }
              : current,
        );
      },
      setStatus,
      (expected, received) => setGap({ expected, received }),
      () => endedRef.current,
    );
    return () => controller.abort();
  }, [queryClient, refreshSummaries, replay.data, threadId, sessionLive]);

  const session = useMutation({
    mutationFn: (action: "start" | "resume" | "cancel") => {
      if (!threadQuery.data) throw new Error("Thread is not loaded");
      return api.threadSession(
        action,
        threadQuery.data.id,
        threadQuery.data.resourceVersion,
      );
    },
    onSuccess: refresh,
    onError: async (error) => {
      if (normalizeAPIError(error).code === "conflict") await refresh();
    },
  });
  const capsuleThreads = useQuery(queries.threads(threadQuery.data?.capsuleId ?? ""));
  // Shared with the Capsule page; says whether this is a CLI Capsule.
  const home = useQuery(queries.capsule(threadQuery.data?.capsuleId ?? ""));
  const fresh = useMutation({
    mutationFn: () => {
      if (!threadQuery.data) throw new Error("Thread is not loaded");
      return startFresh(threadQuery.data);
    },
    onSuccess: async (next) => {
      await queryClient.invalidateQueries({ queryKey: ["threads", next.capsuleId] });
      navigate(sessionPath(next.capsuleId, next.id));
    },
    onError: refresh,
  });
  const archive = useMutation({
    mutationFn: () =>
      api.archiveThread(threadId, threadQuery.data?.resourceVersion ?? 0),
    onSuccess: refresh,
    onError: async (error) => {
      if (normalizeAPIError(error).code === "conflict") await refresh();
    },
  });
  // A pin only moves the Capsule up in the sidebar (ADR 0059).
  const pin = useMutation({
    mutationFn: () => {
      if (!threadQuery.data) throw new Error("Thread is not loaded");
      return setThreadPinned(threadQuery.data, !threadQuery.data.pinnedAt);
    },
    onSettled: refresh,
  });
  const deletion = useMutation({
    mutationFn: () =>
      api.deleteThread(threadId, threadQuery.data?.resourceVersion ?? 0),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["threads"] });
      navigate(
        embedded
          ? `/ui/capsules/${encodeURIComponent(threadQuery.data?.capsuleId ?? "")}`
          : "/ui/threads",
      );
    },
    onError: async (error) => {
      if (normalizeAPIError(error).code === "conflict") await refresh();
    },
  });

  if (threadQuery.isPending || replay.isPending) {
    return <ListSkeleton label="Loading Thread…" />;
  }
  if (threadQuery.isError) return <ThreadError error={threadQuery.error} />;
  if (replay.isError) return <ThreadError error={replay.error} />;
  const thread = threadQuery.data;
  // A pack can list a native and a structured profile under one name.
  const named = profiles.data?.items.filter((item) => item.name === thread.harness) ?? [];
  const profile = named.find((item) => item.structured) ?? named[0];
  const activeSession = isActiveRunState(thread.currentRunState);
  // Context fill: a meter on plain Threads from half full, and always in the
  // details when the harness reports it.
  const contextMeter = contextMeterUsage(thread);
  const contextFill = contextUsage(thread);
  const canCompose = thread.state === "active" && thread.structuredSupported !== false;
  // Start fresh replaces this Thread's session with a new Thread in the same
  // Thread Capsule. A CLI Capsule runs its terminal and never starts a Thread.
  // A lead and its subagents keep their coordination, so they do not offer it.
  const coordinated = Boolean(thread.coordinationGrantId || thread.parentThreadId);
  const threadCapsule = home.data !== undefined && !home.data.harness;
  const newSession =
    !coordinated &&
    threadCapsule &&
    thread.state !== "deleted" &&
    thread.state !== "archived" &&
    profiles.data?.items.some((item) => item.structured)
      ? () =>
          activeSession
            ? setConfirmAction("fresh")
            : fresh.mutate()
      : undefined;
  const menu: ActionsMenuItem[] = [];
  if (newSession) {
    menu.push({ label: "Start fresh", disabled: fresh.isPending, onSelect: newSession });
  }
  if (activeSession) {
    menu.push({
      label: "Cancel session",
      disabled: session.isPending,
      onSelect: () => setConfirmAction("cancel"),
    });
  }
  // Only the Capsule's current, live Thread of a main Capsule can be pinned.
  const pinTarget =
    home.data && capsuleThreads.data
      ? pinnableThread({ capsule: home.data, threads: capsuleThreads.data.items })
      : undefined;
  if (pinTarget?.id === thread.id) {
    menu.push({
      label: thread.pinnedAt ? "Unpin" : "Pin",
      disabled: pin.isPending,
      onSelect: () => pin.mutate(),
    });
  }
  if (thread.state !== "archived" && thread.state !== "deleted") {
    menu.push({
      label: "Archive",
      disabled: archive.isPending,
      onSelect: () => setConfirmAction("archive"),
    });
  }
  // A structured-only pack, such as DeepSeek Harness, has no native profile
  // and so never starts a Run terminal to attach to.
  if (profiles.data?.items.some((item) => !item.structured)) {
    menu.push({
      label: "Open terminal",
      onSelect: () => navigate(`/ui/capsules/${encodeURIComponent(thread.capsuleId)}#terminal`),
    });
  }
  if (thread.state !== "deleted") {
    menu.push({ label: "Crypto-shred", danger: true, onSelect: () => setConfirmDelete(true) });
  }

  return (
    <article className={`thread-detail thread-workspace thread-chat${embedded ? " embedded" : ""}`}>
      {!embedded && (
        <header className="workspace-view-header">
          <div>
            <p className="eyebrow">Thread</p>
            <h1>
              {owner.data
                ? safeInline(owner.data.name)
                : safeInline(harnessName(thread.harness)) || "Agent"}
            </h1>
            <ThreadMeta
              thread={thread}
              project={
                projects.data?.items.find((item) => item.id === owner.data?.projectId)?.name
              }
              role={threadRole(thread, leadCapsule.data?.name)}
              lead={
                thread.parentThreadId && lead.data
                  ? {
                      name: leadCapsule.data?.name ?? "lead",
                      href: `/ui/capsules/${encodeURIComponent(lead.data.capsuleId)}?session=${encodeURIComponent(lead.data.id)}`,
                    }
                  : undefined
              }
            >
              <Link to={`/ui/capsules/${encodeURIComponent(thread.capsuleId)}?session=${encodeURIComponent(thread.id)}`}>
                Open in Capsule
              </Link>
            </ThreadMeta>
          </div>
          <div className="workspace-header-meta">
            <ThreadStatusChip thread={thread} />
          </div>
        </header>
      )}
      {!embedded && owner.data && isRepositoryAccessFailure(owner.data) && (
        <RepositoryAccessNotice message={owner.data.failure} capsule={owner.data} />
      )}
      <div className="thread-toolbar">
        <button
          type="button"
          className="thread-toolbar-toggle"
          aria-expanded={showDetails}
          onClick={() => setShowDetails((value) => !value)}
        >
          Thread details
        </button>
        {contextMeter && <ContextMeter usage={contextMeter} harness={thread.harness} />}
        {ended ? (
          <span className="connection closed ended" role="status">
            Session ended
          </span>
        ) : (
          streamProblems[status] && (
            <span
              className={`connection ${status}`}
              role="status"
              title={streamProblems[status].title}
            >
              {streamProblems[status].label}
            </span>
          )
        )}
        <label className="thread-raw-toggle">
          <input
            type="checkbox"
            checked={rawEvents}
            onChange={(event) => setRawEvents(event.target.checked)}
          />
          Raw events
        </label>
        {thread.state === "active" && !activeSession && !ended && thread.structuredSupported !== false && (
          <button disabled={session.isPending} onClick={() => session.mutate("start")}>
            Start session
          </button>
        )}
        {thread.state === "paused" && (
          <button disabled={session.isPending} onClick={() => session.mutate("resume")}>
            Resume session
          </button>
        )}
        {thread.state === "paused" && newSession && (
          <button disabled={fresh.isPending} onClick={newSession}>
            Start fresh
          </button>
        )}
        <ActionsMenu label="Thread actions" items={menu} />
      </div>
      {showDetails && (
        <dl className="facts thread-facts">
          <div><dt>Agent</dt><dd>{safeInline(harnessName(thread.harness))}</dd></div>
          <div><dt>Adapter</dt><dd>{safeInline(profile?.adapterKind ?? (profile?.pty ? "Native PTY" : "Unknown"))}</dd></div>
          <div><dt>Protocol</dt><dd>{safeInline(thread.protocol ?? "Unsupported")}</dd></div>
          <div><dt>Session</dt><dd>{safeInline(thread.currentRunState ?? "Idle")}</dd></div>
          <div><dt>Messages</dt><dd>{thread.messageCount}</dd></div>
          {contextFill && <div><dt>Context</dt><dd>{contextDetail(contextFill)}</dd></div>}
          <div><dt>Encryption</dt><dd>{thread.encryptedAtRest ? "Encrypted at rest" : "Unavailable"}</dd></div>
          <div><dt>Thread ID</dt><dd className="mono">{safeInline(thread.id)}</dd></div>
        </dl>
      )}
      {thread.structuredSupported === false && (
        <p className="notice" role="status">
          This agent does not support written-task sessions. Use its terminal
          instead.
        </p>
      )}
      {(session.isError || archive.isError || fresh.isError || pin.isError) && (
        <ThreadError error={session.error ?? archive.error ?? fresh.error ?? pin.error} />
      )}
      {fresh.isPending && <p className="notice" role="status">Starting a fresh session…</p>}
      {confirmAction && (
        <section
          className="danger-zone"
          role="alertdialog"
          aria-labelledby="thread-action-title"
        >
          <h2 id="thread-action-title">
            {confirmAction === "cancel"
              ? "Cancel the running session?"
              : confirmAction === "fresh"
                ? "Stop this session and start fresh?"
                : "Archive this Thread?"}
          </h2>
          <p>
            {confirmAction === "cancel"
              ? "The agent will be asked to stop."
              : confirmAction === "fresh"
                ? "The agent is asked to stop, then a new session starts in this Capsule. Files and changes stay. The conversation so far stays readable above, but the new session starts without it."
                : "The transcript remains encrypted and readable, but new messages are disabled."}
          </p>
          <div className="actions">
            <button
              className={confirmAction === "archive" ? undefined : "danger"}
              disabled={session.isPending || archive.isPending || fresh.isPending}
              onClick={() => {
                const action = confirmAction;
                setConfirmAction(undefined);
                if (action === "cancel") session.mutate("cancel");
                else if (action === "fresh") fresh.mutate();
                else archive.mutate();
              }}
            >
              {confirmAction === "cancel"
                ? "Confirm cancel"
                : confirmAction === "fresh"
                  ? "Stop and start fresh"
                  : "Confirm archive"}
            </button>
            <button type="button" onClick={() => setConfirmAction(undefined)}>
              Keep Thread
            </button>
          </div>
        </section>
      )}
      {confirmDelete && (
        <section className="danger-zone" role="alertdialog" aria-labelledby="delete-title">
          <h2 id="delete-title">Permanently crypto-shred this transcript?</h2>
          <p>
            This irreversibly removes the wrapped data key. Type{" "}
            <strong>crypto-shred</strong> to continue.
          </p>
          <label>
            Confirmation
            <input value={confirmation} onChange={(event) => setConfirmation(event.target.value)} />
          </label>
          <div className="actions">
            <button
              className="danger"
              disabled={confirmation !== "crypto-shred" || deletion.isPending}
              onClick={() => deletion.mutate()}
            >
              Delete key permanently
            </button>
            <button type="button" onClick={() => setConfirmDelete(false)}>Cancel</button>
          </div>
          {deletion.isError && <ThreadError error={deletion.error} />}
        </section>
      )}
      <ThreadSubagents thread={thread} />
      <ThreadConversation
        thread={thread}
        blocks={blocks}
        raw={rawEvents}
        canCompose={canCompose}
        stopPending={session.isPending}
        onStop={activeSession ? () => session.mutate("cancel") : undefined}
        onNewSession={newSession}
        onRefresh={refresh}
        earlier={
          <EarlierSessions
            thread={thread}
            threads={capsuleThreads.data?.items ?? []}
            unfold={unfold}
          />
        }
        endedNotice={
          ended && (
            <SessionEnded
              state={ended}
              run={endedRun.data?.id === thread.currentRunId ? endedRun.data : undefined}
              lastPrompt={lastPrompt}
              restartPending={session.isPending}
              onRestart={
                thread.structuredSupported !== false
                  ? () => session.mutate("start")
                  : undefined
              }
              onNewSession={newSession}
            />
          )
        }
        banners={
          <>
            {(gap || replay.data.gap) && (
              <p className="warning" role="alert">
                Replay gap detected. Earlier blocks are unavailable; continue only
                after reviewing the visible sequence.
                {gap ? ` Expected ${gap.expected}, received ${gap.received}.` : ""}
              </p>
            )}
            {replay.data.more && (
              <p className="notice">Transcript rendering is bounded to {MAX_BLOCKS} blocks.</p>
            )}
          </>
        }
      />
    </article>
  );
}
