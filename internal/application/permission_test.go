package application

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestPermissionProfileMatrixIsExact(t *testing.T) {
	tests := []struct {
		name   string
		policy PermissionPolicy
		effect domain.CapabilityEffectClass
		risk   domain.RiskClass
		want   domain.ReviewDisposition
		reason string
	}{
		{"read-only safe read", permissionPolicy(domain.PermissionProfileReadOnly), domain.CapabilityEffectSafeRead, domain.RiskSafe, domain.ReviewDispositionAutomatic, "safe_automatic"},
		{"read-only sensitive read", permissionPolicy(domain.PermissionProfileReadOnly), domain.CapabilityEffectSensitiveRead, domain.RiskReview, domain.ReviewDispositionHuman, "sensitive_read_human"},
		{"read-only mutation", permissionPolicy(domain.PermissionProfileReadOnly), domain.CapabilityEffectClusterMutation, domain.RiskReview, domain.ReviewDispositionDeny, "read_only_effect_denied"},
		{"read-only critical", permissionPolicy(domain.PermissionProfileReadOnly), domain.CapabilityEffectRemoteExecute, domain.RiskCritical, domain.ReviewDispositionDeny, "read_only_effect_denied"},
		{"ask safe", permissionPolicy(domain.PermissionProfileAsk), domain.CapabilityEffectSafeRead, domain.RiskSafe, domain.ReviewDispositionAutomatic, "safe_automatic"},
		{"ask review", permissionPolicy(domain.PermissionProfileAsk), domain.CapabilityEffectClusterMutation, domain.RiskReview, domain.ReviewDispositionHuman, "human_review_required"},
		{"ask critical", permissionPolicy(domain.PermissionProfileAsk), domain.CapabilityEffectRemoteExecute, domain.RiskCritical, domain.ReviewDispositionHuman, "critical_human_required"},
		{"auto-review review", permissionPolicy(domain.PermissionProfileAutoReview), domain.CapabilityEffectClusterMutation, domain.RiskReview, domain.ReviewDispositionReviewer, "reviewer_delegated"},
		{"auto-review critical", permissionPolicy(domain.PermissionProfileAutoReview), domain.CapabilityEffectRemoteExecute, domain.RiskCritical, domain.ReviewDispositionHuman, "critical_human_required"},
		{"full-access review", PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 1, FullAccessAllowed: true, HighRiskAcknowledged: true}, domain.CapabilityEffectClusterMutation, domain.RiskReview, domain.ReviewDispositionAutomatic, "explicit_full_access"},
		{"full-access critical", PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 1, FullAccessAllowed: true, HighRiskAcknowledged: true}, domain.CapabilityEffectRemoteExecute, domain.RiskCritical, domain.ReviewDispositionAutomatic, "explicit_full_access"},
		{"custom safe human", PermissionPolicy{Profile: domain.PermissionProfileCustom, Generation: 1, CustomRoutes: []CustomPermissionRoute{{Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskSafe, Disposition: domain.ReviewDispositionHuman}}}, domain.CapabilityEffectSafeRead, domain.RiskSafe, domain.ReviewDispositionHuman, "custom_route"},
		{"custom review reviewer", PermissionPolicy{Profile: domain.PermissionProfileCustom, Generation: 1, CustomRoutes: []CustomPermissionRoute{{Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskReview, Disposition: domain.ReviewDispositionReviewer}}}, domain.CapabilityEffectClusterMutation, domain.RiskReview, domain.ReviewDispositionReviewer, "custom_route"},
		{"custom critical automatic", PermissionPolicy{Profile: domain.PermissionProfileCustom, Generation: 1, HighRiskAcknowledged: true, CustomRoutes: []CustomPermissionRoute{{Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskCritical, Disposition: domain.ReviewDispositionAutomatic}}}, domain.CapabilityEffectClusterMutation, domain.RiskCritical, domain.ReviewDispositionAutomatic, "custom_route"},
		{"custom critical default", permissionPolicy(domain.PermissionProfileCustom), domain.CapabilityEffectRemoteExecute, domain.RiskCritical, domain.ReviewDispositionHuman, "custom_critical_human_default"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := EvaluatePermission(test.policy, PermissionEvaluationInput{
				Operation: domain.ActionOperationRestartDeployment, Effect: test.effect, Risk: test.risk,
				CapabilityAdmitted: true, CapabilityEnabled: true,
			})
			if got.Disposition != test.want || got.ReasonCode != test.reason || got.Validate() != nil {
				t.Fatalf("EvaluatePermission() = %#v, want %q/%q", got, test.want, test.reason)
			}
		})
	}
}

func TestPermissionProfileEffectRiskCartesianMatrix(t *testing.T) {
	profiles := []struct {
		name   string
		policy PermissionPolicy
	}{
		{"read-only", permissionPolicy(domain.PermissionProfileReadOnly)},
		{"ask", permissionPolicy(domain.PermissionProfileAsk)},
		{"auto-review", permissionPolicy(domain.PermissionProfileAutoReview)},
		{"full-access", PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 1, FullAccessAllowed: true, HighRiskAcknowledged: true}},
		{"custom", PermissionPolicy{Profile: domain.PermissionProfileCustom, Generation: 1, CustomRoutes: []CustomPermissionRoute{
			{Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskSafe, Disposition: domain.ReviewDispositionAutomatic},
			{Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskReview, Disposition: domain.ReviewDispositionReviewer},
		}}},
	}
	effects := []domain.CapabilityEffectClass{
		domain.CapabilityEffectSafeRead, domain.CapabilityEffectSensitiveRead,
		domain.CapabilityEffectNetworkEgress, domain.CapabilityEffectRemoteExecute,
		domain.CapabilityEffectLocalExecute, domain.CapabilityEffectClusterMutation,
	}
	risks := []domain.RiskClass{domain.RiskSafe, domain.RiskReview, domain.RiskCritical, domain.RiskDeny}
	for _, profile := range profiles {
		for _, effect := range effects {
			for _, risk := range risks {
				name := profile.name + "/" + string(effect) + "/" + string(risk)
				t.Run(name, func(t *testing.T) {
					got := EvaluatePermission(profile.policy, PermissionEvaluationInput{
						Operation: domain.ActionOperationRestartDeployment, Effect: effect, Risk: risk,
						CapabilityAdmitted: true, CapabilityEnabled: true,
					})
					want := expectedPermissionDisposition(profile.policy.Profile, effect, risk)
					if got.Disposition != want || got.Validate() != nil {
						t.Fatalf("EvaluatePermission() = %#v, want %q", got, want)
					}
				})
			}
		}
	}
}

