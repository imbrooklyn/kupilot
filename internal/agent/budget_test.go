package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestRunBudgetEnforcesExactHardLimits(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")

	t.Run("steps", func(t *testing.T) {
		clock := newFakeClock()
		budget, err := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		if err != nil {
			t.Fatalf("NewRunBudget() error = %v", err)
		}
		for index := 0; index < domain.MaxAgentSteps; index++ {
			if err := budget.ReserveStep(context.Background()); err != nil {
				t.Fatalf("ReserveStep(%d) error = %v", index, err)
			}
			if err := budget.CompleteStep(1); err != nil {
				t.Fatalf("CompleteStep(%d) error = %v", index, err)
			}
		}
		assertBudgetStop(t, budget.ReserveStep(context.Background()), RunStopStepLimit)
		assertBudgetStop(t, firstError(budget.ReserveModelCall(context.Background())), RunStopStepLimit)
	})

	t.Run("model calls", func(t *testing.T) {
		clock := newFakeClock()
		budget, err := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		if err != nil {
			t.Fatalf("NewRunBudget() error = %v", err)
		}
		for index := 0; index < domain.MaxAgentModelCalls; index++ {
			reservation, reserveErr := budget.ReserveModelCall(context.Background())
			if reserveErr != nil {
				t.Fatalf("ReserveModelCall(%d) error = %v", index, reserveErr)
			}
			if reservation.Timeout > domain.MaxModelRequestTimeout {
				t.Fatalf("model timeout = %s", reservation.Timeout)
			}
		}
		assertBudgetStop(t, firstError(budget.ReserveModelCall(context.Background())), RunStopModelCallLimit)
	})

	t.Run("Tool calls", func(t *testing.T) {
		clock := newFakeClock()
		budget, err := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		if err != nil {
			t.Fatalf("NewRunBudget() error = %v", err)
		}
		for index := 0; index < domain.MaxAgentToolCalls; index++ {
			call := testBoundCall(t, input, invocationID(index), fmt.Sprintf("sample-pod-%d", index))
			if _, reserveErr := budget.ReserveToolCall(context.Background(), call); reserveErr != nil {
				t.Fatalf("ReserveToolCall(%d) error = %v", index, reserveErr)
			}
			if completeErr := budget.CompleteToolCall(call, ToolCallOutcome{}); completeErr != nil {
				t.Fatalf("CompleteToolCall(%d) error = %v", index, completeErr)
			}
		}
		call := testBoundCall(t, input, invocationID(domain.MaxAgentToolCalls), "one-more-pod")
		assertBudgetStop(t, firstError(budget.ReserveToolCall(context.Background(), call)), RunStopToolCallLimit)
	})

	t.Run("cumulative Tool bytes", func(t *testing.T) {
		clock := newFakeClock()
		budget, err := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		if err != nil {
			t.Fatalf("NewRunBudget() error = %v", err)
		}
		for index := 0; index < domain.MaxAgentRunToolResultBytes/domain.MaxToolResultBytes; index++ {
			call := testBoundCall(t, input, invocationID(index), fmt.Sprintf("sample-pod-%d", index))
			if _, reserveErr := budget.ReserveToolCall(context.Background(), call); reserveErr != nil {
				t.Fatalf("ReserveToolCall(%d) error = %v", index, reserveErr)
			}
			if completeErr := budget.CompleteToolCall(call, ToolCallOutcome{ResultBytes: domain.MaxToolResultBytes}); completeErr != nil {
				t.Fatalf("CompleteToolCall(%d) error = %v", index, completeErr)
			}
		}
		call := testBoundCall(t, input, invocationID(8), "overflow-pod")
		if _, err := budget.ReserveToolCall(context.Background(), call); err != nil {
			t.Fatalf("ReserveToolCall(overflow) error = %v", err)
		}
		assertBudgetStop(t, budget.CompleteToolCall(call, ToolCallOutcome{ResultBytes: 1}), RunStopToolResultBytes)
		assertBudgetStop(t, firstError(budget.ReserveModelCall(context.Background())), RunStopToolResultBytes)
	})
}

