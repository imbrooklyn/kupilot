ALTER TABLE diagnoses ADD COLUMN claim_coverage_json TEXT CHECK (
    claim_coverage_json IS NULL
    OR (
        json_valid(claim_coverage_json)
        AND length(CAST(claim_coverage_json AS BLOB)) <= 131072
    )
);

ALTER TABLE diagnoses ADD COLUMN plan_json TEXT CHECK (
    plan_json IS NULL
    OR (
        json_valid(plan_json)
        AND length(CAST(plan_json AS BLOB)) <= 131072
    )
);
