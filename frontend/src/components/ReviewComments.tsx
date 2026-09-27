import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  type KeyboardEvent,
  type ReactNode,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import { useNavigate } from "react-router-dom";

import { api, normalizeAPIError } from "../api/client";
import type { Capsule, Thread } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import {
  capsuleSessions,
  defaultSession,
  harnessName,
  sessionPath,
} from "../shell/capsuleActivity";
import type { DiffFile } from "./DiffViewer";
import {
  anchorLabel,
  commentsByRow,
  composeReviewMessage,
  MAX_COMMENT_LENGTH,
  MAX_MESSAGE_BYTES,
  MAX_REVIEW_COMMENTS,
  messageBytes,
  rangeCandidates,
  reviewAnchor,
  type ReviewComment,
  type ReviewCommentsController,
  useReviewComments,
} from "./reviewBatch";
import { sanitizeThreadText } from "./Threads";

function CommentEditor({
  label,
  initial = "",
  submitLabel,
  onSubmit,
  onCancel,
  children,
}: {
  label: string;
  initial?: string;
  submitLabel: string;
  onSubmit: (body: string) => void;
  onCancel: () => void;
  children?: ReactNode;
}) {
  const id = useId();
  const [body, setBody] = useState(initial);
  const field = useRef<HTMLTextAreaElement>(null);
  useEffect(() => field.current?.focus(), []);
  const submit = () => {
    if (body.trim()) onSubmit(body);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      onCancel();
    } else if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      submit();
    }
  };
  return (
    <form
      className="review-editor"
      aria-label={label}
      onSubmit={(event) => {
        event.preventDefault();
        submit();
      }}
    >
      <label htmlFor={`${id}-body`}>Comment (Markdown)</label>
      <textarea
        id={`${id}-body`}
        ref={field}
        rows={3}
        maxLength={MAX_COMMENT_LENGTH}
        value={body}
        onChange={(event) => setBody(event.target.value)}
        onKeyDown={onKeyDown}
        placeholder="Ctrl+Enter saves · Escape cancels"
      />
      {children}
      <div className="review-editor-actions">
        <button type="button" className="text-button" onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" disabled={!body.trim()}>
          {submitLabel}
        </button>
      </div>
    </form>
  );
}

export function AddCommentForm({
  file,
  lineIndex,
  onAdd,
  onCancel,
}: {
  file: DiffFile;
  lineIndex: number;
  onAdd: (anchor: NonNullable<ReturnType<typeof reviewAnchor>>, body: string) => void;
  onCancel: () => void;
}) {
  const id = useId();
  const candidates = useMemo(() => rangeCandidates(file, lineIndex), [file, lineIndex]);
  const [end, setEnd] = useState(lineIndex);
  const start = reviewAnchor(file, lineIndex);
  if (!start) return null;
  return (
    <CommentEditor
      label={`New comment on ${anchorLabel(start)}`}
      submitLabel="Add comment"
      onCancel={onCancel}
      onSubmit={(body) => {
        const anchor = reviewAnchor(file, lineIndex, end);
        if (anchor) onAdd(anchor, body);
      }}
    >
      {candidates.length > 1 && (
        <div className="review-range">
          <label htmlFor={`${id}-end`}>Through line</label>
          <select
            id={`${id}-end`}
            value={end}
            onChange={(event) => setEnd(Number(event.target.value))}
          >
            {candidates.map((row) => (
              <option key={row.lineIndex} value={row.lineIndex}>
                {row.lineIndex === lineIndex ? `${row.line} (this line only)` : row.line}
              </option>
            ))}
          </select>
        </div>
      )}
    </CommentEditor>
  );
}

