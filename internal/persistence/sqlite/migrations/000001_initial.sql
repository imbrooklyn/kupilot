CREATE TABLE sessions (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    title TEXT NOT NULL CHECK (length(CAST(title AS BLOB)) <= 512),
    status TEXT NOT NULL CHECK (status IN ('active', 'archived')),
    privacy_mode TEXT NOT NULL CHECK (privacy_mode IN ('standard', 'minimal')),
    last_context TEXT CHECK (
        last_context IS NULL
        OR length(CAST(last_context AS BLOB)) BETWEEN 1 AND 253
    ),
    last_namespace TEXT CHECK (
        last_namespace IS NULL
        OR length(CAST(last_namespace AS BLOB)) BETWEEN 1 AND 63
    ),
    selected_resource_json TEXT CHECK (
        selected_resource_json IS NULL
        OR (
            json_valid(selected_resource_json)
            AND length(CAST(selected_resource_json AS BLOB)) <= 4096
        )
    ),
    summary TEXT CHECK (
        summary IS NULL
        OR length(CAST(summary AS BLOB)) <= 4096
    ),
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms)
) STRICT;

CREATE TABLE messages (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    session_id TEXT NOT NULL,
    run_id TEXT,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system_notice')),
    content TEXT NOT NULL CHECK (length(CAST(content AS BLOB)) <= 65536),
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

CREATE TABLE agent_runs (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    session_id TEXT NOT NULL,
    request_message_id TEXT NOT NULL,
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
    FOREIGN KEY (request_message_id) REFERENCES messages(id)
        DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE model_requests (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 3),
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

CREATE TABLE tool_invocations (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 10),
    tool_name TEXT NOT NULL CHECK (
        tool_name IN (
            'get_resource',
            'list_resources',
            'get_events',
            'get_pod_logs',
            'get_previous_pod_logs',
            'get_related_resources'
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

CREATE TABLE evidence_items (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL,
    invocation_id TEXT NOT NULL,
    category TEXT NOT NULL CHECK (
        length(CAST(category AS BLOB)) BETWEEN 1 AND 64
    ),
    resource_ref_json TEXT NOT NULL CHECK (
        json_valid(resource_ref_json)
        AND length(CAST(resource_ref_json AS BLOB)) <= 4096
    ),
    fact TEXT NOT NULL CHECK (length(CAST(fact AS BLOB)) BETWEEN 1 AND 2048),
    source_path TEXT CHECK (
        source_path IS NULL
        OR length(CAST(source_path AS BLOB)) <= 1024
    ),
    severity TEXT CHECK (severity IS NULL OR severity IN ('info', 'warning', 'critical')),
    resource_version TEXT CHECK (
        resource_version IS NULL
        OR length(CAST(resource_version AS BLOB)) <= 256
    ),
    redaction_count INTEGER NOT NULL DEFAULT 0 CHECK (redaction_count >= 0),
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    fingerprint TEXT NOT NULL CHECK (
        length(fingerprint) = 64
        AND fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    observed_at_ms INTEGER NOT NULL CHECK (observed_at_ms >= 0),
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    FOREIGN KEY (invocation_id) REFERENCES tool_invocations(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE diagnoses (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL UNIQUE,
    confirmed_json TEXT NOT NULL CHECK (json_valid(confirmed_json)),
    hypotheses_json TEXT NOT NULL CHECK (json_valid(hypotheses_json)),
    missing_json TEXT NOT NULL CHECK (json_valid(missing_json)),
    actions_json TEXT NOT NULL CHECK (json_valid(actions_json)),
    answer_markdown TEXT NOT NULL,
    validation_warnings_json TEXT CHECK (
        validation_warnings_json IS NULL
        OR json_valid(validation_warnings_json)
    ),
    observed_from_ms INTEGER CHECK (observed_from_ms IS NULL OR observed_from_ms >= 0),
    observed_to_ms INTEGER CHECK (observed_to_ms IS NULL OR observed_to_ms >= 0),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    CHECK (
        observed_from_ms IS NULL
        OR observed_to_ms IS NULL
        OR observed_to_ms >= observed_from_ms
    ),
    CHECK (
        length(CAST(confirmed_json AS BLOB))
        + length(CAST(hypotheses_json AS BLOB))
        + length(CAST(missing_json AS BLOB))
        + length(CAST(actions_json AS BLOB))
        + length(CAST(answer_markdown AS BLOB))
        + coalesce(length(CAST(validation_warnings_json AS BLOB)), 0)
        <= 131072
    ),
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE approvals (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    operation TEXT NOT NULL CHECK (operation = 'restart_deployment'),
    scope_context TEXT NOT NULL CHECK (
        length(CAST(scope_context AS BLOB)) BETWEEN 1 AND 253
    ),
    scope_namespace TEXT NOT NULL CHECK (
        length(CAST(scope_namespace AS BLOB)) BETWEEN 1 AND 63
    ),
    scope_generation INTEGER NOT NULL CHECK (scope_generation >= 0),
    target_ref_json TEXT NOT NULL CHECK (
        json_valid(target_ref_json)
        AND length(CAST(target_ref_json AS BLOB)) <= 4096
    ),
    canonical_parameters_json TEXT NOT NULL CHECK (
        json_valid(canonical_parameters_json)
        AND length(CAST(canonical_parameters_json AS BLOB)) <= 4096
    ),
    operation_digest TEXT NOT NULL UNIQUE CHECK (
        length(operation_digest) = 64
        AND operation_digest NOT GLOB '*[^0-9a-f]*'
    ),
    human_summary TEXT NOT NULL CHECK (
        length(CAST(human_summary AS BLOB)) BETWEEN 1 AND 4096
    ),
    risk_summary TEXT NOT NULL CHECK (
        length(CAST(risk_summary AS BLOB)) BETWEEN 1 AND 4096
    ),
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'approved', 'rejected', 'expired', 'cancelled', 'executed', 'failed')
    ),
    policy_version TEXT NOT NULL CHECK (
        length(CAST(policy_version AS BLOB)) BETWEEN 1 AND 128
    ),
    requested_at_ms INTEGER NOT NULL CHECK (requested_at_ms >= 0),
    expires_at_ms INTEGER NOT NULL CHECK (expires_at_ms > requested_at_ms),
    resolved_at_ms INTEGER CHECK (resolved_at_ms IS NULL OR resolved_at_ms >= requested_at_ms),
    execution_outcome TEXT CHECK (
        execution_outcome IS NULL
        OR length(CAST(execution_outcome AS BLOB)) <= 1024
    ),
    verification_summary TEXT CHECK (
        verification_summary IS NULL
        OR length(CAST(verification_summary AS BLOB)) <= 4096
    ),
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    session_id TEXT,
    run_id TEXT,
    event_type TEXT NOT NULL CHECK (
        length(CAST(event_type AS BLOB)) BETWEEN 1 AND 64
    ),
    actor TEXT NOT NULL CHECK (actor IN ('user', 'agent', 'system')),
    outcome TEXT NOT NULL CHECK (outcome IN ('success', 'failure', 'denied', 'unknown')),
    scope_context TEXT CHECK (
        scope_context IS NULL
        OR length(CAST(scope_context AS BLOB)) BETWEEN 1 AND 253
    ),
    scope_namespace TEXT CHECK (
        scope_namespace IS NULL
        OR length(CAST(scope_namespace AS BLOB)) BETWEEN 1 AND 63
    ),
    scope_generation INTEGER CHECK (scope_generation IS NULL OR scope_generation >= 0),
    subject_ref_json TEXT CHECK (
        subject_ref_json IS NULL
        OR (
            json_valid(subject_ref_json)
            AND length(CAST(subject_ref_json AS BLOB)) <= 4096
        )
    ),
    details_json TEXT NOT NULL CHECK (
        json_valid(details_json)
        AND length(CAST(details_json AS BLOB)) <= 4096
    ),
    correlation_id TEXT CHECK (
        correlation_id IS NULL
        OR length(CAST(correlation_id AS BLOB)) BETWEEN 1 AND 128
    ),
    integrity_hash TEXT CHECK (
        integrity_hash IS NULL
        OR (
            length(integrity_hash) = 64
            AND integrity_hash NOT GLOB '*[^0-9a-f]*'
        )
    ),
    occurred_at_ms INTEGER NOT NULL CHECK (occurred_at_ms >= 0),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE settings (
    key TEXT PRIMARY KEY CHECK (
        length(CAST(key AS BLOB)) BETWEEN 1 AND 64
        AND key = lower(key)
        AND key NOT GLOB '*api_key*'
        AND key NOT GLOB '*token*'
        AND key NOT GLOB '*kubeconfig*'
        AND key NOT GLOB '*credential*'
        AND key NOT GLOB '*certificate*'
        AND key NOT GLOB '*private_key*'
        AND key NOT GLOB '*secret*'
    ),
    value_json TEXT NOT NULL CHECK (
        json_valid(value_json)
        AND length(CAST(value_json AS BLOB)) <= 4096
    ),
    schema_version INTEGER NOT NULL CHECK (schema_version >= 1),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= 0)
) STRICT;

CREATE INDEX messages_session_created_idx
    ON messages (session_id, created_at_ms, id);

CREATE INDEX agent_runs_session_started_idx
    ON agent_runs (session_id, started_at_ms, id);

CREATE INDEX agent_runs_status_idx
    ON agent_runs (status);

CREATE INDEX evidence_items_run_category_idx
    ON evidence_items (run_id, category);

CREATE INDEX evidence_items_invocation_idx
    ON evidence_items (invocation_id);

CREATE INDEX audit_events_session_occurred_idx
    ON audit_events (session_id, occurred_at_ms);

CREATE INDEX audit_events_run_occurred_idx
    ON audit_events (run_id, occurred_at_ms);

CREATE INDEX audit_events_type_occurred_idx
    ON audit_events (event_type, occurred_at_ms);

CREATE INDEX approvals_status_expires_idx
    ON approvals (status, expires_at_ms);

CREATE INDEX sessions_status_updated_idx
    ON sessions (status, updated_at_ms, id);
