CREATE TABLE agent_runs_v5 (
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
    step_count INTEGER NOT NULL DEFAULT 0 CHECK (step_count BETWEEN 0 AND 128),
    tool_call_count INTEGER NOT NULL DEFAULT 0 CHECK (tool_call_count BETWEEN 0 AND 256),
    model_request_count INTEGER NOT NULL DEFAULT 0 CHECK (model_request_count BETWEEN 0 AND 64),
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

INSERT INTO agent_runs_v5 (
    id, session_id, request_message_id, retained_request_message_id, status,
    scope_context, scope_namespace, scope_generation,
    resource_refs_json, prompt_version, tool_catalog_version,
    step_count, tool_call_count, model_request_count,
    input_tokens, output_tokens, termination_reason,
    persistence_degraded, started_at_ms, finished_at_ms
)
SELECT
    id, session_id, request_message_id, retained_request_message_id, status,
    scope_context, scope_namespace, scope_generation,
    resource_refs_json, prompt_version, tool_catalog_version,
    step_count, tool_call_count, model_request_count,
    input_tokens, output_tokens, termination_reason,
    persistence_degraded, started_at_ms, finished_at_ms
FROM agent_runs;

DROP TABLE agent_runs;
ALTER TABLE agent_runs_v5 RENAME TO agent_runs;

CREATE INDEX agent_runs_session_started_idx
    ON agent_runs (session_id, started_at_ms, id);

CREATE INDEX agent_runs_status_idx
    ON agent_runs (status);

CREATE TABLE model_requests_v5 (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 64),
    provider_kind TEXT NOT NULL CHECK (provider_kind = 'openai_compatible'),
    endpoint_origin_hash TEXT CHECK (
        endpoint_origin_hash IS NULL
        OR (
            length(endpoint_origin_hash) = 64
            AND endpoint_origin_hash NOT GLOB '*[^0-9a-f]*'
        )
    ),
    model TEXT NOT NULL CHECK (length(CAST(model AS BLOB)) BETWEEN 1 AND 128),
    status TEXT NOT NULL CHECK (
        status IN ('requested', 'running', 'succeeded', 'failed', 'cancelled', 'timed_out')
    ),
    error_class TEXT CHECK (
        error_class IS NULL
        OR length(CAST(error_class AS BLOB)) BETWEEN 1 AND 64
    ),
    provider_request_id TEXT CHECK (
        provider_request_id IS NULL
        OR length(CAST(provider_request_id AS BLOB)) <= 256
    ),
    prompt_version TEXT NOT NULL CHECK (
        length(CAST(prompt_version AS BLOB)) BETWEEN 1 AND 128
    ),
    prompt_fingerprint TEXT NOT NULL CHECK (
        length(prompt_fingerprint) = 64
        AND prompt_fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    response_fingerprint TEXT CHECK (
        response_fingerprint IS NULL
        OR (
            length(response_fingerprint) = 64
            AND response_fingerprint NOT GLOB '*[^0-9a-f]*'
        )
    ),
    input_tokens INTEGER CHECK (input_tokens IS NULL OR input_tokens >= 0),
    output_tokens INTEGER CHECK (output_tokens IS NULL OR output_tokens >= 0),
    latency_ms INTEGER CHECK (latency_ms IS NULL OR latency_ms >= 0),
    started_at_ms INTEGER NOT NULL CHECK (started_at_ms >= 0),
    finished_at_ms INTEGER CHECK (finished_at_ms IS NULL OR finished_at_ms >= started_at_ms),
    UNIQUE (run_id, sequence),
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
) STRICT;

INSERT INTO model_requests_v5 (
    id, run_id, sequence, provider_kind, endpoint_origin_hash,
    model, status, error_class, provider_request_id,
    prompt_version, prompt_fingerprint, response_fingerprint,
    input_tokens, output_tokens, latency_ms, started_at_ms, finished_at_ms
)
SELECT
    id, run_id, sequence, provider_kind, endpoint_origin_hash,
    model, status, error_class, provider_request_id,
    prompt_version, prompt_fingerprint, response_fingerprint,
    input_tokens, output_tokens, latency_ms, started_at_ms, finished_at_ms
FROM model_requests;

DROP TABLE model_requests;
ALTER TABLE model_requests_v5 RENAME TO model_requests;

CREATE TABLE tool_invocations_v5 (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 256),
    tool_name TEXT NOT NULL CHECK (
        tool_name IN (
            'get_resource',
            'list_resources',
            'get_events',
            'get_pod_logs',
            'get_previous_pod_logs',
            'get_related_resources',
            'get_cluster_overview'
        )
    ),
    tool_version TEXT NOT NULL CHECK (
        length(CAST(tool_version AS BLOB)) BETWEEN 1 AND 128
    ),
    purpose TEXT CHECK (
        purpose IS NULL
        OR length(CAST(purpose AS BLOB)) <= 1024
    ),
    arguments_json TEXT NOT NULL CHECK (
        json_valid(arguments_json)
        AND length(CAST(arguments_json AS BLOB)) <= 8192
    ),
    arguments_digest TEXT NOT NULL CHECK (
        length(arguments_digest) = 64
        AND arguments_digest NOT GLOB '*[^0-9a-f]*'
    ),
    status TEXT NOT NULL CHECK (
        status IN ('requested', 'running', 'succeeded', 'failed', 'cancelled', 'denied')
    ),
    error_class TEXT CHECK (
        error_class IS NULL
        OR length(CAST(error_class AS BLOB)) BETWEEN 1 AND 64
    ),
    safe_error TEXT CHECK (
        safe_error IS NULL
        OR length(CAST(safe_error AS BLOB)) <= 4096
    ),
    result_summary TEXT CHECK (
        result_summary IS NULL
        OR length(CAST(result_summary AS BLOB)) <= 4096
    ),
    returned_bytes INTEGER NOT NULL DEFAULT 0 CHECK (returned_bytes BETWEEN 0 AND 65536),
    evidence_count INTEGER NOT NULL DEFAULT 0 CHECK (evidence_count BETWEEN 0 AND 100),
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    started_at_ms INTEGER CHECK (started_at_ms IS NULL OR started_at_ms >= 0),
    finished_at_ms INTEGER CHECK (
        finished_at_ms IS NULL
        OR started_at_ms IS NULL
        OR finished_at_ms >= started_at_ms
    ),
    UNIQUE (run_id, sequence),
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
) STRICT;