func TestLocalExecutionPermissionProfileMatrixUsesExactOperationAndRisk(t *testing.T) {
	tests := []struct {
		name      string
		policy    PermissionPolicy
		operation domain.ActionOperation
		risk      domain.RiskClass
		want      domain.ReviewDisposition
	}{
		{name: "read-only direct argv", policy: permissionPolicy(domain.PermissionProfileReadOnly), operation: domain.ActionOperationRestrictedLocalArgv, risk: domain.RiskReview, want: domain.ReviewDispositionDeny},
		{name: "ask direct argv", policy: permissionPolicy(domain.PermissionProfileAsk), operation: domain.ActionOperationRestrictedLocalArgv, risk: domain.RiskReview, want: domain.ReviewDispositionHuman},
		{name: "ask shell", policy: permissionPolicy(domain.PermissionProfileAsk), operation: domain.ActionOperationShell, risk: domain.RiskCritical, want: domain.ReviewDispositionHuman},
		{name: "auto-review direct argv", policy: permissionPolicy(domain.PermissionProfileAutoReview), operation: domain.ActionOperationRestrictedLocalArgv, risk: domain.RiskReview, want: domain.ReviewDispositionReviewer},
		{name: "auto-review shell", policy: permissionPolicy(domain.PermissionProfileAutoReview), operation: domain.ActionOperationShell, risk: domain.RiskCritical, want: domain.ReviewDispositionHuman},
		{name: "full-access direct argv", policy: PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 1, FullAccessAllowed: true, HighRiskAcknowledged: true}, operation: domain.ActionOperationRestrictedLocalArgv, risk: domain.RiskReview, want: domain.ReviewDispositionAutomatic},
		{name: "full-access shell", policy: PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 1, FullAccessAllowed: true, HighRiskAcknowledged: true}, operation: domain.ActionOperationShell, risk: domain.RiskCritical, want: domain.ReviewDispositionAutomatic},
		{name: "custom exact direct argv", policy: PermissionPolicy{Profile: domain.PermissionProfileCustom, Generation: 1, CustomRoutes: []CustomPermissionRoute{{Operation: domain.ActionOperationRestrictedLocalArgv, Risk: domain.RiskReview, Disposition: domain.ReviewDispositionAutomatic}}}, operation: domain.ActionOperationRestrictedLocalArgv, risk: domain.RiskReview, want: domain.ReviewDispositionAutomatic},
		{name: "custom shell defaults human", policy: permissionPolicy(domain.PermissionProfileCustom), operation: domain.ActionOperationShell, risk: domain.RiskCritical, want: domain.ReviewDispositionHuman},
		{name: "custom exact shell", policy: PermissionPolicy{Profile: domain.PermissionProfileCustom, Generation: 1, HighRiskAcknowledged: true, CustomRoutes: []CustomPermissionRoute{{Operation: domain.ActionOperationShell, Risk: domain.RiskCritical, Disposition: domain.ReviewDispositionAutomatic}}}, operation: domain.ActionOperationShell, risk: domain.RiskCritical, want: domain.ReviewDispositionAutomatic},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := EvaluatePermission(test.policy, PermissionEvaluationInput{
				Operation: test.operation, Effect: domain.CapabilityEffectLocalExecute, Risk: test.risk,
				CapabilityAdmitted: true, CapabilityEnabled: true,
			})
			if got.Validate() != nil || got.Disposition != test.want {
				t.Fatalf("local permission = %#v, want %s", got, test.want)
			}
		})
	}
}

