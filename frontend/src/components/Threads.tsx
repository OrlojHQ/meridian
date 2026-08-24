import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  type KeyboardEvent,
  useEffect,
  useMemo,
  useState,
} from "react";
import { Link, useNavigate, useParams } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type {
  Capsule,
  Thread,
  ThreadAdapterEvent,
  ThreadBlock,
} from "../api/generated/types.gen";
import { queries } from "../api/queries";
import {
  followThreadBlocks,
  type ThreadStreamStatus,
} from "../api/threadEvents";

const MAX_BLOCKS = 400;
const MAX_CONTENT = 64 * 1024;

export const sanitizeThreadText = (value = "") =>
  value.replace(/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/g, "");

const bounded = (value = "") => {
  const safe = sanitizeThreadText(value);
  return safe.length > MAX_CONTENT
    ? `${safe.slice(0, MAX_CONTENT)}\n… content truncated …`
    : safe;
};

const safeInline = (value = "") =>
  sanitizeThreadText(value).replace(/[\n\t]+/g, " ");

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

function finalMessageIds(blocks: ThreadBlock[]) {
  return new Set(
    blocks
      .filter((block) => block.event?.type === "assistant_message")
      .map((block) => block.event?.messageId ?? block.messageId),
  );
}

function StateBadge({ state }: { state: string }) {
  return (
    <span className={`badge state-${state.toLowerCase()}`}>
      {safeInline(state)}
    </span>
  );
}

