-- Initial unreleased v0.1.0 schema. Correct this baseline in place until release.

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
,
    last_activity_at_ms INTEGER NOT NULL DEFAULT 0) STRICT;

CREATE TABLE "agent_runs" (
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

CREATE TABLE "messages" (
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
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0), run_sequence INTEGER CHECK (
    run_sequence IS NULL OR run_sequence BETWEEN 0 AND 9
),
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE SET NULL
        DEFERRABLE INITIALLY DEFERRED
) STRICT;

CREATE TABLE "model_requests" (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 64),
    provider_kind TEXT NOT NULL CHECK (provider_kind = 'openai'),
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
    profile_name TEXT NOT NULL DEFAULT 'agent' CHECK (
        length(CAST(profile_name AS BLOB)) BETWEEN 1 AND 128
    ),
    model_role TEXT NOT NULL DEFAULT 'agent' CHECK (
        model_role IN ('agent', 'approval_reviewer')
    ),
    invocation TEXT NOT NULL DEFAULT 'agent' CHECK (
        invocation IN ('agent', 'agent_summary', 'approval_review')
    ),
    reserved_cost_units INTEGER NOT NULL DEFAULT 1 CHECK (
        reserved_cost_units BETWEEN 1 AND 64
    ),
    UNIQUE (run_id, sequence),
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE "tool_invocations" (
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
            'get_pod_metrics',
            'get_node_metrics',
            'query_prometheus',
            'query_loki',
            'get_related_resources',
            'get_cluster_overview',
            'pod_exec',
            'read_container_file',
            'run_diagnostic_pod'
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
    category TEXT NOT NULL CHECK (length(CAST(category AS BLOB)) BETWEEN 1 AND 64),
    resource_ref_json TEXT NOT NULL CHECK (
        json_valid(resource_ref_json)
        AND length(CAST(resource_ref_json AS BLOB)) <= 4096
    ),
    fact TEXT NOT NULL CHECK (length(CAST(fact AS BLOB)) BETWEEN 1 AND 2048),
    source_path TEXT CHECK (
        source_path IS NULL OR length(CAST(source_path AS BLOB)) <= 1024
    ),
    severity TEXT CHECK (severity IS NULL OR severity IN ('info', 'warning', 'critical')),
    resource_version TEXT CHECK (
        resource_version IS NULL OR length(CAST(resource_version AS BLOB)) <= 256
    ),
    redaction_count INTEGER NOT NULL DEFAULT 0 CHECK (redaction_count >= 0),
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    fingerprint TEXT NOT NULL CHECK (
        length(fingerprint) = 64 AND fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    observed_at_ms INTEGER NOT NULL CHECK (observed_at_ms >= 0),
    resource_type_json TEXT CHECK (
        resource_type_json IS NULL
        OR (json_valid(resource_type_json) AND length(CAST(resource_type_json AS BLOB)) <= 4096)
    ),
    resource_policy_version TEXT CHECK (
        resource_policy_version IS NULL
        OR resource_policy_version IN (
            'kupilot-resource-policy-v1',
            'kupilot.observability-policy/v1',
            'kupilot.remote-diagnostics-policy/v1'
        )
    ),
    policy_generation INTEGER CHECK (policy_generation IS NULL OR policy_generation >= 1),
    partial INTEGER NOT NULL DEFAULT 0 CHECK (partial IN (0, 1)),
    source_origin_hash TEXT NOT NULL DEFAULT '' CHECK (
        source_origin_hash = ''
        OR (length(source_origin_hash) = 64 AND source_origin_hash NOT GLOB '*[^0-9a-f]*')
    ),
    series TEXT NOT NULL DEFAULT '' CHECK (length(CAST(series AS BLOB)) <= 512),
    observed_from_ms INTEGER CHECK (observed_from_ms IS NULL OR observed_from_ms >= 0),
    observed_through_ms INTEGER CHECK (observed_through_ms IS NULL OR observed_through_ms >= 0),
    CHECK ((observed_from_ms IS NULL) = (observed_through_ms IS NULL)),
    CHECK (observed_from_ms IS NULL OR observed_through_ms >= observed_from_ms),
    CHECK (observed_through_ms IS NULL OR observed_at_ms >= observed_through_ms),
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
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0), claim_coverage_json TEXT CHECK (
    claim_coverage_json IS NULL
    OR (
        json_valid(claim_coverage_json)
        AND length(CAST(claim_coverage_json AS BLOB)) <= 131072
    )
), plan_json TEXT CHECK (
    plan_json IS NULL
    OR (
        json_valid(plan_json)
        AND length(CAST(plan_json AS BLOB)) <= 131072
    )
), answer_manifest_json TEXT CHECK (
    answer_manifest_json IS NULL
    OR (
        json_valid(answer_manifest_json)
        AND length(CAST(answer_manifest_json AS BLOB)) <= 131072
    )
), clarification_json TEXT CHECK (
    clarification_json IS NULL
    OR (
        json_valid(clarification_json)
        AND length(CAST(clarification_json AS BLOB)) <= 16384
    )
),
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

CREATE TABLE privacy_consents (
    role TEXT PRIMARY KEY CHECK (role IN ('agent', 'approval_reviewer')),
    policy_version TEXT NOT NULL CHECK (length(CAST(policy_version AS BLOB)) BETWEEN 1 AND 64),
    origin_hash TEXT NOT NULL CHECK (
        length(origin_hash) = 64 AND origin_hash NOT GLOB '*[^0-9a-f]*'
    ),
    prometheus_origin_hash TEXT NOT NULL DEFAULT '' CHECK (
        prometheus_origin_hash = ''
        OR (length(prometheus_origin_hash) = 64 AND prometheus_origin_hash NOT GLOB '*[^0-9a-f]*')
    ),
    loki_origin_hash TEXT NOT NULL DEFAULT '' CHECK (
        loki_origin_hash = ''
        OR (length(loki_origin_hash) = 64 AND loki_origin_hash NOT GLOB '*[^0-9a-f]*')
    ),
    categories_json TEXT NOT NULL CHECK (
        json_valid(categories_json)
        AND json_type(categories_json) = 'array'
        AND length(CAST(categories_json AS BLOB)) BETWEEN 2 AND 2048
    ),
    decision TEXT NOT NULL CHECK (decision IN ('pending', 'accepted', 'rejected', 'revoked')),
    decided_at_ms INTEGER NOT NULL CHECK (decided_at_ms >= 0),
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;

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
        policy_version = 'safe-conversation-context/v1'
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

CREATE TABLE "approvals" (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    envelope_schema_version TEXT NOT NULL CHECK (
        envelope_schema_version = 'kupilot.action-envelope/v1'
    ),
    digest_version TEXT NOT NULL CHECK (
        digest_version = 'kupilot.action-digest/v1'
    ),
    operation TEXT NOT NULL CHECK (operation IN (
        'resource_get', 'resource_list', 'resource_describe', 'resource_query',
        'events', 'logs_current', 'logs_previous', 'logs_all_containers',
        'log_search', 'pod_metrics', 'node_metrics', 'prometheus_query',
        'loki_query', 'container_file_read', 'pod_diagnostic', 'pod_exec',
        'diagnostic_pod', 'restart_deployment', 'scale_workload',
        'rollback_deployment', 'delete_owned_pod', 'cordon_node',
        'uncordon_node', 'drain_node', 'restricted_local_argv', 'shell'
    )),
    operation_schema_version TEXT NOT NULL CHECK (
        operation_schema_version = operation || '/v1'
    ),
    policy_version TEXT NOT NULL CHECK (
        policy_version = 'kupilot.action-policy/v1'
    ),
    permission_profile TEXT NOT NULL CHECK (
        permission_profile IN ('read-only', 'ask', 'auto-review', 'full-access', 'custom')
    ),
    policy_generation INTEGER NOT NULL CHECK (policy_generation >= 1),
    risk TEXT NOT NULL CHECK (risk IN ('safe', 'review', 'critical')),
    effect TEXT NOT NULL CHECK (effect IN (
        'safe_read', 'sensitive_read', 'network_egress', 'remote_execute',
        'local_execute', 'cluster_mutation'
    )),
    scope_context TEXT NOT NULL CHECK (
        length(CAST(scope_context AS BLOB)) BETWEEN 1 AND 253
    ),
    scope_namespace TEXT NOT NULL CHECK (
        length(CAST(scope_namespace AS BLOB)) BETWEEN 1 AND 63
    ),
    namespace_access TEXT NOT NULL CHECK (namespace_access IN ('current', 'all')),
    scope_generation INTEGER NOT NULL CHECK (scope_generation >= 1),
    target_api_version TEXT NOT NULL CHECK (
        length(CAST(target_api_version AS BLOB)) BETWEEN 1 AND 253
    ),
    target_kind TEXT NOT NULL CHECK (
        length(CAST(target_kind AS BLOB)) BETWEEN 1 AND 253
    ),
    target_namespace TEXT NOT NULL CHECK (
        length(CAST(target_namespace AS BLOB)) <= 63
    ),
    target_name TEXT NOT NULL CHECK (
        length(CAST(target_name AS BLOB)) BETWEEN 1 AND 253
    ),
    target_uid TEXT NOT NULL CHECK (
        length(CAST(target_uid AS BLOB)) BETWEEN 1 AND 256
    ),
    target_resource_version TEXT NOT NULL CHECK (
        length(CAST(target_resource_version AS BLOB)) BETWEEN 1 AND 256
    ),
    target_subresource TEXT NOT NULL CHECK (
        length(CAST(target_subresource AS BLOB)) <= 64
    ),
    target_fingerprint TEXT NOT NULL CHECK (
        target_fingerprint = ''
        OR (
            length(target_fingerprint) = 64
            AND target_fingerprint NOT GLOB '*[^0-9a-f]*'
        )
    ),
    target_generation INTEGER NOT NULL CHECK (target_generation >= 0),
    target_revision INTEGER NOT NULL CHECK (target_revision >= 0),
    target_set_digest TEXT NOT NULL,
    target_count INTEGER NOT NULL CHECK (
        (target_count = 0 AND target_set_digest = '')
        OR (
            target_count BETWEEN 1 AND 4096
            AND length(target_set_digest) = 64
            AND target_set_digest NOT GLOB '*[^0-9a-f]*'
        )
    ),
    parameter_kind TEXT NOT NULL CHECK (parameter_kind IN (
        'none', 'replica_target', 'revision', 'node_scheduling', 'pod_delete',
        'drain_plan', 'container_file', 'remote_argv', 'local_argv',
        'shell_command', 'observation'
    )),
    parameter_digest TEXT NOT NULL CHECK (
        length(parameter_digest) = 64
        AND parameter_digest NOT GLOB '*[^0-9a-f]*'
    ),
    stdin INTEGER NOT NULL CHECK (stdin IN (0, 1)),
    tty INTEGER NOT NULL CHECK (tty IN (0, 1) AND (tty = 0 OR stdin = 1)),
    shell INTEGER NOT NULL CHECK (
        (operation = 'shell' AND parameter_kind = 'shell_command' AND shell = 1)
        OR (operation != 'shell' AND parameter_kind != 'shell_command' AND shell = 0)
    ),
    data_categories INTEGER NOT NULL CHECK (data_categories BETWEEN 1 AND 63),
    allowed_sinks INTEGER NOT NULL CHECK (allowed_sinks BETWEEN 1 AND 15),
    network_effects INTEGER NOT NULL CHECK (
        network_effects BETWEEN 1 AND 63
        AND (network_effects = 1 OR (network_effects & 1) = 0)
    ),
    network_destination_hash TEXT NOT NULL CHECK (
        ((network_effects & 60) = 0 AND network_destination_hash = '')
        OR ((network_effects & 60) != 0
            AND length(network_destination_hash) = 64
            AND network_destination_hash NOT GLOB '*[^0-9a-f]*')
    ),
    timeout_ms INTEGER NOT NULL CHECK (timeout_ms > 0 AND timeout_ms <= 1800000),
    maximum_items INTEGER NOT NULL CHECK (maximum_items BETWEEN 0 AND 4096),
    maximum_lines INTEGER NOT NULL CHECK (maximum_lines BETWEEN 0 AND 100000),
    maximum_bytes INTEGER NOT NULL CHECK (maximum_bytes BETWEEN 0 AND 16777216),
    maximum_output INTEGER NOT NULL CHECK (maximum_output BETWEEN 0 AND 16777216),
    verification_plan_id TEXT NOT NULL CHECK (
        length(CAST(verification_plan_id AS BLOB)) BETWEEN 1 AND 128
    ),
    reason_summary TEXT NOT NULL CHECK (
        length(CAST(reason_summary AS BLOB)) BETWEEN 1 AND 512
    ),
    risk_summary TEXT NOT NULL CHECK (
        length(CAST(risk_summary AS BLOB)) BETWEEN 1 AND 1024
    ),
    operation_digest TEXT NOT NULL UNIQUE CHECK (
        length(operation_digest) = 64
        AND operation_digest NOT GLOB '*[^0-9a-f]*'
    ),
    nonce_hash TEXT NOT NULL UNIQUE CHECK (
        length(nonce_hash) = 64
        AND nonce_hash NOT GLOB '*[^0-9a-f]*'
    ),
    status TEXT NOT NULL CHECK (status IN (
        'pending', 'approved', 'rejected', 'expired', 'cancelled',
        'invalidated', 'consumed'
    )),
    state_reason TEXT NOT NULL,
    requested_at_ms INTEGER NOT NULL CHECK (requested_at_ms >= 0),
    expires_at_ms INTEGER NOT NULL CHECK (expires_at_ms = requested_at_ms + 60000),
    state_changed_at_ms INTEGER NOT NULL CHECK (state_changed_at_ms >= requested_at_ms),
    CHECK (
        (status = 'pending' AND state_reason = '' AND state_changed_at_ms = requested_at_ms)
        OR (status = 'approved' AND state_reason IN (
            'user_approved', 'policy_approved', 'reviewer_approved', 'session_rule_approved'
        ) AND state_changed_at_ms < expires_at_ms)
        OR (status = 'rejected' AND state_reason IN (
            'user_rejected', 'reviewer_rejected'
        ) AND state_changed_at_ms < expires_at_ms)
        OR (status = 'expired' AND state_reason = 'ttl_expired' AND state_changed_at_ms >= expires_at_ms)
        OR (status = 'cancelled' AND state_reason IN (
            'user_cancelled', 'run_cancelled', 'process_restarted', 'context_cancelled'
        ) AND state_changed_at_ms < expires_at_ms)
        OR (status = 'invalidated' AND state_reason IN (
            'scope_changed', 'policy_changed', 'target_changed', 'digest_mismatch',
            'nonce_mismatch', 'decision_replayed'
        ) AND state_changed_at_ms < expires_at_ms)
        OR (status = 'consumed' AND state_reason = 'approval_consumed' AND state_changed_at_ms < expires_at_ms)
    ),
    CHECK (
        (risk = 'safe' AND effect = 'safe_read')
        OR (risk = 'review' AND effect != 'safe_read')
        OR (risk = 'critical' AND effect IN (
            'network_egress', 'remote_execute', 'local_execute', 'cluster_mutation'
        ))
    ),
    UNIQUE (id, policy_generation),
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE "approval_decisions" (
    approval_id TEXT PRIMARY KEY,
    shown_digest TEXT NOT NULL CHECK (
        length(shown_digest) = 64
        AND shown_digest NOT GLOB '*[^0-9a-f]*'
    ),
    nonce_hash TEXT NOT NULL CHECK (
        length(nonce_hash) = 64
        AND nonce_hash NOT GLOB '*[^0-9a-f]*'
    ),
    decision TEXT NOT NULL CHECK (decision IN ('approve', 'reject')),
    actor TEXT NOT NULL CHECK (actor IN (
        'local_user', 'permission_policy', 'approval_reviewer', 'session_rule'
    )),
    permission_disposition TEXT NOT NULL CHECK (permission_disposition IN (
        'automatic', 'human', 'reviewer'
    )),
    permission_rule_id TEXT,
    reviewer_profile TEXT,
    reviewer_origin_hash TEXT,
    rationale_summary TEXT,
    decided_at_ms INTEGER NOT NULL CHECK (decided_at_ms >= 0),
    CHECK (
        (actor = 'local_user' AND permission_disposition = 'human')
        OR (actor = 'approval_reviewer' AND permission_disposition = 'reviewer')
        OR (actor IN ('permission_policy', 'session_rule') AND permission_disposition = 'automatic')
    ),
    CHECK (actor NOT IN ('permission_policy', 'session_rule') OR decision = 'approve'),
    CHECK (permission_rule_id IS NULL OR length(permission_rule_id) = 36),
    CHECK (reviewer_profile IS NULL OR (
        length(CAST(reviewer_profile AS BLOB)) BETWEEN 1 AND 128
    )),
    CHECK (reviewer_origin_hash IS NULL OR (
        length(reviewer_origin_hash) = 64
        AND reviewer_origin_hash NOT GLOB '*[^0-9a-f]*'
    )),
    CHECK (rationale_summary IS NULL OR (
        length(CAST(rationale_summary AS BLOB)) BETWEEN 1 AND 2048
    )),
    CHECK (
        (actor = 'local_user'
            AND permission_rule_id IS NULL
            AND reviewer_profile IS NULL
            AND reviewer_origin_hash IS NULL
            AND rationale_summary IS NULL)
        OR (actor = 'permission_policy'
            AND permission_rule_id IS NULL
            AND reviewer_profile IS NULL
            AND reviewer_origin_hash IS NULL
            AND rationale_summary IS NULL)
        OR (actor = 'session_rule'
            AND permission_rule_id IS NOT NULL
            AND reviewer_profile IS NULL
            AND reviewer_origin_hash IS NULL
            AND rationale_summary IS NULL)
        OR (actor = 'approval_reviewer'
            AND permission_rule_id IS NULL
            AND reviewer_profile IS NOT NULL
            AND reviewer_origin_hash IS NOT NULL
            AND rationale_summary IS NOT NULL)
    ),
    FOREIGN KEY (approval_id) REFERENCES approvals(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE "action_reviews" (
    approval_id TEXT NOT NULL,
    model_request_id TEXT PRIMARY KEY NOT NULL CHECK (length(model_request_id) = 36),
    profile_name TEXT NOT NULL CHECK (
        length(CAST(profile_name AS BLOB)) BETWEEN 1 AND 128
    ),
    origin_hash TEXT NOT NULL CHECK (
        length(origin_hash) = 64
        AND origin_hash NOT GLOB '*[^0-9a-f]*'
    ),
    policy_generation INTEGER NOT NULL CHECK (policy_generation >= 1),
    disposition TEXT NOT NULL CHECK (
        disposition IN ('approve', 'deny', 'escalate_to_user', 'failed', 'cancelled', 'timed_out')
    ),
    rationale_summary TEXT CHECK (
        rationale_summary IS NULL
        OR length(CAST(rationale_summary AS BLOB)) BETWEEN 1 AND 2048
    ),
    error_class TEXT CHECK (error_class IS NULL OR error_class IN (
        'invalid_input', 'configuration_invalid', 'consent_required',
        'authentication_failed', 'permission_denied', 'not_found', 'conflict',
        'unsupported', 'policy_denied', 'stale_scope', 'budget_exhausted',
        'rate_limited', 'unavailable', 'timeout', 'cancelled',
        'sensitive_output_blocked', 'invalid_external_response',
        'persistence_unavailable', 'internal'
    )),
    occurred_at_ms INTEGER NOT NULL CHECK (occurred_at_ms >= 0),
    CHECK (
        (disposition IN ('approve', 'deny', 'escalate_to_user')
            AND rationale_summary IS NOT NULL AND error_class IS NULL)
        OR (disposition IN ('failed', 'cancelled', 'timed_out')
            AND rationale_summary IS NULL AND error_class IS NOT NULL)
    ),
    FOREIGN KEY (approval_id, policy_generation)
        REFERENCES approvals(id, policy_generation) ON DELETE CASCADE
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
    schema_version INTEGER NOT NULL CHECK (schema_version = 1),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= 0)
) STRICT;

CREATE INDEX action_reviews_approval_time_idx
    ON action_reviews (approval_id, occurred_at_ms, model_request_id);

CREATE INDEX agent_runs_session_started_idx
    ON agent_runs (session_id, started_at_ms, id);

CREATE INDEX agent_runs_status_idx
    ON agent_runs (status);

CREATE INDEX approvals_policy_status_idx
    ON approvals (policy_generation, status, id);

CREATE INDEX approvals_run_status_idx
    ON approvals (run_id, status, id);

CREATE INDEX approvals_status_expires_idx
    ON approvals (status, expires_at_ms, id);

CREATE INDEX audit_events_run_occurred_idx
    ON audit_events (run_id, occurred_at_ms);

CREATE INDEX audit_events_session_occurred_idx
    ON audit_events (session_id, occurred_at_ms);

CREATE INDEX audit_events_type_occurred_idx
    ON audit_events (event_type, occurred_at_ms);

CREATE INDEX evidence_items_invocation_idx ON evidence_items (invocation_id);

CREATE INDEX evidence_items_run_category_idx ON evidence_items (run_id, category);

CREATE UNIQUE INDEX messages_run_sequence_idx
    ON messages (run_id, run_sequence)
    WHERE run_id IS NOT NULL AND run_sequence IS NOT NULL;

CREATE INDEX messages_session_created_idx
    ON messages (session_id, created_at_ms, id);

CREATE INDEX sessions_last_activity_idx
    ON sessions (last_activity_at_ms DESC, id DESC);

CREATE INDEX sessions_status_updated_idx
    ON sessions (status, updated_at_ms, id);