export function ReviewCommentCard({
  comment,
  review,
  compact = false,
}: {
  comment: ReviewComment;
  review: ReviewCommentsController;
  compact?: boolean;
}) {
  const [editing, setEditing] = useState(false);
  const label = anchorLabel(comment);
  if (editing) {
    return (
      <CommentEditor
        label={`Edit comment on ${label}`}
        initial={comment.body}
        submitLabel="Save"
        onCancel={() => setEditing(false)}
        onSubmit={(body) => {
          review.update(comment.id, body);
          setEditing(false);
        }}
      />
    );
  }
  return (
    <article
      className={`review-comment${compact ? " compact" : ""}`}
      aria-label={`Comment on ${label}`}
    >
      <header>
        <span className="review-comment-location mono">{label}</span>
        <span className="review-comment-actions">
          <button
            type="button"
            className="text-button"
            aria-label={`Edit comment on ${label}`}
            onClick={() => setEditing(true)}
          >
            Edit
          </button>
          <button
            type="button"
            className="text-button"
            aria-label={`Remove comment on ${label}`}
            onClick={() => review.remove(comment.id)}
          >
            Remove
          </button>
        </span>
      </header>
      <p className="review-comment-body">{comment.body}</p>
    </article>
  );
}

const sendableThread = (thread: Thread) =>
  thread.state === "active" && thread.structuredSupported !== false;

function ReviewComposer({
  capsule,
  structured,
  message,
  onBack,
  onSent,
}: {
  capsule: Capsule;
  structured: boolean;
  message: string;
  onBack: () => void;
  onSent: () => void;
}) {
  const id = useId();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const threads = useQuery({
    ...queries.threads(capsule.id),
    enabled: structured,
  });
  const eligible = useMemo(
    () => (threads.data?.items ?? []).filter(sendableThread),
    [threads.data],
  );
  const sessions = useMemo(
    () =>
      capsuleSessions(capsule, [], eligible).filter(
        (session) => session.kind === "thread",
      ),
    [capsule, eligible],
  );
  const [chosen, setChosen] = useState("");
  const target =
    sessions.find((session) => session.id === chosen)?.id ??
    defaultSession(sessions)?.id ??
    "";
  const [copied, setCopied] = useState<"copied" | "failed">();
  const [conflict, setConflict] = useState(false);
  const bytes = messageBytes(message);
  const tooLarge = bytes > MAX_MESSAGE_BYTES;

  const send = useMutation({
    mutationFn: (threadId: string) => {
      const thread = eligible.find((item) => item.id === threadId);
      if (!thread) throw new Error("Choose a session to receive the review.");
      return api.sendThreadMessage(thread.id, thread.resourceVersion, message);
    },
    onMutate: () => setConflict(false),
    onSuccess: (_, threadId) => {
      // The batch is cleared only after the server accepted the message.
      onSent();
      navigate(sessionPath(capsule.id, threadId));
      void queryClient.invalidateQueries({ queryKey: ["thread", threadId] });
      void queryClient.invalidateQueries({ queryKey: ["thread-blocks", threadId] });
      void queryClient.invalidateQueries({ queryKey: ["threads", capsule.id] });
    },
    onError: async (error, threadId) => {
      if (normalizeAPIError(error).code !== "conflict") return;
      setConflict(true);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["threads", capsule.id] }),
        queryClient.invalidateQueries({ queryKey: ["thread", threadId] }),
      ]);
    },
  });

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(message);
      setCopied("copied");
    } catch {
      setCopied("failed");
    }
  };

  return (
    <section className="review-composer" aria-labelledby={`${id}-title`}>
      <h4 id={`${id}-title`}>Message to the agent</h4>
      <pre className="review-preview" aria-label="Message preview" tabIndex={0}>
        {message}
      </pre>
      {tooLarge && (
        <p className="inline-error" role="alert">
          The message is {Math.ceil(bytes / 1024)} KiB, over the{" "}
          {MAX_MESSAGE_BYTES / 1024} KiB session limit. Remove or shorten comments
          before sending.
        </p>
      )}
      {structured && threads.isPending ? (
        <p className="tool-loading" role="status">Loading sessions…</p>
      ) : structured && threads.isError ? (
        <p className="inline-error" role="alert">
          {sanitizeThreadText(normalizeAPIError(threads.error).message)}
        </p>
      ) : sessions.length > 0 ? (
        <div className="review-target">
          <label htmlFor={`${id}-target`}>Send to</label>
          <select
            id={`${id}-target`}
            value={target}
            onChange={(event) => setChosen(event.target.value)}
          >
            {sessions.map((session) => (
              <option key={session.id} value={session.id}>
                {harnessName(session.harness)} · {session.label} ·{" "}
                {session.id.slice(0, 8)}
              </option>
            ))}
          </select>
        </div>
      ) : (
        <p className="notice" role="status">
          This Capsule has no structured session that can receive the review.
          Meridian never types into an agent's terminal: copy the prompt and paste
          it into the terminal yourself.
        </p>
      )}
      <div className="review-composer-actions">
        <button type="button" className="text-button" onClick={onBack}>
          Back
        </button>
        <button type="button" className="secondary" onClick={() => void copy()}>
          Copy as prompt
        </button>
        {sessions.length > 0 && (
          <button
            type="button"
            disabled={tooLarge || !target || send.isPending}
            onClick={() => send.mutate(target)}
          >
            {send.isPending ? "Sending…" : "Send to agent"}
          </button>
        )}
      </div>
      {copied === "copied" && (
        <p className="review-status" role="status">
          Copied the review prompt to the clipboard. Paste it where the agent can
          read it; the comments stay pending until you discard them.
        </p>
      )}
      {copied === "failed" && (
        <p className="inline-error" role="alert">
          The browser did not allow clipboard access. Select the message preview
          and copy it manually.
        </p>
      )}
      {send.isError && (
        <p className="inline-error" role="alert">
          {conflict
            ? "The session changed while you were reviewing. Its latest state is loaded; check the target and send again."
            : sanitizeThreadText(normalizeAPIError(send.error).message)}
        </p>
      )}
    </section>
  );
}

