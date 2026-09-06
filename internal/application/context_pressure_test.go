package application

import (
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestContextPressureUsesExactMessageByteCoverageHealthThresholds(t *testing.T) {
	tests := []struct {
		name   string
		status UIModelContextStatus
		want   ContextPressureState
	}{
		{name: "normal", status: UIModelContextStatus{StorageHealthy: true}, want: ContextPressureNormal},
		{
			name: "elevated working messages", want: ContextPressureElevated,
			status: UIModelContextStatus{StorageHealthy: true, WorkingMessages: domain.SessionContextMessageTrigger / 2},
		},
		{
			name: "elevated working bytes", want: ContextPressureElevated,
			status: UIModelContextStatus{StorageHealthy: true, WorkingBytes: domain.MaxSessionContextBytes / 2},
		},
		{
			name: "elevated retained messages", want: ContextPressureElevated,
			status: UIModelContextStatus{StorageHealthy: true, EligibleMessages: domain.MaxSessionContextMessages / 2},
		},
		{
			name: "elevated retained bytes", want: ContextPressureElevated,
			status: UIModelContextStatus{StorageHealthy: true, EligibleBytes: domain.MaxSessionHistoryBytes / 2},
		},
		{
			name: "critical summary message trigger", want: ContextPressureCritical,
			status: UIModelContextStatus{StorageHealthy: true, WorkingMessages: domain.SessionContextMessageTrigger},
		},
		{
			name: "critical summary byte trigger", want: ContextPressureCritical,
			status: UIModelContextStatus{StorageHealthy: true, WorkingBytes: domain.MaxSessionContextBytes},
		},
		{
			name: "critical retained messages", want: ContextPressureCritical,
			status: UIModelContextStatus{StorageHealthy: true, EligibleMessages: (domain.MaxSessionContextMessages*80 + 99) / 100},
		},
		{
			name: "critical retained bytes", want: ContextPressureCritical,
			status: UIModelContextStatus{StorageHealthy: true, EligibleBytes: (domain.MaxSessionHistoryBytes*80 + 99) / 100},
		},
		{
			name: "degraded overrides critical", want: ContextPressureDegraded,
			status: UIModelContextStatus{WorkingMessages: domain.SessionContextMessageTrigger},
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			if got := projectContextPressure(current.status); got != current.want {
				t.Fatalf("projectContextPressure(%#v) = %q, want %q", current.status, got, current.want)
			}
		})
	}
}

func TestContextPressureDoesNotCrossThresholdOneUnitEarly(t *testing.T) {
	if got := projectContextPressure(UIModelContextStatus{
		StorageHealthy: true, WorkingMessages: domain.SessionContextMessageTrigger/2 - 1,
		WorkingBytes:     domain.MaxSessionContextBytes/2 - 1,
		EligibleMessages: domain.MaxSessionContextMessages/2 - 1,
		EligibleBytes:    domain.MaxSessionHistoryBytes/2 - 1,
	}); got != ContextPressureNormal {
		t.Fatalf("one-below elevated threshold = %q", got)
	}
	if got := projectContextPressure(UIModelContextStatus{
		StorageHealthy: true, WorkingMessages: domain.SessionContextMessageTrigger - 1,
		WorkingBytes:     domain.MaxSessionContextBytes - 1,
		EligibleMessages: (domain.MaxSessionContextMessages*80+99)/100 - 1,
		EligibleBytes:    (domain.MaxSessionHistoryBytes*80+99)/100 - 1,
	}); got != ContextPressureElevated {
		t.Fatalf("one-below critical threshold = %q", got)
	}
}