function ThreadError({ error }: { error: unknown }) {
  const normalized = normalizeAPIError(error);
  if (normalized.code === "transcript_locked" || normalized.status === 423) {
    return (
      <section className="locked-state" role="alert">
        <h2>Encrypted transcript locked</h2>
        <p>
          The installation key is missing or does not match this Thread. Ask an
          operator to restore the correct key, then retry. Ciphertext and key
          details are intentionally hidden.
        </p>
      </section>
    );
  }
  if (normalized.code === "transcript_corrupt") {
    return (
      <section className="error-state" role="alert">
        <h2>Encrypted transcript corrupt</h2>
        <p>
          Stop using this Thread and restore from a trusted backup. Ciphertext
          and key details are intentionally hidden.
        </p>
      </section>
    );
  }
  return (
    <section className="error-state" role="alert">
      <h2>Thread request failed</h2>
      <p>{bounded(normalized.message)}</p>
    </section>
  );
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
    <section>
      <div className="page-heading">
        <div>
          <p className="eyebrow">Structured agent fleet</p>
          <h1>Threads</h1>
        </div>
        <span>{threads.length} retained</span>
      </div>
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

type PendingRequest = {
  block: ThreadBlock;
  event: ThreadAdapterEvent;
};

function pendingRequest(blocks: ThreadBlock[]): PendingRequest | undefined {
  const permissions: PendingRequest[] = [];
  const inputs: PendingRequest[] = [];
  blocks.forEach((block) => {
    const event = block.event;
    if (!event) return;
    if (event.type === "permission_request") permissions.push({ block, event });
    else if (event.type === "input_request") inputs.push({ block, event });
    else if (event.type === "permission_response") permissions.pop();
    else if (event.type === "input_response") inputs.pop();
  });
  const permission = permissions.at(-1);
  const input = inputs.at(-1);
  if (!permission) return input;
  if (!input) return permission;
  return permission.block.messageSequence > input.block.messageSequence
    ? permission
    : input;
}

function BlockTimeline({ blocks }: { blocks: ThreadBlock[] }) {
  const finals = useMemo(() => finalMessageIds(blocks), [blocks]);
  const displayed = useMemo(() => {
    const output: ThreadBlock[] = [];
    const deltaIndex = new Map<string, number>();
    for (const block of blocks) {
      if (block.event?.type !== "assistant_delta") {
        output.push(block);
        continue;
      }
      const key = block.event.messageId ?? block.messageId;
      if (finals.has(key)) continue;
      const index = deltaIndex.get(key);
      if (index === undefined) {
        deltaIndex.set(key, output.length);
        output.push({
          ...block,
          content: block.event.summary ?? block.content,
        });
      } else {
        const current = output[index];
        output[index] = {
          ...current,
          content: bounded(
            `${current.content ?? ""}${block.event.summary ?? block.content ?? ""}`,
          ),
        };
      }
    }
    return output;
  }, [blocks, finals]);
  if (displayed.length === 0) return <p className="empty-inline">No messages yet.</p>;
  return (
    <ol className="thread-timeline" aria-label="Thread transcript" aria-live="polite">
      {displayed.map((block) => {
        const event = block.event;
        const content = bounded(block.content);
        const type = event?.type;
        let heading = `${safeInline(block.role)} · ${safeInline(block.messageKind)}`;
        let text = content;
        if (
          !event &&
          !["text", "json", "tool_call", "tool_result", "error"].includes(block.kind)
        ) {
          heading = `Unknown block · ${safeInline(block.kind)}`;
        } else if (type === "assistant_delta") heading = "Assistant · streaming";
        else if (type === "assistant_message") heading = "Assistant";
        else if (type === "tool_start") {
          heading = `Tool started · ${safeInline(event?.toolName ?? "unknown tool")}`;
          text = bounded(event?.summary);
        } else if (type === "tool_result") {
          heading = "Tool result";
          text = bounded(event?.result);
        } else if (type === "status") {
          heading = "Session status";
          text = safeInline(event?.status ?? "unknown");
        } else if (type === "error") {
          heading = `Adapter error · ${safeInline(event?.code ?? "unknown")}`;
          text = bounded(event?.reason);
        } else if (type === "permission_request") {
          heading = `Permission request · ${safeInline(event?.permission?.kind ?? "unknown")}`;
          text = bounded(event?.permission?.summary);
        } else if (type === "input_request") {
          heading = "Input request";
          text = bounded(event?.input?.prompt);
        } else if (
          type &&
          ![
            "permission_response",
            "input_response",
            "gap",
            "end",
          ].includes(type)
        ) {
          heading = `Unknown block event · ${safeInline(type)}`;
          text = content || "No displayable text.";
        }
        return (
          <li key={block.id} className={`thread-block role-${block.role}`}>
            <div className="thread-block-heading">
              <strong>{heading}</strong>
              <span className="mono">
                #{block.messageSequence}.{block.sequence}
              </span>
            </div>
            {text && <pre className="thread-content">{text}</pre>}
          </li>
        );
      })}
    </ol>
  );
}

function PermissionControls({
  thread,
  request,
  onSuccess,
  onConflict,
}: {
  thread: Thread;
  request: PendingRequest;
  onSuccess: () => void;
  onConflict: () => Promise<void>;
}) {
  const [input, setInput] = useState("");
  const mutation = useMutation({
    mutationFn: (value: { choice?: string; input?: string }) =>
      api.respondThread(thread.id, {
        expectedResourceVersion: thread.resourceVersion,
        responseTo: request.event.messageId ?? request.block.messageId,
        ...value,
      }),
    onSuccess,
    onError: async (error) => {
      if (normalizeAPIError(error).code === "conflict") await onConflict();
    },
  });
  const permission = request.event.permission;
  const isInput = request.event.type === "input_request";
  return (
    <section className="permission-panel" aria-labelledby="permission-title">
      <h2 id="permission-title">
        {isInput ? "Input required" : "Permission required"}
      </h2>
      <p>{bounded(isInput ? request.event.input?.prompt : permission?.summary)}</p>
      {isInput ? (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            mutation.mutate({ input });
          }}
        >
          <label>
            Response
            <input
              type={request.event.input?.secret ? "password" : "text"}
              value={input}
              onChange={(event) => setInput(event.target.value)}
            />
          </label>
          <button disabled={mutation.isPending || input === ""}>Send response</button>
        </form>
      ) : (
        <div className="actions">
          {(permission?.options?.length ? permission.options : ["allow", "deny"]).map(
            (choice) => (
              <button
                key={choice}
                type="button"
                disabled={mutation.isPending}
                onClick={() => mutation.mutate({ choice })}
              >
                {safeInline(choice)}
              </button>
            ),
          )}
        </div>
      )}
      {mutation.isError && <ThreadError error={mutation.error} />}
    </section>
  );
}

