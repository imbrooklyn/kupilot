ALTER TABLE evidence_items RENAME TO evidence_items_v8;

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
            'kupilot.observability-policy/2026-09-05.v1'
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

INSERT INTO evidence_items (
    id, run_id, invocation_id, category, resource_ref_json, fact,
    source_path, severity, resource_version, redaction_count, truncated,
    fingerprint, observed_at_ms, resource_type_json, resource_policy_version,
    policy_generation, partial
)
SELECT
    id, run_id, invocation_id, category, resource_ref_json, fact,
    source_path, severity, resource_version, redaction_count, truncated,
    fingerprint, observed_at_ms, resource_type_json, resource_policy_version,
    policy_generation, partial
FROM evidence_items_v8;

DROP TABLE evidence_items_v8;

CREATE INDEX evidence_items_run_category_idx ON evidence_items (run_id, category);
CREATE INDEX evidence_items_invocation_idx ON evidence_items (invocation_id);

ALTER TABLE privacy_consents RENAME TO privacy_consents_v2;

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
    schema_version INTEGER NOT NULL CHECK (schema_version = 3)
) STRICT;

INSERT INTO privacy_consents (
    role, policy_version, origin_hash, prometheus_origin_hash,
    loki_origin_hash, categories_json, decision, decided_at_ms, schema_version
)
SELECT
    role,
    policy_version,
    origin_hash,
    '',
    '',
    CASE
        WHEN instr(categories_json, 'redacted_container_output') > 0 THEN
            '["user_question","safe_conversation_context","resource_names_and_references","projected_kubernetes_status","projected_kubernetes_events","redacted_container_output","projected_kubernetes_metrics"]'
        ELSE
            '["user_question","safe_conversation_context","resource_names_and_references","projected_kubernetes_status","projected_kubernetes_events","projected_kubernetes_metrics"]'
    END,
    'pending',
    decided_at_ms,
    3
FROM privacy_consents_v2;

DROP TABLE privacy_consents_v2;
