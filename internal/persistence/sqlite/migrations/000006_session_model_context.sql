ALTER TABLE privacy_consents RENAME TO privacy_consents_v1;

CREATE TABLE privacy_consents (
    role TEXT PRIMARY KEY CHECK (role IN ('agent', 'approval_reviewer')),
    policy_version TEXT NOT NULL CHECK (
        length(CAST(policy_version AS BLOB)) BETWEEN 1 AND 64
    ),
    origin_hash TEXT NOT NULL CHECK (
        length(origin_hash) = 64
        AND origin_hash NOT GLOB '*[^0-9a-f]*'
    ),
    categories_json TEXT NOT NULL CHECK (
        json_valid(categories_json)
        AND json_type(categories_json) = 'array'
        AND length(CAST(categories_json AS BLOB)) BETWEEN 2 AND 2048
    ),
    decision TEXT NOT NULL CHECK (
        decision IN ('pending', 'accepted', 'rejected', 'revoked')
    ),
    decided_at_ms INTEGER NOT NULL CHECK (decided_at_ms >= 0),
    schema_version INTEGER NOT NULL CHECK (schema_version = 2)
) STRICT;

INSERT INTO privacy_consents (
    role, policy_version, origin_hash, categories_json,
    decision, decided_at_ms, schema_version
)
SELECT
    'agent', policy_version, origin_hash, categories_json,
    decision, decided_at_ms, 2
FROM privacy_consents_v1;

DROP TABLE privacy_consents_v1;

ALTER TABLE model_requests ADD COLUMN profile_name TEXT NOT NULL DEFAULT 'agent'
    CHECK (length(CAST(profile_name AS BLOB)) BETWEEN 1 AND 128);
ALTER TABLE model_requests ADD COLUMN model_role TEXT NOT NULL DEFAULT 'agent'
    CHECK (model_role IN ('agent', 'approval_reviewer'));
ALTER TABLE model_requests ADD COLUMN invocation TEXT NOT NULL DEFAULT 'agent'
    CHECK (invocation IN ('agent', 'agent_summary', 'approval_review'));
ALTER TABLE model_requests ADD COLUMN reserved_cost_units INTEGER NOT NULL DEFAULT 1
    CHECK (reserved_cost_units BETWEEN 1 AND 64);

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
        schema_version = 'session-context-summary/v1'
    ),
    policy_version TEXT NOT NULL CHECK (
        policy_version = 'safe-conversation-context/2026-09-04.v1'
    ),
    covered_first_message_id TEXT NOT NULL CHECK (
        length(covered_first_message_id) = 36
    ),
    covered_through_message_id TEXT NOT NULL CHECK (
        length(covered_through_message_id) = 36
    ),
    covered_count INTEGER NOT NULL CHECK (
        covered_count BETWEEN 2 AND 4096 AND covered_count % 2 = 0
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
