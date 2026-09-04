ALTER TABLE evidence_items ADD COLUMN resource_type_json TEXT CHECK (
    resource_type_json IS NULL
    OR (
        json_valid(resource_type_json)
        AND length(CAST(resource_type_json AS BLOB)) <= 4096
    )
);

ALTER TABLE evidence_items ADD COLUMN resource_policy_version TEXT CHECK (
    resource_policy_version IS NULL
    OR resource_policy_version = 'kupilot-resource-policy-v1'
);

ALTER TABLE evidence_items ADD COLUMN policy_generation INTEGER CHECK (
    policy_generation IS NULL OR policy_generation >= 1
);

ALTER TABLE evidence_items ADD COLUMN partial INTEGER NOT NULL DEFAULT 0 CHECK (
    partial IN (0, 1)
);
