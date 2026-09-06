ALTER TABLE messages ADD COLUMN run_sequence INTEGER CHECK (
    run_sequence IS NULL OR run_sequence BETWEEN 0 AND 9
);

UPDATE messages
SET run_sequence = CASE role
    WHEN 'user' THEN 0
    WHEN 'assistant' THEN 1
END
WHERE run_id IS NOT NULL
    AND role IN ('user', 'assistant');

CREATE UNIQUE INDEX messages_run_sequence_idx
    ON messages (run_id, run_sequence)
    WHERE run_id IS NOT NULL AND run_sequence IS NOT NULL;

-- Stored summaries use the released strict two-message run grammar. They are
-- safe derivatives, so invalidate them while retaining their source Messages.
DROP TABLE session_context_summaries;

CREATE TABLE session_context_summaries (
    session_id TEXT PRIMARY KEY,
    summary_text TEXT NOT NULL CHECK (
        length(CAST(summary_text AS BLOB)) BETWEEN 1 AND 16384
    ),
    summary_hash TEXT NOT NULL CHECK (
        length(summary_hash) = 64
        AND summary_hash NOT GLOB '*[^0-9a-f]*'
    ),
    schema_version TEXT NOT NULL CHECK (
        schema_version = 'session-context-summary/v2'
    ),
    policy_version TEXT NOT NULL CHECK (
        policy_version = 'safe-conversation-context/2026-09-06.v2'
    ),
    covered_first_message_id TEXT NOT NULL CHECK (
        length(covered_first_message_id) = 36
    ),
    covered_through_message_id TEXT NOT NULL CHECK (
        length(covered_through_message_id) = 36
    ),
    covered_count INTEGER NOT NULL CHECK (
        covered_count BETWEEN 2 AND 4096
    ),
    covered_bytes INTEGER NOT NULL CHECK (covered_bytes BETWEEN 1 AND 4194304),
    coverage_digest TEXT NOT NULL CHECK (
        length(coverage_digest) = 64
        AND coverage_digest NOT GLOB '*[^0-9a-f]*'
    ),
    generated_at_ms INTEGER NOT NULL CHECK (generated_at_ms >= 0),
    agent_profile TEXT NOT NULL CHECK (
        length(CAST(agent_profile AS BLOB)) BETWEEN 1 AND 128
    ),
    agent_origin_hash TEXT NOT NULL CHECK (
        length(agent_origin_hash) = 64
        AND agent_origin_hash NOT GLOB '*[^0-9a-f]*'
    ),
    truncated INTEGER NOT NULL CHECK (truncated IN (0, 1)),
    degraded INTEGER NOT NULL CHECK (degraded IN (0, 1)),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (covered_first_message_id) REFERENCES messages(id) ON DELETE CASCADE,
    FOREIGN KEY (covered_through_message_id) REFERENCES messages(id) ON DELETE CASCADE
) STRICT;