func TestS04ExecutionPermissionRouteMatrixIsComplete(t *testing.T) {
	operations := []struct {
		name      string
		operation domain.ActionOperation
		effect    domain.CapabilityEffectClass
		risk      domain.RiskClass
	}{
		{"predefined Pod diagnostic", domain.ActionOperationPodDiagnostic, domain.CapabilityEffectRemoteExecute, domain.RiskReview},
		{"general Pod Exec", domain.ActionOperationPodExec, domain.CapabilityEffectRemoteExecute, domain.RiskCritical},
		{"container file read", domain.ActionOperationContainerFileRead, domain.CapabilityEffectRemoteExecute, domain.RiskReview},
		{"diagnostic Pod", domain.ActionOperationDiagnosticPod, domain.CapabilityEffectClusterMutation, domain.RiskCritical},
		{"restart", domain.ActionOperationRestartDeployment, domain.CapabilityEffectClusterMutation, domain.RiskReview},
		{"scale positive delta one", domain.ActionOperationScaleWorkload, domain.CapabilityEffectClusterMutation, domain.RiskReview},
		{"scale zero or other delta", domain.ActionOperationScaleWorkload, domain.CapabilityEffectClusterMutation, domain.RiskCritical},
		{"rollback", domain.ActionOperationRollbackDeployment, domain.CapabilityEffectClusterMutation, domain.RiskCritical},
		{"delete owned Pod", domain.ActionOperationDeleteOwnedPod, domain.CapabilityEffectClusterMutation, domain.RiskReview},
		{"cordon", domain.ActionOperationCordonNode, domain.CapabilityEffectClusterMutation, domain.RiskReview},
		{"uncordon", domain.ActionOperationUncordonNode, domain.CapabilityEffectClusterMutation, domain.RiskReview},
		{"drain", domain.ActionOperationDrainNode, domain.CapabilityEffectClusterMutation, domain.RiskCritical},
		{"restricted local read", domain.ActionOperationRestrictedLocalArgv, domain.CapabilityEffectLocalExecute, domain.RiskReview},
		{"restricted local side effect", domain.ActionOperationRestrictedLocalArgv, domain.CapabilityEffectLocalExecute, domain.RiskCritical},
		{"separate shell", domain.ActionOperationShell, domain.CapabilityEffectLocalExecute, domain.RiskCritical},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			profiles := []struct {
				name   string
				policy PermissionPolicy
				want   domain.ReviewDisposition
				reason string
			}{
				{"read-only", permissionPolicy(domain.PermissionProfileReadOnly), domain.ReviewDispositionDeny, "read_only_effect_denied"},
				{"ask", permissionPolicy(domain.PermissionProfileAsk), domain.ReviewDispositionHuman, permissionHumanReason(operation.risk)},
				{"auto-review", permissionPolicy(domain.PermissionProfileAutoReview), permissionAutoReviewRoute(operation.risk), permissionAutoReviewReason(operation.risk)},
				{"full-access", PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 1, FullAccessAllowed: true, HighRiskAcknowledged: true}, domain.ReviewDispositionAutomatic, "explicit_full_access"},
				{"custom default", permissionPolicy(domain.PermissionProfileCustom), permissionCustomDefaultRoute(operation.risk), permissionCustomDefaultReason(operation.risk)},
				{"custom exact", exactS04CustomPermission(operation.operation, operation.risk), permissionCustomExactRoute(operation.risk), "custom_route"},
			}
			for _, profile := range profiles {
				t.Run(profile.name, func(t *testing.T) {
					input := PermissionEvaluationInput{
						Operation: operation.operation, Effect: operation.effect, Risk: operation.risk,
						CapabilityAdmitted: true, CapabilityEnabled: true,
					}
					got := EvaluatePermission(profile.policy, input)
					if got.Validate() != nil || got.Disposition != profile.want || got.ReasonCode != profile.reason {
						t.Fatalf("EvaluatePermission() = %#v, want %q/%q", got, profile.want, profile.reason)
					}
					for _, denial := range []struct {
						name     string
						admitted bool
						enabled  bool
						reason   string
					}{
						{"not admitted", false, true, "capability_not_admitted"},
						{"disabled", true, false, "capability_disabled"},
					} {
						t.Run(denial.name, func(t *testing.T) {
							input.CapabilityAdmitted, input.CapabilityEnabled = denial.admitted, denial.enabled
							denied := EvaluatePermission(profile.policy, input)
							if denied.Validate() != nil || denied.Disposition != domain.ReviewDispositionDeny || denied.ReasonCode != denial.reason {
								t.Fatalf("hard denial = %#v, want deny/%q", denied, denial.reason)
							}
						})
					}
				})
			}
		})
	}
}

func permissionHumanReason(risk domain.RiskClass) string {
	if risk == domain.RiskCritical {
		return "critical_human_required"
	}
	return "human_review_required"
}

func permissionAutoReviewRoute(risk domain.RiskClass) domain.ReviewDisposition {
	if risk == domain.RiskCritical {
		return domain.ReviewDispositionHuman
	}
	return domain.ReviewDispositionReviewer
}

func permissionAutoReviewReason(risk domain.RiskClass) string {
	if risk == domain.RiskCritical {
		return "critical_human_required"
	}
	return "reviewer_delegated"
}

func permissionCustomDefaultRoute(risk domain.RiskClass) domain.ReviewDisposition {
	if risk == domain.RiskCritical {
		return domain.ReviewDispositionHuman
	}
	return domain.ReviewDispositionDeny
}

func permissionCustomDefaultReason(risk domain.RiskClass) string {
	if risk == domain.RiskCritical {
		return "custom_critical_human_default"
	}
	return "custom_route_missing"
}

func exactS04CustomPermission(operation domain.ActionOperation, risk domain.RiskClass) PermissionPolicy {
	disposition := domain.ReviewDispositionReviewer
	acknowledged := false
	if risk == domain.RiskCritical {
		disposition = domain.ReviewDispositionAutomatic
		acknowledged = true
	}
	return PermissionPolicy{
		Profile: domain.PermissionProfileCustom, Generation: 1, HighRiskAcknowledged: acknowledged,
		CustomRoutes: []CustomPermissionRoute{{Operation: operation, Risk: risk, Disposition: disposition}},
	}
}

func permissionCustomExactRoute(risk domain.RiskClass) domain.ReviewDisposition {
	if risk == domain.RiskCritical {
		return domain.ReviewDispositionAutomatic
	}
	return domain.ReviewDispositionReviewer
}

func expectedPermissionDisposition(
	profile domain.PermissionProfile,
	effect domain.CapabilityEffectClass,
	risk domain.RiskClass,
) domain.ReviewDisposition {
	if !risk.CompatibleWithEffect(effect) || risk == domain.RiskDeny {
		return domain.ReviewDispositionDeny
	}
	switch profile {
	case domain.PermissionProfileReadOnly:
		if risk == domain.RiskSafe && effect == domain.CapabilityEffectSafeRead {
			return domain.ReviewDispositionAutomatic
		}
		if risk == domain.RiskReview && effect == domain.CapabilityEffectSensitiveRead {
			return domain.ReviewDispositionHuman
		}
		return domain.ReviewDispositionDeny
	case domain.PermissionProfileAsk:
		if risk == domain.RiskSafe {
			return domain.ReviewDispositionAutomatic
		}
		return domain.ReviewDispositionHuman
	case domain.PermissionProfileAutoReview:
		if risk == domain.RiskSafe {
			return domain.ReviewDispositionAutomatic
		}
		if risk == domain.RiskReview {
			return domain.ReviewDispositionReviewer
		}
		return domain.ReviewDispositionHuman
	case domain.PermissionProfileFullAccess:
		return domain.ReviewDispositionAutomatic
	case domain.PermissionProfileCustom:
		if risk == domain.RiskSafe {
			return domain.ReviewDispositionAutomatic
		}
		if risk == domain.RiskReview {
			return domain.ReviewDispositionReviewer
		}
		return domain.ReviewDispositionHuman
	default:
		return domain.ReviewDispositionDeny
	}
}

