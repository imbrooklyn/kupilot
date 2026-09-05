CREATE TABLE approvals_v10 (
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
        policy_version = 'kupilot.action-policy/2026-09-04'
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
        'none', 'replica_target', 'revision', 'node_scheduling', 'drain_plan',
        'container_file', 'remote_argv', 'local_argv'
    )),
    parameter_digest TEXT NOT NULL CHECK (
        length(parameter_digest) = 64
        AND parameter_digest NOT GLOB '*[^0-9a-f]*'
    ),
    stdin INTEGER NOT NULL CHECK (stdin IN (0, 1)),
    tty INTEGER NOT NULL CHECK (tty IN (0, 1) AND (tty = 0 OR stdin = 1)),
    shell INTEGER NOT NULL CHECK (shell = 0),
    data_categories INTEGER NOT NULL CHECK (data_categories BETWEEN 1 AND 63),
    allowed_sinks INTEGER NOT NULL CHECK (allowed_sinks BETWEEN 1 AND 15),
    network_effects INTEGER NOT NULL CHECK (
        network_effects BETWEEN 1 AND 31
        AND (network_effects = 1 OR (network_effects & 1) = 0)
    ),
    network_destination_hash TEXT NOT NULL CHECK (
        ((network_effects & 28) = 0 AND network_destination_hash = '')
        OR ((network_effects & 28) != 0
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

INSERT INTO approvals_v10 (
    id, run_id, session_id, envelope_schema_version, digest_version,
    operation, operation_schema_version, policy_version, permission_profile,
    policy_generation, risk, effect, scope_context, scope_namespace,
    namespace_access, scope_generation, target_api_version, target_kind,
    target_namespace, target_name, target_uid, target_resource_version,
    target_subresource, target_fingerprint, target_generation, target_revision,
    target_set_digest, target_count, parameter_kind, parameter_digest,
    stdin, tty, shell, data_categories, allowed_sinks, network_effects,
    network_destination_hash, timeout_ms, maximum_items, maximum_lines,
    maximum_bytes, maximum_output, verification_plan_id, reason_summary,
    risk_summary, operation_digest, nonce_hash, status, state_reason,
    requested_at_ms, expires_at_ms, state_changed_at_ms
)
SELECT
    id, run_id, session_id, envelope_schema_version, digest_version,
    operation, operation_schema_version, policy_version, permission_profile,
    policy_generation, risk, effect, scope_context, scope_namespace,
    namespace_access, scope_generation, target_api_version, target_kind,
    target_namespace, target_name, target_uid, target_resource_version,
    target_subresource, target_fingerprint, target_generation, target_revision,
    target_set_digest, target_count, parameter_kind, parameter_digest,
    stdin, tty, shell, data_categories, allowed_sinks, network_effects,
    network_destination_hash, timeout_ms, maximum_items, maximum_lines,
    maximum_bytes, maximum_output, verification_plan_id, reason_summary,
    risk_summary, operation_digest, nonce_hash, status, state_reason,
    requested_at_ms, expires_at_ms, state_changed_at_ms
FROM approvals;

CREATE TABLE approval_decisions_v10 (
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
    FOREIGN KEY (approval_id) REFERENCES approvals_v10(id) ON DELETE CASCADE
) STRICT;

INSERT INTO approval_decisions_v10 (
    approval_id, shown_digest, nonce_hash, decision, actor,
    permission_disposition, permission_rule_id, reviewer_profile,
    reviewer_origin_hash, rationale_summary, decided_at_ms
)
SELECT
    approval_id, shown_digest, nonce_hash, decision, actor,
    permission_disposition, permission_rule_id, reviewer_profile,
    reviewer_origin_hash, rationale_summary, decided_at_ms
FROM approval_decisions;

CREATE TABLE action_reviews_v10 (
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
        REFERENCES approvals_v10(id, policy_generation) ON DELETE CASCADE
) STRICT;

INSERT INTO action_reviews_v10 (
    approval_id, model_request_id, profile_name, origin_hash,
    policy_generation, disposition, rationale_summary, error_class,
    occurred_at_ms
)
SELECT
    approval_id, model_request_id, profile_name, origin_hash,
    policy_generation, disposition, rationale_summary, error_class,
    occurred_at_ms
FROM action_reviews;

DROP TABLE action_reviews;
DROP TABLE approval_decisions;
DROP TABLE approvals;

ALTER TABLE approvals_v10 RENAME TO approvals;
ALTER TABLE approval_decisions_v10 RENAME TO approval_decisions;
ALTER TABLE action_reviews_v10 RENAME TO action_reviews;

CREATE INDEX approvals_status_expires_idx
    ON approvals (status, expires_at_ms, id);
CREATE INDEX approvals_run_status_idx
    ON approvals (run_id, status, id);
CREATE INDEX approvals_policy_status_idx
    ON approvals (policy_generation, status, id);
CREATE INDEX action_reviews_approval_time_idx
    ON action_reviews (approval_id, occurred_at_ms, model_request_id);

CREATE TABLE tool_invocations_v10 (
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

INSERT INTO tool_invocations_v10 (
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

ALTER TABLE evidence_items RENAME TO evidence_items_v9;

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
            'kupilot.observability-policy/2026-09-05.v1',
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
    FOREIGN KEY (invocation_id) REFERENCES tool_invocations_v10(id) ON DELETE CASCADE
) STRICT;

INSERT INTO evidence_items (
    id, run_id, invocation_id, category, resource_ref_json, fact,
    source_path, severity, resource_version, redaction_count, truncated,
    fingerprint, observed_at_ms, resource_type_json, resource_policy_version,
    policy_generation, partial, source_origin_hash, series, observed_from_ms,
    observed_through_ms
)
SELECT
    id, run_id, invocation_id, category, resource_ref_json, fact,
    source_path, severity, resource_version, redaction_count, truncated,
    fingerprint, observed_at_ms, resource_type_json, resource_policy_version,
    policy_generation, partial, source_origin_hash, series, observed_from_ms,
    observed_through_ms
FROM evidence_items_v9;

DROP TABLE evidence_items_v9;

DROP TABLE tool_invocations;
ALTER TABLE tool_invocations_v10 RENAME TO tool_invocations;

CREATE INDEX evidence_items_run_category_idx ON evidence_items (run_id, category);
CREATE INDEX evidence_items_invocation_idx ON evidence_items (invocation_id);
