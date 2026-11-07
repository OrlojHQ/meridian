import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { Capsule, Thread, ThreadBlock } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import {
  followThreadBlocks,
  type ThreadStreamStatus,
} from "../api/threadEvents";
import { harnessName } from "../shell/capsuleActivity";
import { isActiveRunState, isBusyAdapterStatus, safeInline } from "./threadChat";
import { ThreadConversation } from "./ThreadConversation";
import { ThreadError } from "./ThreadError";
import { ActionsMenu, type ActionsMenuItem } from "./ui/ActionsMenu";
import { StateBadge } from "./ui/StateBadge";

export { sanitizeThreadText } from "./threadChat";

const MAX_BLOCKS = 400;

const streamLabels: Record<ThreadStreamStatus, string> = {
  connecting: "Connecting…",
  live: "Live",
  reconnecting: "Reconnecting…",
  closed: "Closed",
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

function ThreadSummary({ thread }: { thread: Thread }) {
  const updated = new Date(thread.updatedAt).toLocaleString();
  const supported = thread.structuredSupported !== false;
  const state =
    !supported
      ? "Unsupported"
      : thread.currentRunState === "Failed"
        ? "Error"
        : thread.state;
  return (
    <li className="thread-summary">
      <div>
        <Link to={`/ui/threads/${encodeURIComponent(thread.id)}`}>
          {safeInline(thread.harness)}
        </Link>
        <p className="mono">{safeInline(thread.id)}</p>
      </div>
      <StateBadge state={state} />
      <span>{safeInline(thread.currentRunState ?? "No active session")}</span>
      <span>{thread.messageCount} messages</span>
      <time dateTime={thread.updatedAt}>{updated}</time>
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
    return (
      <p className="loading" role="status">
        Loading Threads…
      </p>
    );
  }
  if (projects.isError) return <ThreadError error={projects.error} />;
  const failed =
    capsuleResults.find((result) => result.isError) ??
    threadResults.find((result) => result.isError);
  if (failed?.error) return <ThreadError error={failed.error} />;

  const threads = threadResults.flatMap((result) => result.data?.items ?? []);
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
        <div className="empty-state">
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
          {threads.map((thread) => (
            <ThreadSummary key={thread.id} thread={thread} />
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
}: { threadId?: string } = {}) {
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
  const [blocks, setBlocks] = useState<ThreadBlock[]>([]);
  const [status, setStatus] = useState<ThreadStreamStatus>("connecting");
  const [gap, setGap] = useState<{ expected: number; received: number }>();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const [confirmAction, setConfirmAction] = useState<"cancel" | "archive">();
  const [showDetails, setShowDetails] = useState(false);
  const [rawEvents, setRawEvents] = useState(false);

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
    );
    return () => controller.abort();
  }, [queryClient, refreshSummaries, replay.data, threadId]);

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
  const archive = useMutation({
    mutationFn: () =>
      api.archiveThread(threadId, threadQuery.data?.resourceVersion ?? 0),
    onSuccess: refresh,
    onError: async (error) => {
      if (normalizeAPIError(error).code === "conflict") await refresh();
    },
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
    return (
      <p className="loading" role="status">
        Loading Thread…
      </p>
    );
  }
  if (threadQuery.isError) return <ThreadError error={threadQuery.error} />;
  if (replay.isError) return <ThreadError error={replay.error} />;
  const thread = threadQuery.data;
  // A pack can list a native and a structured profile under one name.
  const named = profiles.data?.items.filter((item) => item.name === thread.harness) ?? [];
  const profile = named.find((item) => item.structured) ?? named[0];
  const activeSession = isActiveRunState(thread.currentRunState);
  const canCompose = thread.state === "active" && thread.structuredSupported !== false;
  const menu: ActionsMenuItem[] = [];
  if (activeSession) {
    menu.push({
      label: "Cancel session",
      disabled: session.isPending,
      onSelect: () => setConfirmAction("cancel"),
    });
  }
  if (thread.state !== "archived" && thread.state !== "deleted") {
    menu.push({
      label: "Archive",
      disabled: archive.isPending,
      onSelect: () => setConfirmAction("archive"),
    });
  }
  menu.push({
    label: "Open terminal",
    onSelect: () => navigate(`/ui/capsules/${encodeURIComponent(thread.capsuleId)}#terminal`),
  });
  if (thread.state !== "deleted") {
    menu.push({ label: "Crypto-shred", danger: true, onSelect: () => setConfirmDelete(true) });
  }

  return (
    <article className={`thread-detail thread-workspace thread-chat${embedded ? " embedded" : ""}`}>
      {!embedded && (
        <header className="workspace-view-header">
          <div>
            <p className="eyebrow">Thread</p>
            <h1>{safeInline(harnessName(thread.harness)) || "Agent"}</h1>
            <p className="view-description">
              Conversation in{" "}
              <Link to={`/ui/capsules/${encodeURIComponent(thread.capsuleId)}?session=${encodeURIComponent(thread.id)}`}>
                {owner.data ? safeInline(owner.data.name) : "its Capsule"}
              </Link>
              {thread.encryptedAtRest ? ". The transcript is encrypted at rest." : "."}
            </p>
          </div>
          <div className="workspace-header-meta">
            <StateBadge state={thread.state} />
          </div>
        </header>
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
        <span
          className={`connection ${status}`}
          role="status"
          title={`Cursor ${blocks.at(-1)?.messageSequence ?? replay.data.nextCursor}`}
        >
          {streamLabels[status]}
        </span>
        <label className="thread-raw-toggle">
          <input
            type="checkbox"
            checked={rawEvents}
            onChange={(event) => setRawEvents(event.target.checked)}
          />
          Raw events
        </label>
        {thread.state === "active" && !activeSession && thread.structuredSupported !== false && (
          <button disabled={session.isPending} onClick={() => session.mutate("start")}>
            Start session
          </button>
        )}
        {thread.state === "paused" && (
          <button disabled={session.isPending} onClick={() => session.mutate("resume")}>
            Resume session
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
      {(session.isError || archive.isError) && (
        <ThreadError error={session.error ?? archive.error} />
      )}
      {confirmAction && (
        <section
          className="danger-zone"
          role="alertdialog"
          aria-labelledby="thread-action-title"
        >
          <h2 id="thread-action-title">
            {confirmAction === "cancel"
              ? "Cancel the running session?"
              : "Archive this Thread?"}
          </h2>
          <p>
            {confirmAction === "cancel"
              ? "The agent will be asked to stop."
              : "The transcript remains encrypted and readable, but new messages are disabled."}
          </p>
          <div className="actions">
            <button
              className={confirmAction === "cancel" ? "danger" : undefined}
              disabled={session.isPending || archive.isPending}
              onClick={() => {
                const action = confirmAction;
                setConfirmAction(undefined);
                if (action === "cancel") session.mutate("cancel");
                else archive.mutate();
              }}
            >
              {confirmAction === "cancel" ? "Confirm cancel" : "Confirm archive"}
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
      <ThreadConversation
        thread={thread}
        blocks={blocks}
        raw={rawEvents}
        canCompose={canCompose}
        stopPending={session.isPending}
        onStop={activeSession ? () => session.mutate("cancel") : undefined}
        onNewSession={
          profiles.data?.items.some((item) => item.structured)
            ? () =>
                navigate(
                  `/ui/capsules/${encodeURIComponent(thread.capsuleId)}?session=new`,
                )
            : undefined
        }
        onRefresh={refresh}
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