func TestPermissionHardDenialsPrecedeEveryProfile(t *testing.T) {
	for _, profile := range []domain.PermissionProfile{
		domain.PermissionProfileReadOnly, domain.PermissionProfileAsk, domain.PermissionProfileAutoReview,
		domain.PermissionProfileFullAccess, domain.PermissionProfileCustom,
	} {
		policy := permissionPolicy(profile)
		if profile == domain.PermissionProfileFullAccess {
			policy.FullAccessAllowed, policy.HighRiskAcknowledged = true, true
		}
		for _, test := range []struct {
			admitted bool
			enabled  bool
			risk     domain.RiskClass
			reason   string
		}{
			{false, true, domain.RiskSafe, "capability_not_admitted"},
			{true, false, domain.RiskSafe, "capability_disabled"},
			{true, true, domain.RiskDeny, "hard_deny"},
		} {
			got := EvaluatePermission(policy, PermissionEvaluationInput{
				Operation: domain.ActionOperationRestartDeployment, Effect: domain.CapabilityEffectClusterMutation,
				Risk: test.risk, CapabilityAdmitted: test.admitted, CapabilityEnabled: test.enabled,
			})
			if got.Disposition != domain.ReviewDispositionDeny || got.ReasonCode != test.reason {
				t.Fatalf("profile %q denial = %#v", profile, got)
			}
		}
	}
}

func TestPermissionRejectsRiskLabelsThatLowerTechnicalEffects(t *testing.T) {
	policy := PermissionPolicy{
		Profile: domain.PermissionProfileFullAccess, Generation: 1,
		FullAccessAllowed: true, HighRiskAcknowledged: true,
	}
	for _, current := range []PermissionEvaluationInput{
		{Operation: domain.ActionOperationRestartDeployment, Effect: domain.CapabilityEffectClusterMutation, Risk: domain.RiskSafe, CapabilityAdmitted: true, CapabilityEnabled: true},
		{Operation: domain.ActionOperationResourceGet, Effect: domain.CapabilityEffectSafeRead, Risk: domain.RiskReview, CapabilityAdmitted: true, CapabilityEnabled: true},
		{Operation: domain.ActionOperationContainerFileRead, Effect: domain.CapabilityEffectSensitiveRead, Risk: domain.RiskCritical, CapabilityAdmitted: true, CapabilityEnabled: true},
	} {
		got := EvaluatePermission(policy, current)
		if got.Disposition != domain.ReviewDispositionDeny || got.ReasonCode != "invalid_policy" {
			t.Fatalf("lowered risk route = %#v", got)
		}
	}
}

func TestPermissionPolicyRejectsUnsafeOrPartialConfiguration(t *testing.T) {
	tests := []PermissionPolicy{
		{Profile: domain.PermissionProfileFullAccess, Generation: 1, FullAccessAllowed: true},
		{Profile: domain.PermissionProfileAsk, Generation: 1, FullAccessAllowed: true},
		{Profile: domain.PermissionProfileAutoReview, Generation: 1, HighRiskAcknowledged: true},
		{Profile: domain.PermissionProfileCustom, Generation: 1, FullAccessAllowed: true},
		{Profile: domain.PermissionProfileAsk, Generation: 1, CustomRoutes: []CustomPermissionRoute{{Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskReview, Disposition: domain.ReviewDispositionHuman}}},
		{Profile: domain.PermissionProfileCustom, Generation: 1, CustomRoutes: []CustomPermissionRoute{{Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskCritical, Disposition: domain.ReviewDispositionReviewer}}},
		{Profile: domain.PermissionProfileCustom, Generation: 1, CustomRoutes: []CustomPermissionRoute{{Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskCritical, Disposition: domain.ReviewDispositionAutomatic}}},
		{Profile: domain.PermissionProfileCustom, Generation: 1, CustomRoutes: []CustomPermissionRoute{
			{Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskReview, Disposition: domain.ReviewDispositionHuman},
			{Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskReview, Disposition: domain.ReviewDispositionDeny},
		}},
	}
	for index, policy := range tests {
		if policy.Validate() == nil {
			t.Fatalf("policy[%d] unexpectedly valid", index)
		}
	}
}

func TestSessionPermissionRuleMatchesSubresourceAndArgvPrefixOnly(t *testing.T) {
	manager, err := NewPermissionManager(permissionPolicy(domain.PermissionProfileAsk))
	if err != nil {
		t.Fatal(err)
	}
	sessionID := domain.SessionID("00000000-0000-7000-8000-000000000002")
	if err := manager.BindSession(sessionID); err != nil {
		t.Fatal(err)
	}
	hooks := &permissionHookOrder{}
	if err := manager.BindInvalidationHooks(hooks, hooks); err != nil {
		t.Fatal(err)
	}
	base := permissionArgvEnvelope(t, 1, "00000000-0000-7000-8000-000000000031", "exec", "diagnostic-tool", []string{"status", "--brief"})
	rule, err := manager.CreateSessionRule(context.Background(), CreateSessionPermissionRuleCommand{
		Actor: PermissionChangeActorLocalUser, RuleID: "00000000-0000-7000-8000-000000000032",
		SessionID: sessionID, Envelope: base, TargetNamePrefix: "sample", ArgumentPrefixCount: 1,
		CreatedAt: base.RequestedAt.Add(time.Second), ExpiresAt: base.RequestedAt.Add(time.Hour),
	})
	if err != nil || rule.TargetSubresource != "exec" || rule.Executable != "diagnostic-tool" ||
		!reflect.DeepEqual(rule.ArgumentPrefix.Values(), []string{"status"}) || rule.ParameterDigest != "" {
		t.Fatalf("CreateSessionRule() = %#v/%v", rule, err)
	}

	tests := []struct {
		name        string
		envelope    domain.ActionEnvelope
		now         time.Time
		wantRoute   domain.ReviewDisposition
		wantMatched bool
	}{
		{"prefix match", permissionArgvEnvelope(t, 2, "00000000-0000-7000-8000-000000000033", "exec", "diagnostic-tool", []string{"status", "--detailed"}), base.RequestedAt.Add(2 * time.Second), domain.ReviewDispositionAutomatic, true},
		{"subresource mismatch", permissionArgvEnvelope(t, 2, "00000000-0000-7000-8000-000000000034", "log", "diagnostic-tool", []string{"status", "--detailed"}), base.RequestedAt.Add(2 * time.Second), domain.ReviewDispositionHuman, false},
		{"executable mismatch", permissionArgvEnvelope(t, 2, "00000000-0000-7000-8000-000000000035", "exec", "other-tool", []string{"status", "--detailed"}), base.RequestedAt.Add(2 * time.Second), domain.ReviewDispositionHuman, false},
		{"argv prefix mismatch", permissionArgvEnvelope(t, 2, "00000000-0000-7000-8000-000000000036", "exec", "diagnostic-tool", []string{"inspect", "--detailed"}), base.RequestedAt.Add(2 * time.Second), domain.ReviewDispositionHuman, false},
		{"expired", permissionArgvEnvelope(t, 2, "00000000-0000-7000-8000-000000000037", "exec", "diagnostic-tool", []string{"status", "--detailed"}), base.RequestedAt.Add(time.Hour), domain.ReviewDispositionHuman, false},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			got := manager.Evaluate(current.envelope, true, true, current.now)
			if got.Disposition != current.wantRoute || (got.MatchedRuleID == rule.ID) != current.wantMatched {
				t.Fatalf("Evaluate() = %#v", got)
			}
		})
	}
	if err := manager.RevokeSessionRule(context.Background(), PermissionChangeActorLocalUser, rule.ID); err != nil {
		t.Fatalf("RevokeSessionRule() error = %v", err)
	}
	afterRevoke := permissionArgvEnvelope(t, 3, "00000000-0000-7000-8000-000000000038", "exec", "diagnostic-tool", []string{"status", "--detailed"})
	if got := manager.Evaluate(afterRevoke, true, true, afterRevoke.RequestedAt); got.Disposition != domain.ReviewDispositionHuman || got.MatchedRuleID != "" {
		t.Fatalf("revoked rule evaluation = %#v", got)
	}
}

