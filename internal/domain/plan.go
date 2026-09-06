package domain

import (
	"errors"
	"strconv"
	"strings"
)

const (
	PlanSchemaVersion  = 1
	MaxPlanSteps       = 12
	MaxPlanLimitations = 8
	MaxPlanTitleBytes  = 512
	MaxPlanStepBytes   = 2048
	MaxPlanBytes       = 32 * 1024
)

// ErrInvalidPlan reports malformed bounded plan content. A Plan is inert
// assistant content and carries no action or execution authority.
var ErrInvalidPlan = errors.New("Plan data is invalid")

// PlanStep is one ordered explanatory step, never an executable instruction.
type PlanStep struct {
	Sequence    int    `json:"sequence"`
	Description string `json:"description"`
}

// Plan is the fixed plan-only terminal result retained with a Diagnosis.
type Plan struct {
	SchemaVersion int        `json:"schema_version"`
	Title         string     `json:"title"`
	Steps         []PlanStep `json:"steps"`
	Limitations   []string   `json:"limitations"`
}

// Validate checks exact order and limits without interpreting plan semantics.
func (plan Plan) Validate() error {
	if plan.SchemaVersion != PlanSchemaVersion ||
		!validPlanLine(plan.Title, MaxPlanTitleBytes) || len(plan.Steps) < 1 || len(plan.Steps) > MaxPlanSteps ||
		len(plan.Limitations) > MaxPlanLimitations {
		return ErrInvalidPlan
	}
	for index, step := range plan.Steps {
		if step.Sequence != index+1 || !validPlanLine(step.Description, MaxPlanStepBytes) {
			return ErrInvalidPlan
		}
	}
	for _, limitation := range plan.Limitations {
		if !validPlanLine(limitation, MaxPlanStepBytes) {
			return ErrInvalidPlan
		}
	}
	if planRenderedBytes(plan) > MaxPlanBytes {
		return ErrInvalidPlan
	}
	return nil
}

// planRenderedBytes mirrors the fixed Markdown projection without allocating
// it, so the public byte ceiling applies to the visible terminal result rather
// than silently excluding otherwise-valid boundary values after validation.
func planRenderedBytes(plan Plan) int {
	total := len("## ") + len(plan.Title) + len("\n\n")
	for _, step := range plan.Steps {
		total += len(strconv.Itoa(step.Sequence)) + len(". ") + len(step.Description) + len("\n")
	}
	if len(plan.Limitations) > 0 {
		total += len("\nLimitations:\n\n")
		for _, limitation := range plan.Limitations {
			total += len("- ") + len(limitation) + len("\n")
		}
	}
	// The fixed renderer removes exactly the final line feed.
	return total - len("\n")
}

func validPlanLine(value string, maximum int) bool {
	return ValidModelText(value, maximum, false) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n")
}
