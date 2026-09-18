package tui

import (
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestInteractionBoundaryPresentationCannotSelectAuthorityOrExposeUnknownText(t *testing.T) {
	outcome, err := application.ProjectTerminalOutcome(domain.RunTerminalFailed)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []domain.InteractionFailure{domain.FailureFinalJSON, domain.FailureEvidenceUnknown, domain.FailurePersistence} {
		outcome.Diagnostic = failure
		text := renderTerminalOutcome(outcome)
		if !strings.Contains(text, "Boundary: "+string(failure.Stage())+"/"+string(failure)) || !strings.Contains(text, "Next:") {
			t.Fatalf("missing safe diagnostic: %q", text)
		}
	}
	outcome.Diagnostic = "external-canary"
	if text := renderTerminalOutcome(outcome); strings.Contains(text, "external-canary") || strings.Contains(text, "Boundary:") {
		t.Fatalf("unknown diagnostic rendered: %q", text)
	}
}

func TestDoctorDialogUsesFixedInteractionReason(t *testing.T) {
	result, err := application.NewDoctorResult("v0.1.0", "v1", domain.ModelProviderOpenAI, domain.ModelAPIProtocolChatCompletions, domain.SHA256Hex("origin"), true, false, application.SessionStorageHealth{SchemaRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	result.LastInteractionFailure = domain.FailureEvidenceUnknown
	model := NewModel(Config{Width: 120, Height: 40, NoColor: true})
	model.showDoctor(result)
	if frame := model.View().Content; !strings.Contains(frame, "evidence_reference_unknown") || strings.Contains(frame, "http://") {
		t.Fatalf("doctor omitted safe boundary or exposed origin: %q", frame)
	}
}
