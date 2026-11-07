-- Content-free counts of operator messages sent straight to a helper, and how
-- many of them the lead has been told about in a wake prompt.
ALTER TABLE thread_links
    ADD COLUMN operator_messages INTEGER NOT NULL DEFAULT 0 CHECK (operator_messages >= 0);
ALTER TABLE thread_links
    ADD COLUMN operator_noticed INTEGER NOT NULL DEFAULT 0 CHECK (operator_noticed >= 0);

CREATE TRIGGER thread_links_operator_notices
BEFORE UPDATE ON thread_links
WHEN NEW.operator_messages < OLD.operator_messages
    OR NEW.operator_noticed < OLD.operator_noticed
    OR NEW.operator_noticed > NEW.operator_messages
BEGIN
    SELECT RAISE(ABORT, 'thread link lifecycle violation');
END;
