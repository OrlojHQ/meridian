import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { Link } from "react-router-dom";

import type { Thread } from "../api/generated/types.gen";
import { queries } from "../api/queries";
import { earlierThreads, harnessName, revisionStamp } from "../shell/capsuleActivity";
import { ChatEntry } from "./ThreadConversation";
import { ThreadError } from "./ThreadError";
import { buildThreadChat } from "./threadChat";

const noAnswer = () => undefined;
const noRefresh = async () => undefined;

// A Capsule runs one Thread session at a time, and each session keeps its own
// transcript. Earlier ones read as the start of the same conversation, folded
// until asked for, so opening a Capsule decrypts only the current transcript.
// The divider says plainly that the agent does not have what is above it.
export function EarlierSessions({
  thread,
  threads,
  unfold,
}: {
  thread: Thread;
  threads: Thread[];
  unfold?: string;
}) {
  const earlier = useMemo(() => earlierThreads(threads, thread), [threads, thread]);
  if (earlier.length === 0) return null;
  return (
    <section className="earlier-sessions" aria-label="Earlier sessions">
      {earlier.map((item) => (
        <EarlierSession key={item.id} thread={item} initiallyOpen={item.id === unfold} />
      ))}
      <p className="session-divider" role="separator">
        <span>
          New session · {revisionStamp(thread.createdAt)}. The agent starts without the
          conversation above.
        </span>
      </p>
    </section>
  );
}

function EarlierSession({ thread, initiallyOpen }: { thread: Thread; initiallyOpen: boolean }) {
  const [open, setOpen] = useState(initiallyOpen);
  const blocks = useQuery({ ...queries.threadBlocks(thread.id), enabled: open });
  // No run state: nothing in an earlier session can be answered or steered.
  const chat = useMemo(() => buildThreadChat(blocks.data?.items ?? []), [blocks.data]);
  const panelId = `earlier-${thread.id}`;
  const messages = thread.messageCount === 1 ? "1 message" : `${thread.messageCount} messages`;
  return (
    <div className="earlier-session">
      <div className="earlier-session-header">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={panelId}
          onClick={() => setOpen((value) => !value)}
        >
          {open ? "Hide" : "Show"} earlier session
        </button>
        <span className="earlier-session-meta">
          {revisionStamp(thread.createdAt)} · {harnessName(thread.harness)} · {messages}
        </span>
        <Link to={`/ui/threads/${encodeURIComponent(thread.id)}`}>Open</Link>
      </div>
      {open && (
        <div id={panelId}>
          {blocks.isPending ? (
            <p className="earlier-session-note" role="status">Loading the earlier session…</p>
          ) : blocks.isError ? (
            <ThreadError error={blocks.error} />
          ) : chat.items.length === 0 ? (
            <p className="earlier-session-note">No messages in this session.</p>
          ) : (
            <ol className="chat-transcript earlier" aria-label="Earlier session transcript">
              {chat.items.map((item) => (
                <ChatEntry
                  key={item.key}
                  item={item}
                  thread={thread}
                  links={[]}
                  onAnswered={noAnswer}
                  onConflict={noRefresh}
                />
              ))}
            </ol>
          )}
          {blocks.data?.more && (
            <p className="earlier-session-note">
              Showing the start of this session. Open it for the rest.
            </p>
          )}
        </div>
      )}
    </div>
  );
}
