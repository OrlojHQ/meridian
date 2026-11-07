import { useId, useState } from "react";

import type { Run } from "../api/generated/types.gen";
import { relativeTime } from "../shell/capsuleActivity";
import { type EndedRunState, failureText } from "./threadChat";
import { WarningIcon } from "./ui/Icons";

const endedCopy: Record<EndedRunState, string> = {
  Failed: "The agent's session failed",
  Cancelled: "The session was stopped",
  Succeeded: "The agent's session exited",
};

function endedAt(run?: Run) {
  const value = run?.finishedAt ?? run?.updatedAt;
  if (!value) return null;
  const relative = relativeTime(value);
  if (!relative) return null;
  return (
    <>
      {" "}
      <time dateTime={value} title={new Date(value).toLocaleString()}>
        {relative === "now" ? "just now" : `${relative} ago`}
      </time>
    </>
  );
}

export function SessionEnded({
  state,
  run,
  lastPrompt,
  restartPending,
  onRestart,
  onNewSession,
}: {
  state: EndedRunState;
  run?: Run;
  lastPrompt?: string;
  restartPending: boolean;
  onRestart?: () => void;
  onNewSession?: () => void;
}) {
  const [copied, setCopied] = useState<"copied" | "failed">();
  const titleId = useId();
  const reason = state === "Failed" && run ? failureText(run.failure) : "";
  const copy = async () => {
    if (!lastPrompt) return;
    try {
      await navigator.clipboard.writeText(lastPrompt);
      setCopied("copied");
    } catch {
      setCopied("failed");
    }
  };
  return (
    <section
      className={`session-ended session-ended-${state.toLowerCase()}`}
      aria-labelledby={titleId}
    >
      <header>
        {state === "Failed" && <WarningIcon />}
        <h2 id={titleId}>Session ended</h2>
      </header>
      <p>
        {endedCopy[state]}
        {endedAt(run)}.{" "}
        {onRestart
          ? "Restart it to keep working in this Thread; messages can't be sent until then."
          : "Messages can't be sent until a new session starts."}
      </p>
      {state === "Failed" && run && (
        <p className="session-ended-reason">
          <span>Reason</span> {reason || "No reason was recorded."}
        </p>
      )}
      <div className="session-ended-actions">
        {onRestart && (
          <button
            type="button"
            className="primary-button"
            disabled={restartPending}
            onClick={onRestart}
          >
            Restart session
          </button>
        )}
        {lastPrompt && (
          <button type="button" onClick={() => void copy()}>
            Copy last prompt
          </button>
        )}
        {onNewSession && (
          <button type="button" className="secondary" onClick={onNewSession}>
            New session
          </button>
        )}
      </div>
      {copied === "copied" && (
        <p className="session-ended-status" role="status">
          Copied your last message to the clipboard.
        </p>
      )}
      {copied === "failed" && (
        <p className="inline-error" role="alert">
          The browser did not allow clipboard access.
        </p>
      )}
    </section>
  );
}
