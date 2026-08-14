CREATE TABLE agent_runs_minimal_v4 (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    session_id TEXT NOT NULL,
    request_message_id TEXT NOT NULL CHECK (length(request_message_id) = 36),
    retained_request_message_id TEXT CHECK (
        retained_request_message_id IS NULL
        OR retained_request_message_id = request_message_id
    ),
    status TEXT NOT NULL CHECK (
        status IN (
            'queued',
            'running',
            'completed',
            'failed',
            'cancelled',
            'timed_out',
            'stale_scope',
            'interrupted'
        )
    ),
    scope_context TEXT NOT NULL CHECK (
        length(CAST(scope_context AS BLOB)) BETWEEN 1 AND 253
    ),
    scope_namespace TEXT NOT NULL CHECK (
        length(CAST(scope_namespace AS BLOB)) BETWEEN 1 AND 63
    ),
    scope_generation INTEGER NOT NULL CHECK (scope_generation >= 0),
    resource_refs_json TEXT CHECK (
        resource_refs_json IS NULL
        OR (
            json_valid(resource_refs_json)
            AND length(CAST(resource_refs_json AS BLOB)) <= 8192
        )
    ),
    prompt_version TEXT NOT NULL CHECK (
        length(CAST(prompt_version AS BLOB)) BETWEEN 1 AND 128
    ),
    tool_catalog_version TEXT NOT NULL CHECK (
        length(CAST(tool_catalog_version AS BLOB)) BETWEEN 1 AND 128
    ),
    step_count INTEGER NOT NULL DEFAULT 0 CHECK (step_count BETWEEN 0 AND 8),
    tool_call_count INTEGER NOT NULL DEFAULT 0 CHECK (tool_call_count BETWEEN 0 AND 10),
    model_request_count INTEGER NOT NULL DEFAULT 0 CHECK (model_request_count BETWEEN 0 AND 3),
    input_tokens INTEGER CHECK (input_tokens IS NULL OR input_tokens >= 0),
    output_tokens INTEGER CHECK (output_tokens IS NULL OR output_tokens >= 0),
    termination_reason TEXT CHECK (
        termination_reason IS NULL
        OR length(CAST(termination_reason AS BLOB)) <= 1024
    ),
    persistence_degraded INTEGER NOT NULL DEFAULT 0 CHECK (persistence_degraded IN (0, 1)),
    started_at_ms INTEGER CHECK (started_at_ms IS NULL OR started_at_ms >= 0),
    finished_at_ms INTEGER CHECK (finished_at_ms IS NULL OR finished_at_ms >= 0),
    CHECK (
        finished_at_ms IS NULL
        OR started_at_ms IS NULL
        OR finished_at_ms >= started_at_ms
    ),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (retained_request_message_id) REFERENCES messages(id)
        DEFERRABLE INITIALLY DEFERRED
) STRICT;

INSERT INTO agent_runs_minimal_v4 (
    id, session_id, request_message_id, retained_request_message_id, status,
    scope_context, scope_namespace, scope_generation,
    resource_refs_json, prompt_version, tool_catalog_version,
    step_count, tool_call_count, model_request_count,
    input_tokens, output_tokens, termination_reason,
    persistence_degraded, started_at_ms, finished_at_ms
)
SELECT
    id, session_id, request_message_id, request_message_id, status,
    scope_context, scope_namespace, scope_generation,
    resource_refs_json, prompt_version, tool_catalog_version,
    step_count, tool_call_count, model_request_count,
    input_tokens, output_tokens, termination_reason,
    persistence_degraded, started_at_ms, finished_at_ms
FROM agent_runs;

DROP TABLE agent_runs;
ALTER TABLE agent_runs_minimal_v4 RENAME TO agent_runs;

CREATE INDEX agent_runs_session_started_idx
    ON agent_runs (session_id, started_at_ms, id);

CREATE INDEX agent_runs_status_idx
    ON agent_runs (status);
