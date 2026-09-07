ALTER TABLE sessions
ADD COLUMN last_activity_at_ms INTEGER NOT NULL DEFAULT 0;

UPDATE sessions
SET last_activity_at_ms = updated_at_ms;

CREATE INDEX sessions_last_activity_idx
    ON sessions (last_activity_at_ms DESC, id DESC);
