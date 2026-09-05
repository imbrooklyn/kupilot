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

func TestRunBudgetProfilesMatchOperationalContract(t *testing.T) {
	tests := []struct {
		profile                             BudgetProfile
		run                                 time.Duration
		steps, tools, models                int
		model, kube                         time.Duration
		bytes, logs, remote, local, stalled int
	}{
		{BudgetProfileCompact, 2 * time.Minute, 12, 16, 6, 60 * time.Second, 15 * time.Second, 1 * 1024 * 1024, 4, 2, 1, 2},
		{BudgetProfileBalanced, 10 * time.Minute, 32, 48, 16, 120 * time.Second, 30 * time.Second, 4 * 1024 * 1024, 12, 4, 1, 4},
		{BudgetProfileExtended, 30 * time.Minute, 64, 128, 32, 300 * time.Second, 60 * time.Second, 12 * 1024 * 1024, 32, 8, 1, 6},
	}
	for _, test := range tests {
		t.Run(string(test.profile), func(t *testing.T) {
			limits, err := RunBudgetLimitsForProfile(test.profile)
			if err != nil || limits.Validate() != nil {
				t.Fatalf("RunBudgetLimitsForProfile(%q) = %#v, %v", test.profile, limits, err)
			}
			if limits.RunDuration != test.run || limits.Steps != test.steps || limits.ToolCalls != test.tools ||
				limits.ModelCalls != test.models || limits.ModelRequestTimeout != test.model || limits.ToolRequestTimeout != test.kube ||
				limits.RunToolResultBytes != test.bytes || limits.LogCalls != test.logs || limits.RemoteExecCalls != test.remote ||
				limits.LocalProcessCalls != test.local || limits.NoProgressSteps != test.stalled ||
				limits.ToolResultBytes != domain.MaxToolResultBytes {
				t.Fatalf("profile limits = %#v", limits)
			}
		})
	}
	if _, err := RunBudgetLimitsForProfile("unknown"); !errors.Is(err, ErrInvalidRunBudget) {
		t.Fatalf("unknown profile error = %v", err)
	}
	if got := DefaultRunBudgetLimits().Profile; got != BudgetProfileBalanced {
		t.Fatalf("default profile = %q", got)
	}
}

func TestRunBudgetSnapshotReportsTimeAndFrozenLimits(t *testing.T) {
	clock := newFakeClock()
	limits := DefaultRunBudgetLimits()
	startedAt := clock.Now()
	budget, err := NewRunBudget(limits, startedAt, clock.Now)
	if err != nil {
		t.Fatalf("NewRunBudget() error = %v", err)
	}
	clock.Advance(90 * time.Second)
	snapshot := budget.Snapshot()
	if snapshot.Profile != BudgetProfileBalanced || snapshot.Limits != limits || snapshot.StartedAt != startedAt ||
		snapshot.Deadline != startedAt.Add(limits.RunDuration) || snapshot.CapturedAt != clock.Now() ||
		snapshot.Elapsed != 90*time.Second || snapshot.Remaining != limits.RunDuration-90*time.Second {
		t.Fatalf("budget snapshot = %#v", snapshot)
	}
}