func TestRunBudgetStopsAfterRepeatNoProgressCancellationAndDeadline(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")

	t.Run("repeat without retryable result", func(t *testing.T) {
		clock := newFakeClock()
		budget, _ := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		call := testBoundCall(t, input, testInvocationID, "sample-pod")
		if _, err := budget.ReserveToolCall(context.Background(), call); err != nil {
			t.Fatalf("first ReserveToolCall() error = %v", err)
		}
		if err := budget.CompleteToolCall(call, ToolCallOutcome{}); err != nil {
			t.Fatalf("CompleteToolCall() error = %v", err)
		}
		assertBudgetStop(t, firstError(budget.ReserveToolCall(context.Background(), call)), RunStopRepeatedToolCall)
		assertBudgetStop(t, firstError(budget.ReserveModelCall(context.Background())), RunStopRepeatedToolCall)
	})

	t.Run("retryable repeat only once", func(t *testing.T) {
		clock := newFakeClock()
		budget, _ := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		call := testBoundCall(t, input, testInvocationID, "sample-pod")
		if _, err := budget.ReserveToolCall(context.Background(), call); err != nil {
			t.Fatalf("first ReserveToolCall() error = %v", err)
		}
		if err := budget.CompleteToolCall(call, ToolCallOutcome{Retryable: true}); err != nil {
			t.Fatalf("first CompleteToolCall() error = %v", err)
		}
		if _, err := budget.ReserveToolCall(context.Background(), call); err != nil {
			t.Fatalf("second ReserveToolCall() error = %v", err)
		}
		if err := budget.CompleteToolCall(call, ToolCallOutcome{Retryable: true}); err != nil {
			t.Fatalf("second CompleteToolCall() error = %v", err)
		}
		assertBudgetStop(t, firstError(budget.ReserveToolCall(context.Background(), call)), RunStopRepeatedToolCall)
	})

	t.Run("explicit runtime revalidation", func(t *testing.T) {
		clock := newFakeClock()
		budget, _ := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		call := testBoundCall(t, input, testInvocationID, "sample-pod")
		if _, err := budget.ReserveToolCall(context.Background(), call); err != nil {
			t.Fatalf("first ReserveToolCall() error = %v", err)
		}
		if err := budget.CompleteToolCall(call, ToolCallOutcome{}); err != nil {
			t.Fatalf("CompleteToolCall() error = %v", err)
		}
		if _, err := budget.ReserveToolRevalidation(context.Background(), call); err != nil {
			t.Fatalf("ReserveToolRevalidation() error = %v", err)
		}
	})

	t.Run("two no-progress steps", func(t *testing.T) {
		clock := newFakeClock()
		budget, _ := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		for index := 0; index < domain.MaxAgentNoProgressSteps; index++ {
			if err := budget.ReserveStep(context.Background()); err != nil {
				t.Fatalf("ReserveStep(%d) error = %v", index, err)
			}
			err := budget.CompleteStep(0)
			if index+1 < domain.MaxAgentNoProgressSteps && err != nil {
				t.Fatalf("CompleteStep(%d) error = %v", index, err)
			}
			if index+1 == domain.MaxAgentNoProgressSteps {
				assertBudgetStop(t, err, RunStopNoProgress)
			}
		}
		assertBudgetStop(t, firstError(budget.ReserveToolCall(context.Background(), testBoundCall(t, input, testInvocationID, "sample-pod"))), RunStopNoProgress)
	})

	t.Run("cancelled context", func(t *testing.T) {
		clock := newFakeClock()
		budget, _ := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		assertBudgetStop(t, firstError(budget.ReserveModelCall(ctx)), RunStopCancelled)
		assertBudgetStop(t, firstError(budget.ReserveModelCall(context.Background())), RunStopCancelled)
	})

	t.Run("run deadline", func(t *testing.T) {
		clock := newFakeClock()
		budget, _ := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		clock.Advance(domain.MaxAgentRunDuration)
		assertBudgetStop(t, firstError(budget.ReserveModelCall(context.Background())), RunStopTimedOut)
	})

	t.Run("stale scope", func(t *testing.T) {
		clock := newFakeClock()
		budget, _ := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		if !budget.Terminate(RunStopStaleScope) {
			t.Fatal("Terminate(stale scope) = false")
		}
		assertBudgetStop(t, firstError(budget.ReserveModelCall(context.Background())), RunStopStaleScope)
	})

	t.Run("log calls", func(t *testing.T) {
		clock := newFakeClock()
		budget, _ := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		for index := 0; index < maxLogCalls; index++ {
			call, err := BindToolCall(input, invocationID(index), domain.ModelToolCall{
				ID:            fmt.Sprintf("call-%d", index+1),
				Name:          domain.ToolNameGetPodLogs,
				ArgumentsJSON: fmt.Sprintf(`{"pod_name":"sample-pod-%d","purpose":"Inspect bounded current logs."}`, index),
			})
			if err != nil {
				t.Fatalf("BindToolCall(log %d) error = %v", index, err)
			}
			if _, err := budget.ReserveToolCall(context.Background(), call); err != nil {
				t.Fatalf("ReserveToolCall(log %d) error = %v", index, err)
			}
			if err := budget.CompleteToolCall(call, ToolCallOutcome{}); err != nil {
				t.Fatalf("CompleteToolCall(log %d) error = %v", index, err)
			}
		}
		oneMore, err := BindToolCall(input, invocationID(maxLogCalls), domain.ModelToolCall{
			ID:            "call-log-over",
			Name:          domain.ToolNameGetPreviousPodLogs,
			ArgumentsJSON: `{"pod_name":"sample-pod-over","purpose":"Inspect bounded previous logs."}`,
		})
		if err != nil {
			t.Fatalf("BindToolCall(log over) error = %v", err)
		}
		assertBudgetStop(t, firstError(budget.ReserveToolCall(context.Background(), oneMore)), RunStopLogCallLimit)
	})
}

