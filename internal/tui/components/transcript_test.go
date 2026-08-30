package components

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

func TestToolStepsSeparateCompactStatusFromDetails(t *testing.T) {
	t.Parallel()
	steps := NewToolSteps(ToolStepStyles{})
	steps.Upsert(ToolStep{
		InvocationID:  "invocation-1",
		Name:          "list_resources",
		Purpose:       "List Pods in the active Namespace.",
		Status:        "succeeded",
		Summary:       "Nine projected resources were collected.",
		EvidenceCount: 9,
	})
	want := strings.Join([]string{
		"• List resources · done",
		"    └ List Pods in the active Namespace. → Nine projected resources were collected.",
	}, "\n")
	if got := steps.View(); got != want {
		t.Fatalf("ToolSteps.View() = %q, want %q", got, want)
	}
}

func TestTranscriptMatchesUserSurfaceAndFinalRunTimeline(t *testing.T) {
	t.Parallel()

	surface := lipgloss.NewStyle().Background(lipgloss.Color("#303030"))
	userText := surface
	transcript := NewTranscript(TranscriptStyles{
		UserSurface: surface.Padding(1, 1),
		UserPrompt:  surface.Foreground(lipgloss.Cyan).Bold(true),
		UserText:    userText,
	}, ToolStepStyles{})
	transcript.SetSize(48, 30)
	question := "How many nodes are in the cluster?"
	transcript.AppendUser(question)
	if content := transcript.renderContent(); !strings.Contains(content, "› ") ||
		lipgloss.Height(content) != 3 || !strings.Contains(content, userText.Render(question)) {
		t.Fatalf("historic user surface does not match the three-row composer language: %q", content)
	}

	transcript.StartAgent()
	transcript.UpsertToolStep(ToolStep{
		InvocationID: "invocation-1",
		Name:         "get_cluster_overview",
		Purpose:      "Count the current cluster nodes.",
		Status:       "succeeded",
	})
	transcript.FinishAgentWithDuration("The cluster has three Ready nodes.", 20*time.Second)
	content := transcript.renderContent()
	toolAt := strings.Index(content, "Inspect cluster overview · done")
	detailAt := strings.Index(content, "    └ Count the current cluster nodes.")
	separatorAt := strings.Index(content, strings.Repeat("─", 48))
	answerAt := strings.Index(content, "The cluster has three Ready nodes.")
	timingAt := strings.Index(content, "Worked for 20s")
	if !(toolAt >= 0 && toolAt < detailAt && detailAt < separatorAt && separatorAt < answerAt && answerAt < timingAt) {
		t.Fatalf("terminal run timeline order is invalid: %q", content)
	}
}

func TestTranscriptCollapsesBulkEvidenceWithoutLosingSelection(t *testing.T) {
	t.Parallel()
	transcript := NewTranscript(TranscriptStyles{}, ToolStepStyles{})
	transcript.StartAgent()
	transcript.FinishAgent("A bounded list was collected.")
	references := make([]EvidenceReference, 9)
	for index := range references {
		references[index] = EvidenceReference{
			Index: index,
			ID:    fmt.Sprintf("00000000-0000-7000-8000-%012d", 201+index),
			State: "available",
		}
	}
	references[len(references)-1].State = "partial"
	transcript.SetAgentEvidence(references)
	defaultView := transcript.renderContent()
	for _, reference := range references {
		if strings.Contains(defaultView, reference.ID) {
			t.Fatalf("default transcript exposed internal ID %q: %q", reference.ID, defaultView)
		}
	}
	if strings.Contains(defaultView, "Evidence") || strings.Contains(defaultView, "Observation 1/") {
		t.Fatalf("default transcript exposed provenance controls: %q", defaultView)
	}

	if !transcript.BeginEvidenceSelection() {
		t.Fatal("BeginEvidenceSelection() = false")
	}
	selected := transcript.renderContent()
	if !strings.Contains(selected, "Observation 1/9 · ready") || strings.Contains(selected, references[0].ID) {
		t.Fatalf("first bulk Evidence selection = %q", selected)
	}
	transcript.MoveEvidence(1)
	selected = transcript.renderContent()
	if !strings.Contains(selected, "Observation 2/9 · ready") || strings.Contains(selected, references[1].ID) {
		t.Fatalf("second bulk Evidence selection = %q", selected)
	}
}

func TestToolStepNamesCoverTheFixedCatalogWithoutProtocolIdentifiers(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"get_resource":          "Inspect resource",
		"list_resources":        "List resources",
		"get_events":            "Read events",
		"get_pod_logs":          "Read current logs",
		"get_previous_pod_logs": "Read previous logs",
		"get_related_resources": "Inspect related resources",
		"get_cluster_overview":  "Inspect cluster overview",
	}
	for name, want := range tests {
		if got := toolStepDisplayName(name); got != want || strings.Contains(got, "_") {
			t.Fatalf("toolStepDisplayName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestPickersShowHumanLabelsWithoutRepeatedNamespaceOrInternalPrefixes(t *testing.T) {
	t.Parallel()

	resources := NewResourcePicker(PickerStyles{})
	resources.SetCandidates([]ResourceCandidate{{
		APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "payment-api-7d9", Status: "Pending",
	}})
	resourceView := resources.View()
	if !strings.Contains(resourceView, "Pod/payment-api-7d9 · Pending") || strings.Contains(resourceView, "ns/") {
		t.Fatalf("resource picker = %q", resourceView)
	}

	sessions := NewSessionPicker(PickerStyles{})
	sessions.SetCandidates([]SessionCandidate{{
		ID: "session-1", Title: "Payment diagnosis", Context: "development", Namespace: "payments", Privacy: "standard",
	}})
	sessionView := sessions.View()
	if !strings.Contains(sessionView, "development / payments") || !strings.Contains(sessionView, "history saved") ||
		strings.Contains(sessionView, "ctx/") || strings.Contains(sessionView, "ns/") || strings.Contains(sessionView, "privacy/") {
		t.Fatalf("session picker = %q", sessionView)
	}
}