func TestSessionPermissionRuleMatchesOnlyOneExactLocalArgv(t *testing.T) {
	sessionID := domain.SessionID("00000000-0000-7000-8000-000000000002")
	manager, err := NewPermissionManager(permissionPolicy(domain.PermissionProfileAsk))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.BindSession(sessionID); err != nil {
		t.Fatal(err)
	}
	hooks := &permissionHookOrder{}
	if err := manager.BindInvalidationHooks(hooks, hooks); err != nil {
		t.Fatal(err)
	}
	base := permissionLocalArgvEnvelope(t, 1, "00000000-0000-7000-8000-000000000071", []string{"status", "--brief"})
	if _, err := manager.CreateSessionRule(context.Background(), CreateSessionPermissionRuleCommand{
		Actor: PermissionChangeActorLocalUser, RuleID: "00000000-0000-7000-8000-000000000072",
		SessionID: sessionID, Envelope: base, TargetNamePrefix: "test-namespace", ArgumentPrefixCount: 1,
		CreatedAt: base.RequestedAt.Add(time.Second), ExpiresAt: base.RequestedAt.Add(time.Hour),
	}); err != ErrPermissionRuleInvalid {
		t.Fatalf("CreateSessionRule(partial local argv) error = %v", err)
	}
	rule, err := manager.CreateSessionRule(context.Background(), CreateSessionPermissionRuleCommand{
		Actor: PermissionChangeActorLocalUser, RuleID: "00000000-0000-7000-8000-000000000073",
		SessionID: sessionID, Envelope: base, TargetNamePrefix: "test-namespace", ArgumentPrefixCount: 2,
		CreatedAt: base.RequestedAt.Add(time.Second), ExpiresAt: base.RequestedAt.Add(time.Hour),
	})
	if err != nil || rule.ParameterKind != domain.ActionParametersLocalArgv || rule.Container != "" ||
		!rule.ParameterDigest.Valid() || rule.Executable != "/opt/bin/diagnostic" ||
		!reflect.DeepEqual(rule.ArgumentPrefix.Values(), []string{"status", "--brief"}) {
		t.Fatalf("CreateSessionRule(local argv) = %#v/%v", rule, err)
	}
	matching := permissionLocalArgvEnvelope(t, 2, "00000000-0000-7000-8000-000000000074", []string{"status", "--brief"})
	if got := manager.Evaluate(matching, true, true, matching.RequestedAt.Add(2*time.Second)); got.Disposition != domain.ReviewDispositionAutomatic || got.MatchedRuleID != rule.ID {
		t.Fatalf("matching local argv route = %#v", got)
	}
	extended := permissionLocalArgvEnvelope(t, 2, "00000000-0000-7000-8000-000000000075", []string{"status", "--brief", "extra"})
	if got := manager.Evaluate(extended, true, true, extended.RequestedAt.Add(2*time.Second)); got.Disposition != domain.ReviewDispositionHuman || got.MatchedRuleID != "" {
		t.Fatalf("expanded local argv route = %#v", got)
	}
	changedIntent := matching.Intent
	changedIntent.Parameters.WorkingDirectoryID = domain.ActionDigest(strings.Repeat("c", 64))
	changed, err := domain.NewActionEnvelope(
		"00000000-0000-7000-8000-000000000076", matching.SessionID, matching.RunID,
		changedIntent, matching.RequestedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Evaluate(changed, true, true, changed.RequestedAt.Add(2*time.Second)); got.Disposition != domain.ReviewDispositionHuman || got.MatchedRuleID != "" {
		t.Fatalf("changed local execution identity route = %#v", got)
	}
}

func TestSessionPermissionRuleCannotOverrideLocalDenyOrUseStaleAuthority(t *testing.T) {
	sessionID := domain.SessionID("00000000-0000-7000-8000-000000000002")
	manager, err := NewPermissionManager(permissionPolicy(domain.PermissionProfileReadOnly))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.BindSession(sessionID); err != nil {
		t.Fatal(err)
	}
	hooks := &permissionHookOrder{}
	if err := manager.BindInvalidationHooks(hooks, hooks); err != nil {
		t.Fatal(err)
	}
	denied := permissionEnvelope(t, 1, domain.PermissionProfileReadOnly, "00000000-0000-7000-8000-000000000051")
	command := CreateSessionPermissionRuleCommand{
		Actor: PermissionChangeActorLocalUser, RuleID: "00000000-0000-7000-8000-000000000052",
		SessionID: sessionID, Envelope: denied, TargetNamePrefix: "sample",
		CreatedAt: denied.RequestedAt, ExpiresAt: denied.RequestedAt.Add(time.Hour),
	}
	if _, err := manager.CreateSessionRule(context.Background(), command); err != ErrPermissionRuleInvalid {
		t.Fatalf("CreateSessionRule(read-only mutation) error = %v", err)
	}

	// Even a structurally valid in-process rule cannot override the base profile
	// denial. This protects evaluation if local rule state is ever restored from
	// another current-process component incorrectly.
	manager.rules = []SessionPermissionRule{{
		ID: command.RuleID, SessionID: sessionID, Operation: denied.Intent.Operation,
		Scope: denied.Intent.Scope, NamespaceAccess: denied.Intent.NamespaceAccess,
		TargetAPIVersion: denied.Intent.Target.Resource.APIVersion, TargetKind: denied.Intent.Target.Resource.Kind,
		TargetNamespace: denied.Intent.Target.Resource.Namespace, TargetNamePrefix: "sample",
		ParameterKind: denied.Intent.Parameters.Kind, ParameterDigest: denied.Intent.Parameters.Digest(),
		Effect: denied.Intent.Effect, Risk: denied.Intent.Risk,
		DataCategories: denied.Intent.DataCategories, AllowedSinks: denied.Intent.AllowedSinks,
		NetworkEffects: denied.Intent.NetworkEffects, Limits: denied.Intent.Limits,
		PolicyGeneration: 1, CreatedAt: denied.RequestedAt, ExpiresAt: denied.RequestedAt.Add(time.Hour),
	}}
	if manager.rules[0].Validate() != nil {
		t.Fatal("test Session rule is invalid")
	}
	got := manager.Evaluate(denied, true, true, denied.RequestedAt)
	if got.Disposition != domain.ReviewDispositionDeny || got.ReasonCode != "read_only_effect_denied" || got.MatchedRuleID != "" {
		t.Fatalf("read-only denial with Session rule = %#v", got)
	}

	ask, err := NewPermissionManager(permissionPolicy(domain.PermissionProfileAsk))
	if err != nil {
		t.Fatal(err)
	}
	if err := ask.BindSession(sessionID); err != nil {
		t.Fatal(err)
	}
	askHooks := &permissionHookOrder{}
	if err := ask.BindInvalidationHooks(askHooks, askHooks); err != nil {
		t.Fatal(err)
	}
	current := permissionEnvelope(t, 1, domain.PermissionProfileAsk, "00000000-0000-7000-8000-000000000053")
	command.Envelope = current
	command.RuleID = "00000000-0000-7000-8000-000000000054"
	command.CreatedAt = current.ExpiresAt
	command.ExpiresAt = current.ExpiresAt.Add(time.Hour)
	if _, err := ask.CreateSessionRule(context.Background(), command); err != ErrPermissionRuleInvalid {
		t.Fatalf("CreateSessionRule(expired envelope) error = %v", err)
	}
	if status := ask.Status(current.RequestedAt); status.PolicyGeneration != 1 || len(status.SessionRules) != 0 {
		t.Fatalf("expired source changed permission state = %#v", status)
	}
	if got := askHooks.snapshot(); len(got) != 0 {
		t.Fatalf("expired source invoked invalidation hooks = %#v", got)
	}
	if err := ask.Reconfigure(
		context.Background(), PermissionChangeActorLocalUser, permissionPolicy(domain.PermissionProfileAsk),
	); err != nil {
		t.Fatalf("Reconfigure() error = %v", err)
	}
	command.CreatedAt = current.RequestedAt.Add(time.Second)
	command.ExpiresAt = current.RequestedAt.Add(time.Hour)
	if _, err := ask.CreateSessionRule(context.Background(), command); err != ErrPermissionRuleInvalid {
		t.Fatalf("CreateSessionRule(stale generation) error = %v", err)
	}
	if status := ask.Status(current.RequestedAt); status.PolicyGeneration != 2 || len(status.SessionRules) != 0 {
		t.Fatalf("stale source changed permission state = %#v", status)
	}
	if got := askHooks.snapshot(); !reflect.DeepEqual(got, []string{"invalidate:2", "cancel:1"}) {
		t.Fatalf("stale source invoked unexpected invalidation hooks = %#v", got)
	}
}

func TestPermissionGenerationInvalidatesBeforeCancellationAndRulesAreProcessLocal(t *testing.T) {
	manager, err := NewPermissionManager(permissionPolicy(domain.PermissionProfileAsk))
	if err != nil {
		t.Fatalf("NewPermissionManager() error = %v", err)
	}
	sessionID := domain.SessionID("00000000-0000-7000-8000-000000000002")
	if err := manager.BindSession(sessionID); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}
	order := &permissionHookOrder{}
	if err := manager.BindInvalidationHooks(order, order); err != nil {
		t.Fatalf("BindInvalidationHooks() error = %v", err)
	}
	base := permissionEnvelope(t, 1, domain.PermissionProfileAsk, "00000000-0000-7000-8000-000000000001")
	rule, err := manager.CreateSessionRule(context.Background(), CreateSessionPermissionRuleCommand{
		Actor: PermissionChangeActorLocalUser, RuleID: "00000000-0000-7000-8000-000000000010",
		SessionID: sessionID, Envelope: base, TargetNamePrefix: "sample",
		CreatedAt: base.RequestedAt, ExpiresAt: base.RequestedAt.Add(time.Hour),
	})
	if err != nil || rule.PolicyGeneration != 2 {
		t.Fatalf("CreateSessionRule() = %#v/%v", rule, err)
	}
	if got := order.snapshot(); !reflect.DeepEqual(got, []string{"invalidate:2", "cancel:1"}) {
		t.Fatalf("hook order = %#v", got)
	}
	fresh := permissionEnvelope(t, 2, domain.PermissionProfileAsk, "00000000-0000-7000-8000-000000000011")
	got := manager.Evaluate(fresh, true, true, fresh.RequestedAt)
	if got.Disposition != domain.ReviewDispositionAutomatic || got.ReasonCode != "session_rule" || got.MatchedRuleID != rule.ID {
		t.Fatalf("Session rule evaluation = %#v", got)
	}
	if stale := manager.Evaluate(base, true, true, base.RequestedAt); stale.Disposition != domain.ReviewDispositionDeny || stale.ReasonCode != "stale_policy" {
		t.Fatalf("stale envelope = %#v", stale)
	}
	status := manager.Status(fresh.RequestedAt)
	if len(status.SessionRules) != 1 || status.SessionRules[0].ID != rule.ID ||
		status.SessionRules[0].Scope != rule.Scope || status.SessionRules[0].TargetAPIVersion != rule.TargetAPIVersion ||
		status.SessionRules[0].TargetKind != rule.TargetKind || status.SessionRules[0].TargetNamespace != rule.TargetNamespace ||
		status.SessionRules[0].TargetSubresource != rule.TargetSubresource || status.SessionRules[0].TargetPrefix != rule.TargetNamePrefix ||
		status.SessionRules[0].ParameterKind != rule.ParameterKind || status.SessionRules[0].ParameterDigest != rule.ParameterDigest ||
		status.SessionRules[0].Effect != rule.Effect || status.SessionRules[0].Risk != domain.RiskReview ||
		status.SessionRules[0].DataCategories != rule.DataCategories || status.SessionRules[0].AllowedSinks != rule.AllowedSinks ||
		status.SessionRules[0].NetworkEffects != rule.NetworkEffects || status.SessionRules[0].Limits != rule.Limits ||
		!status.SessionRules[0].CreatedAt.Equal(rule.CreatedAt) || !status.SessionRules[0].ExpiresAt.Equal(rule.ExpiresAt) {
		t.Fatalf("Status() = %#v", status)
	}
	if err := manager.ChangeSession(context.Background(), "00000000-0000-7000-8000-000000000020"); err != nil {
		t.Fatalf("ChangeSession() error = %v", err)
	}
	if got := manager.Status(fresh.RequestedAt); len(got.SessionRules) != 0 || got.PolicyGeneration != 3 {
		t.Fatalf("status after Session change = %#v", got)
	}
	newProcess, err := NewPermissionManager(permissionPolicy(domain.PermissionProfileAsk))
	if err != nil {
		t.Fatal(err)
	}
	if got := newProcess.Status(fresh.RequestedAt); len(got.SessionRules) != 0 {
		t.Fatalf("new process restored rules = %#v", got.SessionRules)
	}
}