func TestRunBudgetRejectsExpandedLimitsAndCapsChildDeadline(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunBudgetLimits)
	}{
		{name: "run duration", mutate: func(limits *RunBudgetLimits) { limits.RunDuration = domain.MaxAgentRunDuration + time.Nanosecond }},
		{name: "steps", mutate: func(limits *RunBudgetLimits) { limits.Steps = domain.MaxAgentSteps + 1 }},
		{name: "Tool calls", mutate: func(limits *RunBudgetLimits) { limits.ToolCalls = domain.MaxAgentToolCalls + 1 }},
		{name: "model calls", mutate: func(limits *RunBudgetLimits) { limits.ModelCalls = domain.MaxAgentModelCalls + 1 }},
		{name: "result bytes", mutate: func(limits *RunBudgetLimits) { limits.ToolResultBytes = domain.MaxToolResultBytes + 1 }},
		{name: "run result bytes", mutate: func(limits *RunBudgetLimits) { limits.RunToolResultBytes = domain.MaxAgentRunToolResultBytes + 1 }},
		{name: "no progress", mutate: func(limits *RunBudgetLimits) { limits.NoProgressSteps = domain.MaxAgentNoProgressSteps + 1 }},
		{name: "model timeout", mutate: func(limits *RunBudgetLimits) {
			limits.ModelRequestTimeout = domain.MaxModelRequestTimeout + time.Nanosecond
		}},
		{name: "Tool timeout", mutate: func(limits *RunBudgetLimits) { limits.ToolRequestTimeout = maxToolRequestDuration + time.Nanosecond }},
		{name: "log calls", mutate: func(limits *RunBudgetLimits) { limits.LogCalls = maxLogCalls + 1 }},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			limits := DefaultRunBudgetLimits()
			current.mutate(&limits)
			if err := limits.Validate(); err == nil {
				t.Fatal("expanded RunBudgetLimits.Validate() error = nil")
			}
		})
	}

	tightened := RunBudgetLimits{
		RunDuration:         time.Second,
		Steps:               1,
		ToolCalls:           1,
		ModelCalls:          1,
		ToolResultBytes:     1024,
		RunToolResultBytes:  1024,
		NoProgressSteps:     1,
		ModelRequestTimeout: 500 * time.Millisecond,
		ToolRequestTimeout:  500 * time.Millisecond,
		LogCalls:            1,
	}
	if err := tightened.Validate(); err != nil {
		t.Fatalf("tightened RunBudgetLimits.Validate() error = %v", err)
	}

	clock := newFakeClock()
	budget, err := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
	if err != nil {
		t.Fatalf("NewRunBudget() error = %v", err)
	}
	clock.Advance(80 * time.Second)
	reservation, err := budget.ReserveModelCall(context.Background())
	if err != nil {
		t.Fatalf("ReserveModelCall() error = %v", err)
	}
	if reservation.Timeout != 10*time.Second {
		t.Fatalf("child timeout = %s, want 10s", reservation.Timeout)
	}
}

func TestRunBudgetAtomicallyReservesConcurrentCallIntentions(t *testing.T) {
	clock := newFakeClock()
	budget, err := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
	if err != nil {
		t.Fatalf("NewRunBudget() error = %v", err)
	}

	const extraAttempts = 4
	start := make(chan struct{})
	results := make(chan error, domain.MaxAgentModelCalls+extraAttempts)
	var wait sync.WaitGroup
	for index := 0; index < domain.MaxAgentModelCalls+extraAttempts; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, reserveErr := budget.ReserveModelCall(context.Background())
			results <- reserveErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	successes := 0
	for reserveErr := range results {
		if reserveErr == nil {
			successes++
			continue
		}
		assertBudgetStop(t, reserveErr, RunStopModelCallLimit)
	}
	if successes != domain.MaxAgentModelCalls {
		t.Fatalf("successful concurrent reservations = %d, want %d", successes, domain.MaxAgentModelCalls)
	}
	snapshot := budget.Snapshot()
	if snapshot.ModelCalls != domain.MaxAgentModelCalls || !snapshot.Stopped || snapshot.StopReason != RunStopModelCallLimit {
		t.Fatalf("concurrent budget snapshot = %#v", snapshot)
	}
}

func firstError[T any](_ T, err error) error {
	return err
}

func assertBudgetStop(t *testing.T, err error, want RunStopReason) {
	t.Helper()
	var budgetError *RunBudgetError
	if !errors.As(err, &budgetError) {
		t.Fatalf("error = %v, want RunBudgetError", err)
	}
	if budgetError.Reason() != want {
		t.Fatalf("stop reason = %q, want %q", budgetError.Reason(), want)
	}
}

func invocationID(index int) domain.ToolInvocationID {
	return domain.ToolInvocationID(fmt.Sprintf("00000000-0000-7000-8000-%012d", 4_100+index))
}