// ReviewBatch summarizes a Capsule's pending comments and composes them
// into one explicit message for a structured Thread, or a copied prompt.
export function ReviewBatch({
  capsule,
  files,
  structured,
}: {
  capsule: Capsule;
  files: DiffFile[];
  structured: boolean;
}) {
  const review = useReviewComments(capsule.id);
  const [composing, setComposing] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [sent, setSent] = useState(false);
  const { placed } = useMemo(
    () => commentsByRow(files, review.comments),
    [files, review.comments],
  );
  const message = useMemo(
    () => composeReviewMessage(review.comments),
    [review.comments],
  );
  const count = review.comments.length;

  useEffect(() => {
    if (count === 0) {
      setComposing(false);
      setConfirmDiscard(false);
    } else setSent(false);
  }, [count]);

  if (count === 0) {
    return sent ? (
      <p className="review-status" role="status">Review sent to the agent.</p>
    ) : null;
  }
  return (
    <section className="review-batch" aria-label="Pending review">
      <header className="review-batch-header">
        <h3>
          Review <span className="count-label">{count} pending</span>
        </h3>
        <span>
          <button
            type="button"
            className="text-button"
            onClick={() => setConfirmDiscard(true)}
          >
            Discard all
          </button>
          {!composing && (
            <button type="button" onClick={() => setComposing(true)}>
              Send to agent…
            </button>
          )}
        </span>
      </header>
      {confirmDiscard && (
        <div className="review-discard" role="alertdialog" aria-label="Discard review">
          <p>Discard {count === 1 ? "this comment" : `all ${count} comments`}?</p>
          <button type="button" className="danger" onClick={review.clear}>
            Discard
          </button>
          <button type="button" className="text-button" onClick={() => setConfirmDiscard(false)}>
            Keep
          </button>
        </div>
      )}
      {count >= MAX_REVIEW_COMMENTS && (
        <p className="notice">
          A review holds at most {MAX_REVIEW_COMMENTS} comments. Send or remove
          some to add more.
        </p>
      )}
      <ol className="review-summary" aria-label="Pending comments">
        {review.comments.map((comment) => (
          <li key={comment.id}>
            <ReviewCommentCard comment={comment} review={review} compact />
            {!placed.has(comment.id) && (
              <small className="review-outdated">Not in the current diff</small>
            )}
          </li>
        ))}
      </ol>
      {composing && (
        <ReviewComposer
          capsule={capsule}
          structured={structured}
          message={message}
          onBack={() => setComposing(false)}
          onSent={() => {
            review.clear();
            setSent(true);
          }}
        />
      )}
    </section>
  );
}

export function PendingReviewNotice({ capsuleId }: { capsuleId: string }) {
  const { comments } = useReviewComments(capsuleId);
  if (comments.length === 0) return null;
  return (
    <p className="notice" role="status">
      {comments.length === 1 ? "1 review comment is" : `${comments.length} review comments are`}{" "}
      pending. Review and send them from the Changes tool.
    </p>
  );
}
