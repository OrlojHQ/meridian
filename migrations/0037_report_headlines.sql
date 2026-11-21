-- Lead wakes carry report headlines instead of full reports (ADR 0059).
-- report_headlined_seq is the highest report sequence a wake has shown as a
-- headline. It is content-free and only stops a lead being woken again for a
-- headline it already saw; report_delivered_seq still moves only when the
-- lead reads the full report through wait or result.
ALTER TABLE thread_links
    ADD COLUMN report_headlined_seq INTEGER NOT NULL DEFAULT 0 CHECK (report_headlined_seq >= 0);

-- Reports already delivered in full were also shown.
UPDATE thread_links SET report_headlined_seq = report_delivered_seq;

CREATE TRIGGER thread_links_report_headlines
BEFORE UPDATE OF report_headlined_seq, report_seq ON thread_links
WHEN NEW.report_headlined_seq < OLD.report_headlined_seq
    OR NEW.report_headlined_seq > NEW.report_seq
BEGIN
    SELECT RAISE(ABORT, 'thread link lifecycle violation');
END;

CREATE TRIGGER thread_links_report_headlines_insert
BEFORE INSERT ON thread_links
WHEN NEW.report_headlined_seq > NEW.report_seq
BEGIN
    SELECT RAISE(ABORT, 'thread link lifecycle violation');
END;