INSERT INTO tool_invocations_v5 (
    id, run_id, sequence, tool_name, tool_version, purpose,
    arguments_json, arguments_digest, status, error_class, safe_error,
    result_summary, returned_bytes, evidence_count, truncated,
    started_at_ms, finished_at_ms
)
SELECT
    id, run_id, sequence, tool_name, tool_version, purpose,
    arguments_json, arguments_digest, status, error_class, safe_error,
    result_summary, returned_bytes, evidence_count, truncated,
    started_at_ms, finished_at_ms
FROM tool_invocations;

DROP TABLE tool_invocations;
ALTER TABLE tool_invocations_v5 RENAME TO tool_invocations;

CREATE TABLE messages_v5 (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    session_id TEXT NOT NULL,
    run_id TEXT,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system_notice')),
    content TEXT NOT NULL CHECK (
        (
            role = 'assistant'
            AND length(CAST(content AS BLOB)) <= 131072
        )
        OR (
            role IN ('user', 'system_notice')
            AND length(CAST(content AS BLOB)) <= 65536
        )
    ),
    content_format TEXT NOT NULL CHECK (content_format IN ('plain', 'markdown')),
    status TEXT NOT NULL CHECK (status IN ('committed', 'interrupted', 'redacted')),
    scope_context TEXT CHECK (
        scope_context IS NULL
        OR length(CAST(scope_context AS BLOB)) BETWEEN 1 AND 253
    ),
    scope_namespace TEXT CHECK (
        scope_namespace IS NULL
        OR length(CAST(scope_namespace AS BLOB)) BETWEEN 1 AND 63
    ),
    scope_generation INTEGER CHECK (scope_generation IS NULL OR scope_generation >= 0),
    resource_refs_json TEXT CHECK (
        resource_refs_json IS NULL
        OR (
            json_valid(resource_refs_json)
            AND length(CAST(resource_refs_json AS BLOB)) <= 8192
        )
    ),
    content_hash TEXT NOT NULL CHECK (
        length(content_hash) = 64
        AND content_hash NOT GLOB '*[^0-9a-f]*'
    ),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE SET NULL
        DEFERRABLE INITIALLY DEFERRED
) STRICT;

INSERT INTO messages_v5 (
    id, session_id, run_id, role, content, content_format, status,
    scope_context, scope_namespace, scope_generation, resource_refs_json,
    content_hash, created_at_ms
)
SELECT
    id, session_id, run_id, role, content, content_format, status,
    scope_context, scope_namespace, scope_generation, resource_refs_json,
    content_hash, created_at_ms
FROM messages;

DROP TABLE messages;
ALTER TABLE messages_v5 RENAME TO messages;

CREATE INDEX messages_session_created_idx
    ON messages (session_id, created_at_ms, id);
