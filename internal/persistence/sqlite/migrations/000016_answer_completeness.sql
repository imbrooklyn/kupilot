ALTER TABLE diagnoses ADD COLUMN answer_manifest_json TEXT CHECK (
    answer_manifest_json IS NULL
    OR (
        json_valid(answer_manifest_json)
        AND length(CAST(answer_manifest_json AS BLOB)) <= 131072
    )
);

ALTER TABLE diagnoses ADD COLUMN clarification_json TEXT CHECK (
    clarification_json IS NULL
    OR (
        json_valid(clarification_json)
        AND length(CAST(clarification_json AS BLOB)) <= 16384
    )
);
