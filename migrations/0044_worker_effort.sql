-- Match Worker Model spawns carry the lead's reasoning effort (ADR 0033
-- amendment).
--
-- thread_links gains requested_effort: the effort level Match copied from the
-- lead's current session, applied best effort after the requested model and
-- before the subagent's first prompt. effort_requested is 1 once Meridian
-- has asked the subagent's harness for that level, so a scan waits for the
-- harness to report it instead of asking again.

ALTER TABLE thread_links ADD COLUMN requested_effort TEXT NOT NULL DEFAULT ''
    CHECK (length(requested_effort) <= 256);
ALTER TABLE thread_links ADD COLUMN effort_requested INTEGER NOT NULL DEFAULT 0
    CHECK (effort_requested IN (0, 1));
