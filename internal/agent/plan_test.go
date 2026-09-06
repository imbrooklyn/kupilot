package agent

import (
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestRenderPlanMarkdownMatchesTheExactVisibleByteLimit(t *testing.T) {
	steps := make([]domain.PlanStep, domain.MaxPlanSteps)
	for index := range steps {
		steps[index] = domain.PlanStep{Sequence: index + 1, Description: strings.Repeat("s", domain.MaxPlanStepBytes)}
	}
	plan := domain.Plan{
		SchemaVersion: domain.PlanSchemaVersion,
		Title:         strings.Repeat("t", domain.MaxPlanTitleBytes),
		Steps:         steps,
		Limitations: []string{
			strings.Repeat("l", domain.MaxPlanStepBytes),
			strings.Repeat("m", domain.MaxPlanStepBytes),
			strings.Repeat("n", domain.MaxPlanStepBytes),
			strings.Repeat("o", 1454),
		},
	}
	result, err := RenderPlanMarkdown(plan)
	if err != nil || len(result) != domain.MaxPlanBytes {
		t.Fatalf("exact rendered plan = %d bytes, %v", len(result), err)
	}
	plan.Limitations[3] += "o"
	if _, err := RenderPlanMarkdown(plan); err == nil {
		t.Fatal("one-over rendered plan was accepted")
	}
}
