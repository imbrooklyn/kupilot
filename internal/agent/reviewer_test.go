package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestReviewerBudgetIsIndependentBoundedAndAtomic(t *testing.T) {
	t.Parallel()

	limits, err := ReviewerBudgetLimitsForProfile(BudgetProfileBalanced)
	if err != nil {
		t.Fatalf("ReviewerBudgetLimitsForProfile() error = %v", err)
	}
	budget, err := NewReviewerBudget(limits)
	if err != nil {
		t.Fatalf("NewReviewerBudget() error = %v", err)
	}
	start := make(chan struct{})
	errorsSeen := make(chan error, limits.Calls+3)
	var wait sync.WaitGroup
	for index := 0; index < limits.Calls+3; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, reserveErr := budget.Reserve(context.Background())
			errorsSeen <- reserveErr
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	succeeded := 0
	for reserveErr := range errorsSeen {
		if reserveErr == nil {
			succeeded++
			continue
		}
		var budgetErr *RunBudgetError
		if !errors.As(reserveErr, &budgetErr) || budgetErr.Reason() != RunStopReviewerCallLimit {
			t.Fatalf("one-over reviewer error = %v", reserveErr)
		}
	}
	snapshot := budget.Snapshot()
	if succeeded != limits.Calls || snapshot.Calls != limits.Calls || snapshot.CostUnits != limits.CostUnits {
		t.Fatalf("reviewer reservations = success %d snapshot %#v", succeeded, snapshot)
	}
}

func TestReviewerBudgetCancellationConsumesNothing(t *testing.T) {
	t.Parallel()

	limits, _ := ReviewerBudgetLimitsForProfile(BudgetProfileCompact)
	budget, err := NewReviewerBudget(limits)
	if err != nil {
		t.Fatalf("NewReviewerBudget() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := budget.Reserve(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reserve(cancelled) error = %v", err)
	}
	if snapshot := budget.Snapshot(); snapshot.Calls != 0 || snapshot.CostUnits != 0 {
		t.Fatalf("cancelled reviewer reservation = %#v", snapshot)
	}
}

func TestReviewerBudgetRejectsExpandedAndAcceptsTightenedLimits(t *testing.T) {
	t.Parallel()

	maximum, _ := ReviewerBudgetLimitsForProfile(BudgetProfileCompact)
	tests := []struct {
		name   string
		mutate func(*ReviewerBudgetLimits)
	}{
		{name: "calls", mutate: func(value *ReviewerBudgetLimits) { value.Calls++ }},
		{name: "request", mutate: func(value *ReviewerBudgetLimits) { value.RequestBytes++ }},
		{name: "output", mutate: func(value *ReviewerBudgetLimits) { value.OutputBytes++ }},
		{name: "cost", mutate: func(value *ReviewerBudgetLimits) { value.CostUnits++ }},
		{name: "timeout", mutate: func(value *ReviewerBudgetLimits) { value.RequestTimeout++ }},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			value := maximum
			current.mutate(&value)
			if value.Validate() == nil {
				t.Fatal("expanded ReviewerBudgetLimits.Validate() error = nil")
			}
		})
	}
	tightened := maximum
	tightened.Calls = 1
	tightened.RequestBytes = 1024
	tightened.OutputBytes = 256
	tightened.CostUnits = 1
	if err := tightened.Validate(); err != nil {
		t.Fatalf("tightened ReviewerBudgetLimits.Validate() error = %v", err)
	}
}
