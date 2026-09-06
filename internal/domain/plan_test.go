package domain

import (
	"strings"
	"testing"
)

func boundedPlan(steps int) Plan {
	values := make([]PlanStep, steps)
	for index := range values {
		values[index] = PlanStep{Sequence: index + 1, Description: "Collect one bounded observation."}
	}
	return Plan{
		SchemaVersion: PlanSchemaVersion,
		Title:         "Bounded diagnostic plan",
		Steps:         values,
		Limitations:   []string{"This plan carries no execution authority."},
	}
}

func TestPlanValidatesExactStepAndAggregateBounds(t *testing.T) {
	if err := boundedPlan(MaxPlanSteps).Validate(); err != nil {
		t.Fatalf("exact step limit error = %v", err)
	}
	overSteps := boundedPlan(MaxPlanSteps + 1)
	if err := overSteps.Validate(); err == nil {
		t.Fatal("one-over step limit was accepted")
	}

	exactBytes := boundedPlan(MaxPlanSteps)
	exactBytes.Title = strings.Repeat("t", MaxPlanTitleBytes)
	for index := range exactBytes.Steps {
		exactBytes.Steps[index].Description = strings.Repeat("s", MaxPlanStepBytes)
	}
	exactBytes.Limitations = []string{
		strings.Repeat("l", MaxPlanStepBytes),
		strings.Repeat("m", MaxPlanStepBytes),
		strings.Repeat("n", MaxPlanStepBytes),
		strings.Repeat("o", 1454),
	}
	if err := exactBytes.Validate(); err != nil {
		t.Fatalf("exact aggregate byte limit error = %v", err)
	}
	exactBytes.Limitations[3] += "o"
	if err := exactBytes.Validate(); err == nil {
		t.Fatal("one-over aggregate byte limit was accepted")
	}
}

func TestPlanRejectsMalformedOrderAndMultilineContent(t *testing.T) {
	for _, current := range []struct {
		name   string
		mutate func(*Plan)
	}{
		{name: "schema", mutate: func(plan *Plan) { plan.SchemaVersion++ }},
		{name: "missing steps", mutate: func(plan *Plan) { plan.Steps = nil }},
		{name: "out of order", mutate: func(plan *Plan) { plan.Steps[0].Sequence = 2 }},
		{name: "multiline title", mutate: func(plan *Plan) { plan.Title = "First\nSecond" }},
		{name: "multiline step", mutate: func(plan *Plan) { plan.Steps[0].Description = "First\rSecond" }},
		{name: "trimmed meaning", mutate: func(plan *Plan) { plan.Limitations[0] += " " }},
	} {
		t.Run(current.name, func(t *testing.T) {
			plan := boundedPlan(1)
			current.mutate(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatalf("invalid plan was accepted: %#v", plan)
			}
		})
	}
}
