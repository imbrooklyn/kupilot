CREATE TABLE privacy_consents (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
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
    schema_version INTEGER NOT NULL CHECK (schema_version = 1)
) STRICT;
