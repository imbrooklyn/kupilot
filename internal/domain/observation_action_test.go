package domain

import (
	"strings"
	"testing"
	"time"
)

func TestObservationActionPlanBindsEverySourceInput(t *testing.T) {
	t.Parallel()

	base := testObservationActionPlan(t, ActionObservationPrometheus)
	intent, err := base.Intent(PermissionProfileAutoReview)
	if err != nil || intent.ValidateObservationAction() != nil || !base.MatchesIntent(intent) {
		t.Fatalf("valid observation action = %#v/%v", intent, err)
	}
	baseDigest := base.Parameters.Digest()
	tests := []struct {
		name   string
		mutate func(*ObservationActionPlan)
	}{
		{"query", func(plan *ObservationActionPlan) {
			plan.Parameters.Observation.QueryID = QueryPrometheusPodMemoryWorkingSet
		}},
		{"window", func(plan *ObservationActionPlan) { plan.Parameters.Observation.WindowSeconds += 60 }},
		{"step", func(plan *ObservationActionPlan) { plan.Parameters.Observation.StepSeconds += 15 }},
		{"series", func(plan *ObservationActionPlan) { plan.Parameters.Observation.SeriesLimit++ }},
		{"origin", func(plan *ObservationActionPlan) { plan.OriginHash = ActionDigest(strings.Repeat("b", 64)) }},
		{"uid", func(plan *ObservationActionPlan) { plan.Target.Resource.UID = "replacement-pod" }},
		{"resource version", func(plan *ObservationActionPlan) { plan.Target.Resource.ResourceVersion = "22" }},
		{"timeout", func(plan *ObservationActionPlan) { plan.Limits.Timeout += time.Second }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			test.mutate(&changed)
			if changed.Parameters.Digest() != baseDigest {
				changed.Target.Fingerprint = string(changed.Parameters.Digest())
			}
			changedIntent, intentErr := changed.Intent(PermissionProfileAutoReview)
			if intentErr == nil && changedIntent == intent {
				t.Fatal("changed source input retained the original immutable intent")
			}
		})
	}
}

func TestObservationActionKindsRejectCrossKindFieldsAndUnsafeRouting(t *testing.T) {
	t.Parallel()

	for _, kind := range []ActionObservationKind{ActionObservationPodLog, ActionObservationPrometheus, ActionObservationLoki} {
		plan := testObservationActionPlan(t, kind)
		for _, profile := range []PermissionProfile{
			PermissionProfileReadOnly, PermissionProfileAsk, PermissionProfileAutoReview,
			PermissionProfileFullAccess, PermissionProfileCustom,
		} {
			intent, err := plan.Intent(profile)
			if err != nil || intent.ValidateObservationAction() != nil || intent.Risk != RiskReview {
				t.Fatalf("kind/profile %s/%s = %#v/%v", kind, profile, intent, err)
			}
		}
	}

	logPlan := testObservationActionPlan(t, ActionObservationPodLog)
	logPlan.Parameters.Observation.QueryID = QueryPrometheusPodCPUUsage
	logPlan.Target.Fingerprint = string(logPlan.Parameters.Digest())
	if logPlan.Validate() == nil {
		t.Fatal("Pod log action accepted a data-source query")
	}
	queryPlan := testObservationActionPlan(t, ActionObservationPrometheus)
	queryPlan.Parameters.Observation.Search = "smuggled filter"
	queryPlan.Target.Fingerprint = string(queryPlan.Parameters.Digest())
	if queryPlan.Validate() == nil {
		t.Fatal("Prometheus action accepted a model-selected filter")
	}
}

func TestObservationOutcomeIsContentFreeAndBounded(t *testing.T) {
	t.Parallel()

	intent, err := testObservationActionPlan(t, ActionObservationLoki).Intent(PermissionProfileAsk)
	if err != nil {
		t.Fatal(err)
	}
	valid := ObservationActionOutcome{State: ObservationActionSucceeded, ResultItems: 1, ResultLines: 10, ResultBytes: 100}
	if valid.Validate(intent) != nil {
		t.Fatalf("valid outcome rejected: %v", valid.Validate(intent))
	}
	valid.ResultBytes = intent.Limits.MaximumBytes + 1
	if valid.Validate(intent) == nil {
		t.Fatal("outcome exceeded its envelope byte ceiling")
	}
	failed := ObservationActionOutcome{State: ObservationActionFailed, ErrorClass: SafeErrorClassTimeout}
	if failed.Validate(intent) != nil {
		t.Fatalf("bounded failure rejected: %v", failed.Validate(intent))
	}
	failed.ErrorClass = ""
	if failed.Validate(intent) == nil {
		t.Fatal("failed outcome accepted without a safe error class")
	}
}

func testObservationActionPlan(t *testing.T, kind ActionObservationKind) ObservationActionPlan {
	t.Helper()
	parameters := ActionObservationParameters{Kind: kind, WindowSeconds: 300}
	limits := ActionLimits{MaximumBytes: 4096, MaximumOutput: 4096}
	origin := ActionDigest("")
	subresource := ""
	switch kind {
	case ActionObservationPodLog:
		parameters.Container, parameters.TailLines = "app", 20
		limits.Timeout, limits.MaximumItems, limits.MaximumLines = ObservationKubernetesTimeout, 1, 20
		subresource = "log"
	case ActionObservationPrometheus:
		parameters.QueryID, parameters.StepSeconds, parameters.SeriesLimit = QueryPrometheusPodCPUUsage, 30, 2
		limits.Timeout, limits.MaximumItems = 5*time.Second, 2
		origin = ActionDigest(strings.Repeat("a", 64))
	case ActionObservationLoki:
		parameters.QueryID, parameters.Search, parameters.LineLimit = QueryLokiPodLogs, "error", 20
		limits.Timeout, limits.MaximumItems, limits.MaximumLines = 5*time.Second, 2, 20
		origin = ActionDigest(strings.Repeat("a", 64))
	default:
		t.Fatalf("unsupported observation kind %q", kind)
	}
	typed := ActionParameters{Kind: ActionParametersObservation, Observation: parameters}
	return ObservationActionPlan{
		RunID: "00000000-0000-7000-8000-000000088001", SessionID: "00000000-0000-7000-8000-000000088002",
		Scope:            ClusterScope{Context: "test-context", Namespace: "team-a", NamespaceAccess: NamespaceAccessCurrent, Generation: 7, ActivatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		PolicyGeneration: 3, Operation: parameters.operation(),
		Target:     ActionTarget{Resource: ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod", UID: "pod-uid", ResourceVersion: "21"}, Subresource: subresource, Fingerprint: string(typed.Digest())},
		Parameters: typed, Limits: limits, OriginHash: origin, ReasonSummary: "Read one exact bounded observation.",
	}
}
