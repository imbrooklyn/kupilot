CREATE TABLE model_requests_v17 (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 64),
    provider_kind TEXT NOT NULL CHECK (provider_kind IN ('openai', 'ollama')),
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

INSERT INTO model_requests_v17 (
    id, run_id, sequence, provider_kind, endpoint_origin_hash,
    model, status, error_class, provider_request_id,
    prompt_version, prompt_fingerprint, response_fingerprint,
    input_tokens, output_tokens, latency_ms, started_at_ms, finished_at_ms,
    profile_name, model_role, invocation, reserved_cost_units
)
SELECT
    id, run_id, sequence,
    CASE provider_kind WHEN 'openai_compatible' THEN 'openai' ELSE provider_kind END,
    endpoint_origin_hash,
    model, status, error_class, provider_request_id,
    prompt_version, prompt_fingerprint, response_fingerprint,
    input_tokens, output_tokens, latency_ms, started_at_ms, finished_at_ms,
    profile_name, model_role, invocation, reserved_cost_units
FROM model_requests;

DROP TABLE model_requests;
ALTER TABLE model_requests_v17 RENAME TO model_requests;