func TestRunBudgetEnforcesSelectedProfileLimits(t *testing.T) {
	input := testRunInput(t, "Inspect the selected Pod.")
	limits := DefaultRunBudgetLimits()

	t.Run("steps", func(t *testing.T) {
		clock := newFakeClock()
		budget, err := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		if err != nil {
			t.Fatalf("NewRunBudget() error = %v", err)
		}
		for index := 0; index < limits.Steps; index++ {
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
		for index := 0; index < limits.ModelCalls; index++ {
			reservation, reserveErr := budget.ReserveModelCall(context.Background())
			if reserveErr != nil {
				t.Fatalf("ReserveModelCall(%d) error = %v", index, reserveErr)
			}
			if reservation.Timeout > limits.ModelRequestTimeout {
				t.Fatalf("model timeout = %s", reservation.Timeout)
			}
		}
		assertBudgetStop(t, firstError(budget.ReserveModelCall(context.Background())), RunStopModelCallLimit)
	})

	t.Run("summary calls one over", func(t *testing.T) {
		clock := newFakeClock()
		budget, err := NewRunBudget(limits, clock.Now(), clock.Now)
		if err != nil {
			t.Fatalf("NewRunBudget() error = %v", err)
		}
		for index := 0; index < limits.SummaryCalls; index++ {
			reservation, reserveErr := budget.ReserveSummaryCall(context.Background())
			if reserveErr != nil {
				t.Fatalf("ReserveSummaryCall(%d) error = %v", index, reserveErr)
			}
			if reservation.Timeout > limits.SummaryRequestTimeout || reservation.CostUnits != 1 {
				t.Fatalf("summary reservation = %#v", reservation)
			}
		}
		assertBudgetStop(t, firstError(budget.ReserveSummaryCall(context.Background())), RunStopSummaryCallLimit)
	})

	t.Run("summary cost one over", func(t *testing.T) {
		clock := newFakeClock()
		costLimits := limits
		costLimits.SummaryCostUnits = 1
		budget, err := NewRunBudget(costLimits, clock.Now(), clock.Now)
		if err != nil {
			t.Fatalf("NewRunBudget() error = %v", err)
		}
		if _, err := budget.ReserveSummaryCall(context.Background()); err != nil {
			t.Fatalf("first ReserveSummaryCall() error = %v", err)
		}
		assertBudgetStop(t, firstError(budget.ReserveSummaryCall(context.Background())), RunStopSummaryCostLimit)
	})

	t.Run("Tool calls", func(t *testing.T) {
		clock := newFakeClock()
		budget, err := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
		if err != nil {
			t.Fatalf("NewRunBudget() error = %v", err)
		}
		for index := 0; index < limits.ToolCalls; index++ {
			call := testBoundCall(t, input, invocationID(index), fmt.Sprintf("sample-pod-%d", index))
			if _, reserveErr := budget.ReserveToolCall(context.Background(), call); reserveErr != nil {
				t.Fatalf("ReserveToolCall(%d) error = %v", index, reserveErr)
			}
			if completeErr := budget.CompleteToolCall(call, ToolCallOutcome{}); completeErr != nil {
				t.Fatalf("CompleteToolCall(%d) error = %v", index, completeErr)
			}
		}
		call := testBoundCall(t, input, invocationID(limits.ToolCalls), "one-more-pod")
		assertBudgetStop(t, firstError(budget.ReserveToolCall(context.Background(), call)), RunStopToolCallLimit)
	})

	t.Run("cumulative Tool bytes", func(t *testing.T) {
		clock := newFakeClock()
		byteLimits := limits
		byteLimits.RunToolResultBytes = 2 * domain.MaxToolResultBytes
		budget, err := NewRunBudget(byteLimits, clock.Now(), clock.Now)
		if err != nil {
			t.Fatalf("NewRunBudget() error = %v", err)
		}
		for index := 0; index < byteLimits.RunToolResultBytes/domain.MaxToolResultBytes; index++ {
			call := testBoundCall(t, input, invocationID(index), fmt.Sprintf("sample-pod-%d", index))
			if _, reserveErr := budget.ReserveToolCall(context.Background(), call); reserveErr != nil {
				t.Fatalf("ReserveToolCall(%d) error = %v", index, reserveErr)
			}
			if completeErr := budget.CompleteToolCall(call, ToolCallOutcome{ResultBytes: domain.MaxToolResultBytes}); completeErr != nil {
				t.Fatalf("CompleteToolCall(%d) error = %v", index, completeErr)
			}
		}
		call := testBoundCall(t, input, invocationID(2), "overflow-pod")
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
		limits := DefaultRunBudgetLimits()
		budget, _ := NewRunBudget(limits, clock.Now(), clock.Now)
		for index := 0; index < limits.NoProgressSteps; index++ {
			if err := budget.ReserveStep(context.Background()); err != nil {
				t.Fatalf("ReserveStep(%d) error = %v", index, err)
			}
			err := budget.CompleteStep(0)
			if index+1 < limits.NoProgressSteps && err != nil {
				t.Fatalf("CompleteStep(%d) error = %v", index, err)
			}
			if index+1 == limits.NoProgressSteps {
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
		limits := DefaultRunBudgetLimits()
		budget, _ := NewRunBudget(limits, clock.Now(), clock.Now)
		clock.Advance(limits.RunDuration)
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
		limits := DefaultRunBudgetLimits()
		budget, _ := NewRunBudget(limits, clock.Now(), clock.Now)
		for index := 0; index < limits.LogCalls; index++ {
			call, err := BindToolCall(input, invocationID(index), ToolSelection{
				ID:            fmt.Sprintf("call-%d", index+1),
				Name:          domain.ToolNameGetPodLogs,
				ArgumentsJSON: fmt.Sprintf(`{"container":null,"container_mode":null,"include_ephemeral":null,"include_init":null,"namespace":null,"pod_name":"sample-pod-%d","purpose":"Inspect bounded current logs.","search":null,"since_seconds":null,"tail_lines":null}`, index),
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
		oneMore, err := BindToolCall(input, invocationID(limits.LogCalls), ToolSelection{
			ID:            "call-log-over",
			Name:          domain.ToolNameGetPreviousPodLogs,
			ArgumentsJSON: `{"container":null,"container_mode":null,"include_ephemeral":null,"include_init":null,"namespace":null,"pod_name":"sample-pod-over","purpose":"Inspect bounded previous logs.","search":null,"since_seconds":null,"tail_lines":null}`,
		})
		if err != nil {
			t.Fatalf("BindToolCall(log over) error = %v", err)
		}
		assertBudgetStop(t, firstError(budget.ReserveToolCall(context.Background(), oneMore)), RunStopLogCallLimit)
	})

	t.Run("remote execution calls", func(t *testing.T) {
		clock := newFakeClock()
		limits := DefaultRunBudgetLimits()
		budget, _ := NewRunBudget(limits, clock.Now(), clock.Now)
		remoteInput := remoteDiagnosticsRunInput(t)
		for index := 0; index < limits.RemoteExecCalls; index++ {
			call, err := BindToolCall(remoteInput, invocationID(index), ToolSelection{
				ID:            fmt.Sprintf("call-remote-%d", index+1),
				Name:          domain.ToolNamePodExec,
				ArgumentsJSON: fmt.Sprintf(`{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"test-namespace","pod_name":"sample-pod-%d","purpose":"Inspect exact argv output."}`, index),
			})
			if err != nil {
				t.Fatalf("BindToolCall(remote %d) error = %v", index, err)
			}
			if _, err := budget.ReserveToolCall(context.Background(), call); err != nil {
				t.Fatalf("ReserveToolCall(remote %d) error = %v", index, err)
			}
			if err := budget.CompleteToolCall(call, ToolCallOutcome{}); err != nil {
				t.Fatalf("CompleteToolCall(remote %d) error = %v", index, err)
			}
		}
		oneMore, err := BindToolCall(remoteInput, invocationID(limits.RemoteExecCalls), ToolSelection{
			ID:            "call-remote-over",
			Name:          domain.ToolNamePodExec,
			ArgumentsJSON: `{"arguments":["literal;not-a-shell","$(ignored)","*.log"],"command_id":"literal-argv","container":"app","executable":"/usr/bin/printf","namespace":"test-namespace","pod_name":"sample-pod-over","purpose":"Inspect exact argv output."}`,
		})
		if err != nil {
			t.Fatalf("BindToolCall(remote over) error = %v", err)
		}
		assertBudgetStop(t, firstError(budget.ReserveToolCall(context.Background(), oneMore)), RunStopRemoteExecLimit)
		if snapshot := budget.Snapshot(); snapshot.RemoteExecCalls != limits.RemoteExecCalls {
			t.Fatalf("remote execution calls = %d, want %d", snapshot.RemoteExecCalls, limits.RemoteExecCalls)
		}
	})
}

func TestRunBudgetRejectsExpandedLimitsAndCapsChildDeadline(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunBudgetLimits)
	}{
		{name: "profile", mutate: func(limits *RunBudgetLimits) { limits.Profile = "unknown" }},
		{name: "run duration", mutate: func(limits *RunBudgetLimits) { limits.RunDuration++ }},
		{name: "steps", mutate: func(limits *RunBudgetLimits) { limits.Steps++ }},
		{name: "Tool calls", mutate: func(limits *RunBudgetLimits) { limits.ToolCalls++ }},
		{name: "model calls", mutate: func(limits *RunBudgetLimits) { limits.ModelCalls++ }},
		{name: "model request bytes", mutate: func(limits *RunBudgetLimits) { limits.ModelRequestBytes++ }},
		{name: "model stream bytes", mutate: func(limits *RunBudgetLimits) { limits.ModelStreamBytes++ }},
		{name: "model cost", mutate: func(limits *RunBudgetLimits) { limits.ModelCostUnits++ }},
		{name: "summary calls", mutate: func(limits *RunBudgetLimits) { limits.SummaryCalls++ }},
		{name: "summary request bytes", mutate: func(limits *RunBudgetLimits) { limits.SummaryRequestBytes++ }},
		{name: "summary output bytes", mutate: func(limits *RunBudgetLimits) { limits.SummaryOutputBytes++ }},
		{name: "summary cost", mutate: func(limits *RunBudgetLimits) { limits.SummaryCostUnits++ }},
		{name: "result bytes", mutate: func(limits *RunBudgetLimits) { limits.ToolResultBytes = domain.MaxToolResultBytes + 1 }},
		{name: "run result bytes", mutate: func(limits *RunBudgetLimits) { limits.RunToolResultBytes++ }},
		{name: "no progress", mutate: func(limits *RunBudgetLimits) { limits.NoProgressSteps++ }},
		{name: "model timeout", mutate: func(limits *RunBudgetLimits) {
			limits.ModelRequestTimeout++
		}},
		{name: "summary timeout", mutate: func(limits *RunBudgetLimits) { limits.SummaryRequestTimeout++ }},
		{name: "Tool timeout", mutate: func(limits *RunBudgetLimits) { limits.ToolRequestTimeout++ }},
		{name: "log calls", mutate: func(limits *RunBudgetLimits) { limits.LogCalls++ }},
		{name: "remote execution calls", mutate: func(limits *RunBudgetLimits) { limits.RemoteExecCalls++ }},
		{name: "local process calls", mutate: func(limits *RunBudgetLimits) { limits.LocalProcessCalls++ }},
		{name: "resource page bytes", mutate: func(limits *RunBudgetLimits) { limits.ResourcePageBytes++ }},
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
		Profile:               BudgetProfileBalanced,
		RunDuration:           time.Second,
		Steps:                 1,
		ToolCalls:             1,
		ModelCalls:            1,
		ModelRequestBytes:     1024,
		ModelStreamBytes:      1024,
		ModelCostUnits:        1,
		SummaryCalls:          1,
		SummaryRequestBytes:   1024,
		SummaryOutputBytes:    1024,
		SummaryCostUnits:      1,
		ToolResultBytes:       1024,
		RunToolResultBytes:    1024,
		NoProgressSteps:       1,
		ModelRequestTimeout:   500 * time.Millisecond,
		SummaryRequestTimeout: 500 * time.Millisecond,
		ToolRequestTimeout:    500 * time.Millisecond,
		LogCalls:              1,
		LogContainers:         1,
		LogBytes:              512,
		EventPages:            1,
		EventPageItems:        1,
		EventPageBytes:        512,
		EventBytes:            512,
		MetricCalls:           1,
		MetricContainers:      1,
		MetricBytes:           512,
		DataSourceCalls:       1,
		DataSourcePages:       1,
		DataSourceSeries:      1,
		DataSourceSamples:     1,
		DataSourceLines:       1,
		DataSourceBytes:       512,
		DataSourceWindow:      time.Minute,
		DataSourceStep:        15 * time.Second,
		RemoteExecCalls:       1,
		LocalProcessCalls:     1,
		ResourcePages:         1,
		ResourcePageItems:     1,
		ResourcePageBytes:     512,
		ResourceScannedItems:  1,
		ResourceReturnedItems: 1,
		ResourceBytes:         1024,
	}
	if err := tightened.Validate(); err != nil {
		t.Fatalf("tightened RunBudgetLimits.Validate() error = %v", err)
	}

	clock := newFakeClock()
	budget, err := NewRunBudget(DefaultRunBudgetLimits(), clock.Now(), clock.Now)
	if err != nil {
		t.Fatalf("NewRunBudget() error = %v", err)
	}
	clock.Advance(DefaultRunBudgetLimits().RunDuration - 10*time.Second)
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
	limits := DefaultRunBudgetLimits()
	budget, err := NewRunBudget(limits, clock.Now(), clock.Now)
	if err != nil {
		t.Fatalf("NewRunBudget() error = %v", err)
	}

	const extraAttempts = 4
	start := make(chan struct{})
	results := make(chan error, limits.ModelCalls+extraAttempts)
	var wait sync.WaitGroup
	for index := 0; index < limits.ModelCalls+extraAttempts; index++ {
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
	if successes != limits.ModelCalls {
		t.Fatalf("successful concurrent reservations = %d, want %d", successes, limits.ModelCalls)
	}
	snapshot := budget.Snapshot()
	if snapshot.ModelCalls != limits.ModelCalls || !snapshot.Stopped || snapshot.StopReason != RunStopModelCallLimit {
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
