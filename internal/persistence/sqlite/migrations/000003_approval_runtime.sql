CREATE TABLE approval_runtime_migration_guard (
    old_schema_is_empty INTEGER PRIMARY KEY CHECK (old_schema_is_empty = 1)
) STRICT;

INSERT INTO approval_runtime_migration_guard (old_schema_is_empty)
SELECT CASE WHEN EXISTS (SELECT id FROM approvals LIMIT 1) THEN 0 ELSE 1 END;

DROP TABLE approval_runtime_migration_guard;
DROP TABLE approvals;

CREATE TABLE approvals (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    operation TEXT NOT NULL CHECK (operation = 'restart_deployment'),
    operation_schema_version TEXT NOT NULL CHECK (
        operation_schema_version = 'restart_deployment/v1'
    ),
    policy_version TEXT NOT NULL CHECK (
        policy_version = 'restart-deployment-approval/v1'
    ),
    scope_context TEXT NOT NULL CHECK (
        length(CAST(scope_context AS BLOB)) BETWEEN 1 AND 253
    ),
    scope_namespace TEXT NOT NULL CHECK (
        length(CAST(scope_namespace AS BLOB)) BETWEEN 1 AND 63
    ),
    scope_generation INTEGER NOT NULL CHECK (scope_generation >= 1),
    target_api_version TEXT NOT NULL CHECK (target_api_version = 'apps/v1'),
    target_kind TEXT NOT NULL CHECK (target_kind = 'Deployment'),
    target_namespace TEXT NOT NULL CHECK (
        target_namespace = scope_namespace
        AND length(CAST(target_namespace AS BLOB)) BETWEEN 1 AND 63
    ),
    deployment_name TEXT NOT NULL CHECK (
        length(CAST(deployment_name AS BLOB)) BETWEEN 1 AND 253
    ),
    deployment_uid TEXT NOT NULL CHECK (
        length(CAST(deployment_uid AS BLOB)) BETWEEN 1 AND 1024
    ),
    template_fingerprint TEXT NOT NULL CHECK (
        length(template_fingerprint) = 64
        AND template_fingerprint NOT GLOB '*[^0-9a-f]*'
    ),
    deployment_generation INTEGER NOT NULL CHECK (deployment_generation >= 1),
    reason_summary TEXT NOT NULL CHECK (
        length(CAST(reason_summary AS BLOB)) BETWEEN 1 AND 512
    ),
    risk_summary TEXT NOT NULL CHECK (
        risk_summary = 'Restarting the Deployment replaces Pods and may temporarily reduce availability.'
    ),
    operation_digest TEXT NOT NULL UNIQUE CHECK (
        length(operation_digest) = 64
        AND operation_digest NOT GLOB '*[^0-9a-f]*'
    ),
    nonce_hash TEXT NOT NULL UNIQUE CHECK (
        length(nonce_hash) = 64
        AND nonce_hash NOT GLOB '*[^0-9a-f]*'
    ),
    status TEXT NOT NULL CHECK (
        status IN (
            'pending',
            'approved',
            'rejected',
            'expired',
            'cancelled',
            'invalidated',
            'consumed'
        )
    ),
    state_reason TEXT NOT NULL,
    requested_at_ms INTEGER NOT NULL CHECK (requested_at_ms >= 0),
    expires_at_ms INTEGER NOT NULL CHECK (expires_at_ms = requested_at_ms + 60000),
    state_changed_at_ms INTEGER NOT NULL CHECK (state_changed_at_ms >= requested_at_ms),
    CHECK (
        (status = 'pending' AND state_reason = '' AND state_changed_at_ms = requested_at_ms)
        OR (status = 'approved' AND state_reason = 'user_approved' AND state_changed_at_ms < expires_at_ms)
        OR (status = 'rejected' AND state_reason = 'user_rejected' AND state_changed_at_ms < expires_at_ms)
        OR (status = 'expired' AND state_reason = 'ttl_expired' AND state_changed_at_ms >= expires_at_ms)
        OR (
            status = 'cancelled'
            AND state_reason IN (
                'user_cancelled',
                'run_cancelled',
                'process_restarted',
                'context_cancelled'
            )
            AND state_changed_at_ms < expires_at_ms
        )
        OR (
            status = 'invalidated'
            AND state_reason IN (
                'scope_changed',
                'digest_mismatch',
                'nonce_mismatch',
                'decision_replayed'
            )
            AND state_changed_at_ms < expires_at_ms
        )
        OR (status = 'consumed' AND state_reason = 'approval_consumed' AND state_changed_at_ms < expires_at_ms)
    ),
    FOREIGN KEY (run_id) REFERENCES agent_runs(id) ON DELETE CASCADE,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
) STRICT;

CREATE TABLE approval_decisions (
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
    actor TEXT NOT NULL CHECK (actor = 'local_user'),
    decided_at_ms INTEGER NOT NULL CHECK (decided_at_ms >= 0),
    FOREIGN KEY (approval_id) REFERENCES approvals(id) ON DELETE CASCADE
) STRICT;

CREATE INDEX approvals_status_expires_idx
    ON approvals (status, expires_at_ms, id);

CREATE INDEX approvals_run_status_idx
    ON approvals (run_id, status, id);
