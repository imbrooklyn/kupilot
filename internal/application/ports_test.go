package application

import (
	"testing"
	"time"
)

func TestIdentifierGeneratorCreatesDistinctCanonicalUUIDv7Values(t *testing.T) {
	t.Parallel()
	generator, err := NewIdentifierGenerator(func() time.Time { return time.UnixMilli(1_700_000_000_000).UTC() })
	if err != nil {
		t.Fatalf("NewIdentifierGenerator() error = %v", err)
	}
	sessionID, sessionErr := generator.NewSessionID()
	messageID, messageErr := generator.NewMessageID()
	runID, runErr := generator.NewAgentRunID()
	auditID, auditErr := generator.NewAuditEventID()
	modelID, modelErr := generator.NewModelRequestID()
	toolID, toolErr := generator.NewToolInvocationID()
	diagnosisID, diagnosisErr := generator.NewDiagnosisID()
	evidenceID, evidenceErr := generator.NewEvidenceID()
	if sessionErr != nil || messageErr != nil || runErr != nil || auditErr != nil || modelErr != nil ||
		toolErr != nil || diagnosisErr != nil || evidenceErr != nil {
		t.Fatal("identifier generation returned an error")
	}
	values := []string{
		string(sessionID), string(messageID), string(runID), string(auditID),
		string(modelID), string(toolID), string(diagnosisID), string(evidenceID),
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if len(value) != 36 || value[14] != '7' {
			t.Fatalf("generated identifier = %q", value)
		}
		if _, duplicate := seen[value]; duplicate {
			t.Fatalf("duplicate identifier = %q", value)
		}
		seen[value] = struct{}{}
	}
}