function ThreadComposer({
  thread,
  onSuccess,
  onConflict,
}: {
  thread: Thread;
  onSuccess: () => void;
  onConflict: () => Promise<void>;
}) {
  const [message, setMessage] = useState("");
  const mutation = useMutation({
    mutationFn: () =>
      api.sendThreadMessage(thread.id, thread.resourceVersion, message),
    onSuccess: () => {
      setMessage("");
      onSuccess();
    },
    onError: async (error) => {
      if (normalizeAPIError(error).code === "conflict") await onConflict();
    },
  });
  const submit = () => {
    if (message.trim() && !mutation.isPending) mutation.mutate();
  };
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      submit();
    }
  };
  return (
    <section className="thread-composer" aria-label="Thread message composer">
      <label htmlFor="thread-message">Message</label>
      <textarea
        id="thread-message"
        rows={4}
        maxLength={128 * 1024}
        value={message}
        onChange={(event) => setMessage(event.target.value)}
        onKeyDown={onKeyDown}
        placeholder="Enter sends · Shift+Enter adds a newline"
      />
      <button type="button" disabled={!message.trim() || mutation.isPending} onClick={submit}>
        Send
      </button>
      {mutation.isError && <ThreadError error={mutation.error} />}
    </section>
  );
}

export function ThreadDetail() {
  const { threadId = "" } = useParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const threadQuery = useQuery(queries.thread(threadId));
  const replay = useQuery(queries.threadBlocks(threadId));
  const profiles = useQuery(
    queries.harnessProfiles(threadQuery.data?.capsuleId ?? ""),
  );
  const [blocks, setBlocks] = useState<ThreadBlock[]>([]);
  const [status, setStatus] = useState<ThreadStreamStatus>("connecting");
  const [gap, setGap] = useState<{ expected: number; received: number }>();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [confirmation, setConfirmation] = useState("");
  const [confirmAction, setConfirmAction] = useState<"cancel" | "archive">();

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["thread", threadId] }),
      queryClient.invalidateQueries({ queryKey: ["thread-blocks", threadId] }),
    ]);
  };

  useEffect(() => {
    if (!replay.data) return;
    setBlocks(mergeThreadBlocks([], replay.data.items));
    setGap(undefined);
    const controller = new AbortController();
    void followThreadBlocks(
      threadId,
      replay.data.nextCursor,
      controller.signal,
      (block) => {
        setBlocks((current) => mergeThreadBlocks(current, [block]));
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
  }, [queryClient, replay.data, threadId]);

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
      navigate("/ui/threads");
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
  const profile = profiles.data?.items.find((item) => item.name === thread.harness);
  const pending = pendingRequest(blocks);
  const activeSession = ["Queued", "Starting", "Running", "Cancelling"].includes(
    thread.currentRunState ?? "",
  );
  const canCompose = thread.state === "active" && thread.structuredSupported !== false;

  return (
    <article className="thread-detail">
      <div className="page-heading">
        <div>
          <p className="eyebrow">Structured Thread</p>
          <h1>{safeInline(thread.harness)}</h1>
          <p className="mono">{safeInline(thread.id)}</p>
        </div>
        <StateBadge state={thread.state} />
      </div>
      <dl className="facts panel">
        <div><dt>Capsule</dt><dd><Link to={`/ui/capsules/${encodeURIComponent(thread.capsuleId)}`}>{safeInline(thread.capsuleId)}</Link></dd></div>
        <div><dt>Harness</dt><dd>{safeInline(thread.harness)}</dd></div>
        <div><dt>Adapter</dt><dd>{safeInline(profile?.adapterKind ?? (profile?.pty ? "Native PTY" : "Unknown"))}</dd></div>
        <div><dt>Protocol</dt><dd>{safeInline(thread.protocol ?? "Unsupported")}</dd></div>
        <div><dt>Session</dt><dd>{safeInline(thread.currentRunState ?? "Idle")}</dd></div>
        <div><dt>Run</dt><dd>{safeInline(thread.currentRunId ?? "None")}</dd></div>
        <div><dt>Messages</dt><dd>{thread.messageCount}</dd></div>
        <div><dt>Encryption</dt><dd>{thread.encryptedAtRest ? "Encrypted at rest" : "Unavailable"}</dd></div>
        <div><dt>Latest activity</dt><dd>{new Date(thread.updatedAt).toLocaleString()}</dd></div>
        <div><dt>Resource version</dt><dd>{thread.resourceVersion}</dd></div>
      </dl>
      {thread.structuredSupported === false && (
        <p className="notice" role="status">
          This profile does not support structured sessions. Use a native Run and
          its separate Terminal tab.
        </p>
      )}
      <div className="actions" aria-label="Thread actions">
        {thread.state === "active" && !activeSession && (
          <button disabled={session.isPending} onClick={() => session.mutate("start")}>
            Start session
          </button>
        )}
        {thread.state === "paused" && (
          <button disabled={session.isPending} onClick={() => session.mutate("resume")}>
            Resume session
          </button>
        )}
        {activeSession && (
          <button disabled={session.isPending} onClick={() => setConfirmAction("cancel")}>
            Cancel session
          </button>
        )}
        {thread.state !== "archived" && thread.state !== "deleted" && (
          <button disabled={archive.isPending} onClick={() => setConfirmAction("archive")}>
            Archive
          </button>
        )}
        <Link
          className="button-link secondary"
          to={`/ui/capsules/${thread.capsuleId}#runs`}
        >
          Native PTY fallback
        </Link>
        {thread.state !== "deleted" && (
          <button className="danger" onClick={() => setConfirmDelete(true)}>
            Crypto-shred
          </button>
        )}
      </div>
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
              ? "Cancel the active structured session?"
              : "Archive this retained Thread?"}
          </h2>
          <p>
            {confirmAction === "cancel"
              ? "The active adapter process will be asked to stop."
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
      {pending && (
        <PermissionControls
          thread={thread}
          request={pending}
          onSuccess={refresh}
          onConflict={refresh}
        />
      )}
      <section className="panel">
        <div className="section-heading">
          <h2>Transcript</h2>
          <span className="connection" role="status">
            {status} · cursor {blocks.at(-1)?.messageSequence ?? replay.data.nextCursor}
          </span>
        </div>
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
        <BlockTimeline blocks={blocks} />
      </section>
      {canCompose && thread.state !== "archived" && (
        <ThreadComposer
          thread={thread}
          onSuccess={refresh}
          onConflict={refresh}
        />
      )}
    </article>
  );
}

export function CapsuleThreads({ capsule }: { capsule: Capsule }) {
  const threads = useQuery(queries.threads(capsule.id));
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  return (
    <section className="panel">
      <div className="section-heading">
        <h2>Structured Threads</h2>
        <Link to="/ui/threads">Open fleet</Link>
      </div>
      {threads.isPending ? (
        <p role="status">Loading Threads…</p>
      ) : threads.isError ? (
        <ThreadError error={threads.error} />
      ) : threads.data.items.length === 0 ? (
        <p>No retained Threads for this Capsule.</p>
      ) : (
        <ul className="thread-list compact" aria-label="Capsule Threads">
          {threads.data.items.map((thread) => (
            <ThreadSummary key={thread.id} thread={thread} />
          ))}
        </ul>
      )}
      {capsule.state === "Ready" ? (
        <details className="thread-fast-path">
          <summary>Create Thread and send first task</summary>
          <ThreadCreate
            capsule={capsule}
            onCreated={async (thread) => {
              await queryClient.invalidateQueries({ queryKey: ["threads", capsule.id] });
              navigate(`/ui/threads/${thread.id}`);
            }}
          />
        </details>
      ) : (
        <p className="notice">The Capsule must be Ready to create a Thread.</p>
      )}
    </section>
  );
}