func TestConcurrentSessionRuleRevocationAdvancesGenerationOnce(t *testing.T) {
	manager, err := NewPermissionManager(permissionPolicy(domain.PermissionProfileAsk))
	if err != nil {
		t.Fatal(err)
	}
	sessionID := domain.SessionID("00000000-0000-7000-8000-000000000002")
	if err := manager.BindSession(sessionID); err != nil {
		t.Fatal(err)
	}
	hooks := &permissionHookOrder{}
	if err := manager.BindInvalidationHooks(hooks, hooks); err != nil {
		t.Fatal(err)
	}
	base := permissionEnvelope(t, 1, domain.PermissionProfileAsk, "00000000-0000-7000-8000-000000000041")
	rule, err := manager.CreateSessionRule(context.Background(), CreateSessionPermissionRuleCommand{
		Actor: PermissionChangeActorLocalUser, RuleID: "00000000-0000-7000-8000-000000000042",
		SessionID: sessionID, Envelope: base, TargetNamePrefix: "sample",
		CreatedAt: base.RequestedAt, ExpiresAt: base.RequestedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateSessionRule() error = %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			<-start
			results <- manager.RevokeSessionRule(context.Background(), PermissionChangeActorLocalUser, rule.ID)
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	succeeded, unavailable := 0, 0
	for result := range results {
		switch result {
		case nil:
			succeeded++
		case ErrPermissionRuleNotFound:
			unavailable++
		default:
			t.Fatalf("RevokeSessionRule() error = %v", result)
		}
	}
	status := manager.Status(base.RequestedAt)
	if succeeded != 1 || unavailable != 1 || status.PolicyGeneration != 3 || len(status.SessionRules) != 0 {
		t.Fatalf("revocation results/status = %d/%d/%#v", succeeded, unavailable, status)
	}
}

func permissionPolicy(profile domain.PermissionProfile) PermissionPolicy {
	return PermissionPolicy{Profile: profile, Generation: 1}
}

func permissionEnvelope(t *testing.T, generation domain.PolicyGeneration, profile domain.PermissionProfile, requestID domain.ApprovalID) domain.ActionEnvelope {
	t.Helper()
	requestedAt := time.UnixMilli(1_700_000_000_000).UTC()
	intent := domain.ActionIntent{
		Operation: domain.ActionOperationRestartDeployment, OperationSchemaVersion: domain.ActionOperationRestartDeployment.SchemaVersion(),
		PolicyVersion: domain.ActionPolicyVersion, PermissionProfile: profile, Risk: domain.RiskReview,
		Effect: domain.CapabilityEffectClusterMutation, PolicyGeneration: generation,
		Scope:           domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		NamespaceAccess: domain.NamespaceAccessCurrent,
		Target: domain.ActionTarget{Resource: domain.ResourceRef{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: "test-namespace", Name: "sample",
			UID: "deployment-uid", ResourceVersion: "42",
		}, Fingerprint: strings.Repeat("a", 64), Generation: 3},
		Parameters:     domain.ActionParameters{Kind: domain.ActionParametersNone},
		DataCategories: domain.ActionDataResourceMetadata, AllowedSinks: domain.ActionSinkKubernetesAPI,
		NetworkEffects:     domain.ActionNetworkKubernetesAPI,
		Limits:             domain.ActionLimits{Timeout: 30 * time.Second, MaximumItems: 1},
		VerificationPlanID: "restart-rollout/v1", ReasonSummary: "Restart one exact Deployment.",
		RiskSummary: "Restarting the Deployment replaces Pods and may temporarily reduce availability.",
	}
	envelope, err := domain.NewActionEnvelope(requestID, "00000000-0000-7000-8000-000000000002", "00000000-0000-7000-8000-000000000003", intent, requestedAt)
	if err != nil {
		t.Fatalf("NewActionEnvelope() error = %v", err)
	}
	return envelope
}

func permissionArgvEnvelope(
	t *testing.T,
	generation domain.PolicyGeneration,
	requestID domain.ApprovalID,
	subresource string,
	executable string,
	argv []string,
) domain.ActionEnvelope {
	t.Helper()
	arguments, err := domain.NewActionArguments(argv)
	if err != nil {
		t.Fatalf("NewActionArguments() error = %v", err)
	}
	requestedAt := time.UnixMilli(1_700_000_000_000).UTC()
	intent := domain.ActionIntent{
		Operation: domain.ActionOperationPodDiagnostic, OperationSchemaVersion: domain.ActionOperationPodDiagnostic.SchemaVersion(),
		PolicyVersion: domain.ActionPolicyVersion, PermissionProfile: domain.PermissionProfileAsk,
		Risk: domain.RiskReview, Effect: domain.CapabilityEffectRemoteExecute, PolicyGeneration: generation,
		Scope:           domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		NamespaceAccess: domain.NamespaceAccessCurrent,
		Target: domain.ActionTarget{Resource: domain.ResourceRef{
			APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod",
			UID: "pod-uid", ResourceVersion: "42",
		}, Subresource: subresource},
		Parameters: domain.ActionParameters{
			Kind: domain.ActionParametersRemoteArgv, Container: "app", Executable: executable, Arguments: arguments,
		},
		DataCategories: domain.ActionDataContainerOutput, AllowedSinks: domain.ActionSinkTerminal,
		NetworkEffects: domain.ActionNetworkKubernetesAPI | domain.ActionNetworkRemotePod,
		NetworkDestinationHash: domain.RemotePodNetworkDestinationHash(domain.ResourceRef{
			APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod", UID: "pod-uid", ResourceVersion: "42",
		}, "app"),
		Limits:             domain.ActionLimits{Timeout: 30 * time.Second, MaximumItems: 1, MaximumOutput: 4096},
		VerificationPlanID: "pod-diagnostic/v1", ReasonSummary: "Run one predefined read-only diagnostic.",
		RiskSummary: "The diagnostic executes fixed read-only arguments in one exact Pod container.",
	}
	envelope, err := domain.NewActionEnvelope(
		requestID, "00000000-0000-7000-8000-000000000002",
		"00000000-0000-7000-8000-000000000003", intent, requestedAt,
	)
	if err != nil {
		t.Fatalf("NewActionEnvelope() error = %v", err)
	}
	return envelope
}

func permissionLocalArgvEnvelope(
	t *testing.T,
	generation domain.PolicyGeneration,
	requestID domain.ApprovalID,
	argv []string,
) domain.ActionEnvelope {
	t.Helper()
	arguments, err := domain.NewActionArguments(argv)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewActionEnvironment([]string{"LC_ALL=C", "NO_COLOR=1"})
	if err != nil {
		t.Fatal(err)
	}
	requestedAt := time.UnixMilli(1_700_000_000_000).UTC()
	intent := domain.ActionIntent{
		Operation:              domain.ActionOperationRestrictedLocalArgv,
		OperationSchemaVersion: domain.ActionOperationRestrictedLocalArgv.SchemaVersion(),
		PolicyVersion:          domain.ActionPolicyVersion,
		PermissionProfile:      domain.PermissionProfileAsk,
		Risk:                   domain.RiskReview,
		Effect:                 domain.CapabilityEffectLocalExecute,
		PolicyGeneration:       generation,
		Scope:                  domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		NamespaceAccess:        domain.NamespaceAccessCurrent,
		Target: domain.ActionTarget{Resource: domain.ResourceRef{
			APIVersion: "v1", Kind: "Namespace", Name: "test-namespace", UID: "namespace-uid", ResourceVersion: "42",
		}, Fingerprint: strings.Repeat("d", 64)},
		Parameters: domain.ActionParameters{
			Kind: domain.ActionParametersLocalArgv, PolicyID: "local-status", Executable: "/opt/bin/diagnostic",
			ExecutableID: domain.ActionDigest(strings.Repeat("a", 64)), Arguments: arguments,
			WorkingDirectory: "/var/empty", WorkingDirectoryID: domain.ActionDigest(strings.Repeat("b", 64)),
			Environment: environment, CredentialReference: domain.LocalCredentialNone,
		},
		DataCategories: domain.ActionDataProcessOutput,
		AllowedSinks:   domain.ActionSinkTerminal | domain.ActionSinkLocalProcess,
		NetworkEffects: domain.ActionNetworkNone,
		Limits: domain.ActionLimits{
			Timeout: 30 * time.Second, MaximumItems: 1, MaximumLines: 20, MaximumBytes: 2048, MaximumOutput: 2048,
		},
		VerificationPlanID: domain.LocalCommandVerificationPlanID,
		ReasonSummary:      "Run one exact configured diagnostic.",
		RiskSummary:        "The approved process runs one exact direct argv vector without a shell or inherited environment.",
	}
	if intent.ValidateLocalCommand() != nil {
		t.Fatalf("local ActionIntent is invalid: %#v", intent)
	}
	envelope, err := domain.NewActionEnvelope(
		requestID, "00000000-0000-7000-8000-000000000002",
		"00000000-0000-7000-8000-000000000003", intent, requestedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

type permissionHookOrder struct {
	mu    sync.Mutex
	order []string
}

func (hook *permissionHookOrder) InvalidatePolicy(generation domain.PolicyGeneration) error {
	hook.mu.Lock()
	defer hook.mu.Unlock()
	hook.order = append(hook.order, "invalidate:"+strconv.FormatInt(int64(generation), 10))
	return nil
}

func (hook *permissionHookOrder) CancelPolicyWork(_ context.Context, generation domain.PolicyGeneration) error {
	hook.mu.Lock()
	defer hook.mu.Unlock()
	hook.order = append(hook.order, "cancel:"+strconv.FormatInt(int64(generation), 10))
	return nil
}

func (hook *permissionHookOrder) snapshot() []string {
	hook.mu.Lock()
	defer hook.mu.Unlock()
	return append([]string(nil), hook.order...)
}
