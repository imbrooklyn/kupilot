package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

func projectTranscriptProvenance(provenance application.UIAnswerProvenance) components.AnswerProvenance {
	observed := ""
	if provenance.ObservedFrom != nil && provenance.ObservedThrough != nil {
		observed = "observed " + provenance.ObservedFrom.UTC().Format(time.RFC3339) + "–" +
			provenance.ObservedThrough.UTC().Format(time.RFC3339)
	}
	reasoning := make([]string, 0, 4)
	if provenance.HasInference {
		reasoning = append(reasoning, "inference")
	}
	if provenance.HasUncertainty {
		reasoning = append(reasoning, "uncertainty")
	}
	if provenance.HasConflict {
		reasoning = append(reasoning, "conflict")
	}
	if provenance.HasSuperseded {
		reasoning = append(reasoning, "superseded")
	}
	return components.AnswerProvenance{
		Visible:        true,
		EvidenceCount:  provenance.EvidenceCount,
		ObservedRange:  observed,
		Generation:     fmt.Sprintf("scope %d/policy %d", provenance.ScopeGeneration, provenance.PolicyGeneration),
		Coverage:       string(provenance.CoverageState),
		Reasoning:      strings.Join(reasoning, ", "),
		SourceCoverage: fmt.Sprintf("checked %d/not checked %d", provenance.CheckedSourceCount, provenance.UncheckedSourceCount),
	}
}

func renderTerminalOutcome(outcome application.UITerminalOutcome) string {
	actions := make([]string, len(outcome.NextActions))
	for index, action := range outcome.NextActions {
		actions[index] = terminalActionLabel(action)
	}
	result := "Result: " + strings.ReplaceAll(string(outcome.Reason), "_", " ")
	if len(outcome.Budget) > 0 {
		result += " · Budget snapshot: 17 categories · input " + terminalBudgetValue(outcome.Budget, application.UIBudgetModelInputBytes) +
			" · attempts " + terminalBudgetValue(outcome.Budget, application.UIBudgetModelAttempts) +
			" · Tools " + terminalBudgetValue(outcome.Budget, application.UIBudgetToolCalls) +
			" · Evidence " + terminalBudgetValue(outcome.Budget, application.UIBudgetEvidenceItems) +
			" · wall " + terminalBudgetValue(outcome.Budget, application.UIBudgetWallMilliseconds)
	}
	result += " · Next: " + strings.Join(actions, " · ")
	return result
}

func terminalBudgetValue(values []application.UIBudgetMeasure, category application.UIBudgetCategory) string {
	for _, value := range values {
		if value.Category != category {
			continue
		}
		if value.Basis == application.UIBudgetUnavailable {
			return "unavailable"
		}
		if value.Limit == 0 {
			return fmt.Sprintf("%d (%s)", value.Used, value.Basis)
		}
		return fmt.Sprintf("%d/%d (%s)", value.Used, value.Limit, value.Basis)
	}
	return "unavailable"
}

func terminalActionLabel(action application.UINextAction) string {
	switch action {
	case application.UINextAskNewQuestion:
		return "ask a new question"
	case application.UINextProvideInput:
		return "provide new explicit input"
	case application.UINextEditRecoveredInput:
		return "edit recovered input"
	case application.UINextInspectEvidence:
		return "inspect cited Evidence"
	case application.UINextReviewScopePolicy:
		return "review scope and policy"
	case application.UINextReviewBudget:
		return "review /status budgets"
	case application.UINextRunDoctor:
		return "run /doctor"
	case application.UINextSubmitExplicitly:
		return "submit explicitly"
	case application.UINextDoNotRetryBlindly:
		return "do not retry blindly"
	default:
		return "no automatic action"
	}
}
