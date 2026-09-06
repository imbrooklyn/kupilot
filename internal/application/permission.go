package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	DefaultPermissionProfile    = domain.PermissionProfileAsk
	MaxCustomPermissionRoutes   = 64
	MaxSessionPermissionRules   = 64
	MaxSessionPermissionRuleTTL = 24 * time.Hour
)

var (
	ErrPermissionConfiguration = errors.New("permission configuration is invalid")
	ErrPermissionDenied        = errors.New("the action is denied by local permission policy")
	ErrPermissionStale         = errors.New("the permission policy generation is stale")
	ErrPermissionUnavailable   = errors.New("permission state is unavailable")
	ErrPermissionRuleInvalid   = errors.New("Session permission rule data is invalid")
	ErrPermissionRuleNotFound  = errors.New("the Session permission rule is unavailable")
)

// PermissionChangeActor is deliberately limited to an explicit local user.
type PermissionChangeActor string

const PermissionChangeActorLocalUser PermissionChangeActor = "local_user"

// CustomPermissionRoute is one exact code-owned operation/risk route. It does
// not create a capability or lower risk.
type CustomPermissionRoute struct {
	Operation   domain.ActionOperation
	Risk        domain.RiskClass
	Disposition domain.ReviewDisposition
}

func (route CustomPermissionRoute) validate() bool {
	if !route.Operation.Valid() || !route.Risk.Valid() || route.Risk == domain.RiskDeny || !route.Disposition.Valid() {
		return false
	}
	switch route.Risk {
	case domain.RiskSafe:
		return route.Disposition == domain.ReviewDispositionAutomatic ||
			route.Disposition == domain.ReviewDispositionHuman || route.Disposition == domain.ReviewDispositionDeny
	case domain.RiskReview:
		return true
	case domain.RiskCritical:
		return route.Disposition != domain.ReviewDispositionReviewer
	default:
		return false
	}
}

// PermissionPolicy is one immutable routing snapshot.
type PermissionPolicy struct {
	Profile              domain.PermissionProfile
	Generation           domain.PolicyGeneration
	FullAccessAllowed    bool
	HighRiskAcknowledged bool
	CustomRoutes         []CustomPermissionRoute
}

// Validate rejects partial, duplicate, or profile-incompatible routing data.
func (policy PermissionPolicy) Validate() error {
	if !policy.Profile.Valid() || !policy.Generation.Valid() || len(policy.CustomRoutes) > MaxCustomPermissionRoutes {
		return ErrPermissionConfiguration
	}
	if policy.Profile != domain.PermissionProfileCustom && len(policy.CustomRoutes) != 0 {
		return ErrPermissionConfiguration
	}
	switch policy.Profile {
	case domain.PermissionProfileFullAccess:
		if !policy.FullAccessAllowed || !policy.HighRiskAcknowledged {
			return ErrPermissionConfiguration
		}
	case domain.PermissionProfileCustom:
		if policy.FullAccessAllowed {
			return ErrPermissionConfiguration
		}
	default:
		if policy.FullAccessAllowed || policy.HighRiskAcknowledged {
			return ErrPermissionConfiguration
		}
	}
	seen := make(map[[2]string]struct{}, len(policy.CustomRoutes))
	criticalAutomatic := false
	for _, route := range policy.CustomRoutes {
		if !route.validate() {
			return ErrPermissionConfiguration
		}
		key := [2]string{string(route.Operation), string(route.Risk)}
		if _, exists := seen[key]; exists {
			return ErrPermissionConfiguration
		}
		seen[key] = struct{}{}
		if route.Risk == domain.RiskCritical && route.Disposition == domain.ReviewDispositionAutomatic &&
			!policy.HighRiskAcknowledged {
			return ErrPermissionConfiguration
		}
		criticalAutomatic = criticalAutomatic ||
			route.Risk == domain.RiskCritical && route.Disposition == domain.ReviewDispositionAutomatic
	}
	if policy.Profile == domain.PermissionProfileCustom && policy.HighRiskAcknowledged != criticalAutomatic {
		return ErrPermissionConfiguration
	}
	return nil
}

func clonePermissionPolicy(policy PermissionPolicy) PermissionPolicy {
	policy.CustomRoutes = append([]CustomPermissionRoute(nil), policy.CustomRoutes...)
	return policy
}

// PermissionEvaluationInput contains deterministic catalog facts only.
type PermissionEvaluationInput struct {
	Operation          domain.ActionOperation
	Effect             domain.CapabilityEffectClass
	Risk               domain.RiskClass
	CapabilityAdmitted bool
	CapabilityEnabled  bool
}

func (input PermissionEvaluationInput) validate() bool {
	return input.Operation.Valid() && input.Effect.Valid() && input.Risk.Valid()
}

// PermissionEvaluation is a content-free deterministic route.
type PermissionEvaluation struct {
	Profile       domain.PermissionProfile
	Generation    domain.PolicyGeneration
	Risk          domain.RiskClass
	Effect        domain.CapabilityEffectClass
	Disposition   domain.ReviewDisposition
	ReasonCode    string
	MatchedRuleID domain.PermissionRuleID
}

// Validate checks one content-free deterministic routing result.
func (evaluation PermissionEvaluation) Validate() error {
	if !evaluation.Profile.Valid() || !evaluation.Generation.Valid() || !evaluation.Risk.Valid() ||
		!evaluation.Effect.Valid() || !evaluation.Disposition.Valid() || !validPermissionCode(evaluation.ReasonCode) {
		return ErrPermissionConfiguration
	}
	if evaluation.MatchedRuleID != "" && !evaluation.MatchedRuleID.Valid() {
		return ErrPermissionConfiguration
	}
	return nil
}

// EvaluatePermission applies the complete profile matrix without I/O.
func EvaluatePermission(policy PermissionPolicy, input PermissionEvaluationInput) PermissionEvaluation {
	result := PermissionEvaluation{
		Profile: policy.Profile, Generation: policy.Generation, Risk: input.Risk, Effect: input.Effect,
		Disposition: domain.ReviewDispositionDeny, ReasonCode: "invalid_policy",
	}
	if policy.Validate() != nil || !input.validate() {
		return result
	}
	if !input.CapabilityAdmitted {
		result.ReasonCode = "capability_not_admitted"
		return result
	}
	if !input.CapabilityEnabled {
		result.ReasonCode = "capability_disabled"
		return result
	}
	if input.Risk == domain.RiskDeny {
		result.ReasonCode = "hard_deny"
		return result
	}
	if !input.Risk.CompatibleWithEffect(input.Effect) {
		result.ReasonCode = "invalid_policy"
		return result
	}
	switch policy.Profile {
	case domain.PermissionProfileReadOnly:
		switch {
		case input.Risk == domain.RiskSafe && input.Effect == domain.CapabilityEffectSafeRead:
			result.Disposition, result.ReasonCode = domain.ReviewDispositionAutomatic, "safe_automatic"
		case input.Risk == domain.RiskReview && input.Effect.ReadOnlyHumanEligible():
			result.Disposition, result.ReasonCode = domain.ReviewDispositionHuman, "sensitive_read_human"
		default:
			result.ReasonCode = "read_only_effect_denied"
		}
	case domain.PermissionProfileAsk:
		result.Disposition, result.ReasonCode = standardPermissionRoute(input.Risk, false)
	case domain.PermissionProfileAutoReview:
		result.Disposition, result.ReasonCode = standardPermissionRoute(input.Risk, true)
	case domain.PermissionProfileFullAccess:
		result.Disposition, result.ReasonCode = domain.ReviewDispositionAutomatic, "explicit_full_access"
	case domain.PermissionProfileCustom:
		result.Disposition, result.ReasonCode = customPermissionRoute(policy.CustomRoutes, input)
	}
	return result
}

func standardPermissionRoute(risk domain.RiskClass, reviewer bool) (domain.ReviewDisposition, string) {
	switch risk {
	case domain.RiskSafe:
		return domain.ReviewDispositionAutomatic, "safe_automatic"
	case domain.RiskReview:
		if reviewer {
			return domain.ReviewDispositionReviewer, "reviewer_delegated"
		}
		return domain.ReviewDispositionHuman, "human_review_required"
	case domain.RiskCritical:
		return domain.ReviewDispositionHuman, "critical_human_required"
	default:
		return domain.ReviewDispositionDeny, "hard_deny"
	}
}

func customPermissionRoute(routes []CustomPermissionRoute, input PermissionEvaluationInput) (domain.ReviewDisposition, string) {
	for _, route := range routes {
		if route.Operation == input.Operation && route.Risk == input.Risk {
			if route.Disposition == domain.ReviewDispositionDeny {
				return route.Disposition, "custom_deny"
			}
			return route.Disposition, "custom_route"
		}
	}
	if input.Risk == domain.RiskCritical {
		return domain.ReviewDispositionHuman, "custom_critical_human_default"
	}
	return domain.ReviewDispositionDeny, "custom_route_missing"
}

func validPermissionCode(value string) bool {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range []byte(value) {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '_' {
			continue
		}
		return false
	}
	return true
}

// SessionPermissionRule is exact process-local human authority for review
// actions. It is intentionally not a persistence DTO.
type SessionPermissionRule struct {
	ID                     domain.PermissionRuleID
	SessionID              domain.SessionID
	Operation              domain.ActionOperation
	Scope                  domain.ScopeSnapshot
	NamespaceAccess        domain.NamespaceAccessPolicy
	TargetAPIVersion       string
	TargetKind             string
	TargetNamespace        string
	TargetSubresource      string
	TargetNamePrefix       string
	ParameterKind          domain.ActionParameterKind
	ParameterDigest        domain.ActionDigest
	Container              string
	Executable             string
	ArgumentPrefix         domain.ActionArguments
	Effect                 domain.CapabilityEffectClass
	Risk                   domain.RiskClass
	DataCategories         domain.ActionDataCategories
	AllowedSinks           domain.ActionSinks
	NetworkEffects         domain.ActionNetworkEffects
	NetworkDestinationHash domain.ActionDigest
	Limits                 domain.ActionLimits
	PolicyGeneration       domain.PolicyGeneration
	CreatedAt              time.Time
	ExpiresAt              time.Time
}

// Validate checks one exact bounded process-local Session rule.
func (rule SessionPermissionRule) Validate() error {
	target := domain.ResourceRef{APIVersion: rule.TargetAPIVersion, Kind: rule.TargetKind}
	kind, targetKindValid := domain.ResourceKindForReference(target)
	targetNamespaceValid := targetKindValid &&
		(kind.ClusterScoped() && rule.TargetNamespace == "" || kind.Namespaced() && domain.ValidNamespaceName(rule.TargetNamespace))
	remoteArgvRule := rule.ParameterKind == domain.ActionParametersRemoteArgv
	localArgvRule := rule.ParameterKind == domain.ActionParametersLocalArgv
	argumentBindingValid := !remoteArgvRule && !localArgvRule && rule.ParameterDigest.Valid() &&
		rule.Container == "" && rule.Executable == "" && rule.ArgumentPrefix == (domain.ActionArguments{}) ||
		remoteArgvRule && rule.ParameterDigest == "" && (domain.ActionParameters{
			Kind: rule.ParameterKind, Container: rule.Container, Executable: rule.Executable, Arguments: rule.ArgumentPrefix,
		}).Validate() == nil ||
		localArgvRule && rule.ParameterDigest.Valid() && rule.Container == "" &&
			domain.ValidLocalExecutionPath(rule.Executable) && rule.ArgumentPrefix.Valid()
	if !rule.ID.Valid() || !rule.SessionID.Valid() || !rule.Operation.Valid() || rule.Scope.Validate() != nil ||
		!domain.ValidContextName(rule.Scope.Context) || !domain.ValidNamespaceName(rule.Scope.Namespace) ||
		rule.Scope.Generation < 1 || !rule.NamespaceAccess.Valid() || rule.TargetAPIVersion == "" ||
		rule.TargetKind == "" || !targetNamespaceValid || !domain.ValidActionSubresource(rule.TargetSubresource) ||
		!domain.ValidResourceName(rule.TargetNamePrefix) || !rule.ParameterKind.Valid() || !argumentBindingValid ||
		!rule.Effect.Valid() || rule.Risk != domain.RiskReview || !rule.DataCategories.Valid() ||
		!rule.AllowedSinks.Valid() || !rule.NetworkEffects.Valid() ||
		!validPermissionNetworkDestination(rule.NetworkEffects, rule.NetworkDestinationHash) || rule.Limits.Validate() != nil ||
		!rule.PolicyGeneration.Valid() || !validCoordinatorTime(rule.CreatedAt) || !validCoordinatorTime(rule.ExpiresAt) ||
		!rule.ExpiresAt.After(rule.CreatedAt) || rule.ExpiresAt.Sub(rule.CreatedAt) > MaxSessionPermissionRuleTTL {
		return ErrPermissionRuleInvalid
	}
	return nil
}

func (rule SessionPermissionRule) matches(envelope domain.ActionEnvelope, now time.Time) bool {
	if rule.Validate() != nil || envelope.Validate() != nil || !validCoordinatorTime(now) || !now.Before(rule.ExpiresAt) {
		return false
	}
	intent := envelope.Intent
	target := intent.Target.Resource
	return envelope.SessionID == rule.SessionID && intent.Operation == rule.Operation && intent.Scope == rule.Scope &&
		intent.NamespaceAccess == rule.NamespaceAccess && target.APIVersion == rule.TargetAPIVersion &&
		target.Kind == rule.TargetKind && target.Namespace == rule.TargetNamespace &&
		intent.Target.Subresource == rule.TargetSubresource && strings.HasPrefix(target.Name, rule.TargetNamePrefix) &&
		permissionRuleParametersMatch(rule, intent.Parameters) &&
		intent.Effect == rule.Effect && intent.Risk == rule.Risk && intent.DataCategories == rule.DataCategories &&
		intent.AllowedSinks == rule.AllowedSinks && intent.NetworkEffects == rule.NetworkEffects &&
		intent.NetworkDestinationHash == rule.NetworkDestinationHash &&
		intent.Limits == rule.Limits && intent.PolicyGeneration == rule.PolicyGeneration
}

func validPermissionNetworkDestination(effects domain.ActionNetworkEffects, destination domain.ActionDigest) bool {
	requiresHash := effects&(domain.ActionNetworkModelOrigin|domain.ActionNetworkDataSource|domain.ActionNetworkRemotePod|domain.ActionNetworkExternalCommand) != 0
	if requiresHash {
		return destination.Valid()
	}
	return destination == ""
}

func permissionRuleParametersMatch(rule SessionPermissionRule, parameters domain.ActionParameters) bool {
	if parameters.Validate() != nil || parameters.Kind != rule.ParameterKind {
		return false
	}
	if rule.ParameterKind != domain.ActionParametersRemoteArgv && rule.ParameterKind != domain.ActionParametersLocalArgv {
		return parameters.Digest().Equal(rule.ParameterDigest)
	}
	if rule.ParameterKind == domain.ActionParametersLocalArgv && !parameters.Digest().Equal(rule.ParameterDigest) {
		return false
	}
	if parameters.Container != rule.Container || parameters.Executable != rule.Executable {
		return false
	}
	prefix := rule.ArgumentPrefix.Values()
	actual := parameters.Arguments.Values()
	if len(prefix) == 0 || len(actual) < len(prefix) ||
		rule.ParameterKind == domain.ActionParametersLocalArgv && len(actual) != len(prefix) {
		return false
	}
	for index := range prefix {
		if prefix[index] != actual[index] {
			return false
		}
	}
	return true
}

// CreateSessionPermissionRuleCommand is accepted only from an explicit local-user flow.
type CreateSessionPermissionRuleCommand struct {
	Actor               PermissionChangeActor
	RuleID              domain.PermissionRuleID
	SessionID           domain.SessionID
	Envelope            domain.ActionEnvelope
	TargetNamePrefix    string
	ArgumentPrefixCount int
	CreatedAt           time.Time
	ExpiresAt           time.Time
}

// PermissionInvalidationHook makes pending actions and Reviewer work stale
// after the new generation is committed and before old work is cancelled.
type PermissionInvalidationHook interface {
	InvalidatePolicy(domain.PolicyGeneration) error
}

// PermissionWorkCanceller cancels old run work after authority invalidation.
type PermissionWorkCanceller interface {
	CancelPolicyWork(context.Context, domain.PolicyGeneration) error
}

// PermissionManager owns current process policy and Session rules. It performs
// no persistence and its status/query methods perform no external I/O.
type PermissionManager struct {
	changeMu sync.Mutex
	mu       sync.RWMutex

	policy       PermissionPolicy
	healthy      bool
	sessionID    domain.SessionID
	rules        []SessionPermissionRule
	invalidation PermissionInvalidationHook
	canceller    PermissionWorkCanceller
}

// NewPermissionManager creates generation one. An empty profile means ask.
func NewPermissionManager(policy PermissionPolicy) (*PermissionManager, error) {
	if policy.Profile == "" {
		policy.Profile = DefaultPermissionProfile
	}
	if policy.Generation == 0 {
		policy.Generation = 1
	}
	if policy.Validate() != nil {
		return nil, ErrPermissionConfiguration
	}
	return &PermissionManager{policy: clonePermissionPolicy(policy), healthy: true}, nil
}

// BindInvalidationHooks is allowed once before any policy change.
func (manager *PermissionManager) BindInvalidationHooks(invalidation PermissionInvalidationHook, canceller PermissionWorkCanceller) error {
	if manager == nil || invalidation == nil || canceller == nil {
		return ErrPermissionConfiguration
	}
	manager.changeMu.Lock()
	defer manager.changeMu.Unlock()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.invalidation != nil || manager.canceller != nil {
		return ErrPermissionConfiguration
	}
	manager.invalidation = invalidation
	manager.canceller = canceller
	return nil
}

// BindSession establishes the initial process-local Session without changing generation.
func (manager *PermissionManager) BindSession(sessionID domain.SessionID) error {
	if manager == nil || !sessionID.Valid() {
		return ErrPermissionConfiguration
	}
	manager.changeMu.Lock()
	defer manager.changeMu.Unlock()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.sessionID != "" && manager.sessionID != sessionID {
		return ErrPermissionStale
	}
	manager.sessionID = sessionID
	return nil
}

// Reconfigure commits a local-user profile change, invalidates authority, and
// only then cancels old work.
func (manager *PermissionManager) Reconfigure(ctx context.Context, actor PermissionChangeActor, next PermissionPolicy) error {
	if actor != PermissionChangeActorLocalUser || ctx == nil || ctx.Err() != nil {
		return ErrPermissionConfiguration
	}
	return manager.reconfigure(ctx, 0, next)
}

// ReconfigureAtGeneration applies a delivery-requested profile only when the
// displayed policy generation is still current. The generation check and the
// change share the manager's serialized transition, so a stale picker cannot
// overwrite a newer local policy.
func (manager *PermissionManager) ReconfigureAtGeneration(
	ctx context.Context,
	actor PermissionChangeActor,
	expected domain.PolicyGeneration,
	next PermissionPolicy,
) error {
	if actor != PermissionChangeActorLocalUser || ctx == nil || ctx.Err() != nil || !expected.Valid() {
		return ErrPermissionConfiguration
	}
	return manager.reconfigure(ctx, expected, next)
}

func (manager *PermissionManager) reconfigure(
	ctx context.Context,
	expected domain.PolicyGeneration,
	next PermissionPolicy,
) error {
	return manager.advance(ctx, func(current PermissionPolicy, generation domain.PolicyGeneration) (PermissionPolicy, error) {
		if expected != 0 && current.Generation != expected {
			return PermissionPolicy{}, ErrPermissionStale
		}
		next.Generation = generation
		if next.Validate() != nil {
			return PermissionPolicy{}, ErrPermissionConfiguration
		}
		return clonePermissionPolicy(next), nil
	})
}

// InvalidateOriginPolicy advances generation without changing the selected profile.
func (manager *PermissionManager) InvalidateOriginPolicy(ctx context.Context) error {
	return manager.advance(ctx, func(current PermissionPolicy, generation domain.PolicyGeneration) (PermissionPolicy, error) {
		current.Generation = generation
		return current, nil
	})
}

// ChangeSession clears every process-local rule and advances policy generation.
func (manager *PermissionManager) ChangeSession(ctx context.Context, sessionID domain.SessionID) error {
	if !sessionID.Valid() {
		return ErrPermissionRuleInvalid
	}
	return manager.advance(ctx, func(current PermissionPolicy, generation domain.PolicyGeneration) (PermissionPolicy, error) {
		current.Generation = generation
		return current, nil
	}, func() { manager.sessionID = sessionID })
}

func (manager *PermissionManager) advance(
	ctx context.Context,
	build func(PermissionPolicy, domain.PolicyGeneration) (PermissionPolicy, error),
	afterLocked ...func(),
) error {
	if manager == nil || ctx == nil {
		return ErrPermissionConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	manager.changeMu.Lock()
	defer manager.changeMu.Unlock()
	manager.mu.Lock()
	if manager.invalidation == nil || manager.canceller == nil || !manager.healthy {
		manager.mu.Unlock()
		return ErrPermissionUnavailable
	}
	nextGeneration, err := manager.policy.Generation.Next()
	if err != nil {
		manager.healthy = false
		manager.mu.Unlock()
		return ErrPermissionUnavailable
	}
	next, err := build(clonePermissionPolicy(manager.policy), nextGeneration)
	if err != nil {
		manager.mu.Unlock()
		return err
	}
	previous := manager.policy.Generation
	manager.policy = next
	manager.rules = nil
	for _, change := range afterLocked {
		change()
	}
	invalidation := manager.invalidation
	canceller := manager.canceller
	manager.mu.Unlock()
	invalidationErr := invalidation.InvalidatePolicy(nextGeneration)
	cancelErr := canceller.CancelPolicyWork(ctx, previous)
	if invalidationErr != nil || cancelErr != nil {
		manager.mu.Lock()
		manager.healthy = false
		manager.mu.Unlock()
		return ErrPermissionUnavailable
	}
	return nil
}

// Evaluate returns one route and applies any exact current Session rule.
func (manager *PermissionManager) Evaluate(envelope domain.ActionEnvelope, admitted, enabled bool, now time.Time) PermissionEvaluation {
	if manager == nil || envelope.Validate() != nil || !validCoordinatorTime(now) {
		return PermissionEvaluation{Disposition: domain.ReviewDispositionDeny, ReasonCode: "invalid_policy"}
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	input := PermissionEvaluationInput{
		Operation: envelope.Intent.Operation, Effect: envelope.Intent.Effect, Risk: envelope.Intent.Risk,
		CapabilityAdmitted: admitted, CapabilityEnabled: enabled,
	}
	result := EvaluatePermission(manager.policy, input)
	if !manager.healthy || envelope.Intent.PermissionProfile != manager.policy.Profile ||
		envelope.Intent.PolicyGeneration != manager.policy.Generation || envelope.SessionID != manager.sessionID {
		result.Disposition, result.ReasonCode = domain.ReviewDispositionDeny, "stale_policy"
		return result
	}
	if envelope.Intent.Risk == domain.RiskReview &&
		(result.Disposition == domain.ReviewDispositionHuman || result.Disposition == domain.ReviewDispositionReviewer) {
		for _, rule := range manager.rules {
			if rule.matches(envelope, now) {
				result.Disposition = domain.ReviewDispositionAutomatic
				result.ReasonCode = "session_rule"
				result.MatchedRuleID = rule.ID
				break
			}
		}
	}
	return result
}

// EvaluateCatalog applies hard local policy before any target revalidation or
// external call. The returned policy snapshot is the only one that may stamp
// a subsequently prepared ActionIntent.
func (manager *PermissionManager) EvaluateCatalog(
	sessionID domain.SessionID,
	input PermissionEvaluationInput,
) (PermissionPolicy, PermissionEvaluation) {
	if manager == nil || !sessionID.Valid() || !input.validate() {
		return PermissionPolicy{}, PermissionEvaluation{Disposition: domain.ReviewDispositionDeny, ReasonCode: "invalid_policy"}
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	policy := clonePermissionPolicy(manager.policy)
	result := EvaluatePermission(policy, input)
	if !manager.healthy || manager.sessionID != sessionID {
		result.Disposition, result.ReasonCode = domain.ReviewDispositionDeny, "stale_policy"
	}
	return policy, result
}

// NewRestartDeploymentActionIntent stamps one prepared exact target with the
// current deterministic policy snapshot. It performs no I/O.
func NewRestartDeploymentActionIntent(
	policy PermissionPolicy,
	scope domain.ClusterScope,
	target domain.ActionTarget,
	reason string,
) (domain.ActionIntent, error) {
	if policy.Validate() != nil || scope.Validate() != nil || target.Validate() != nil ||
		!scope.AllowsReference(target.Resource) || !domain.ValidActionReasonSummary(reason) {
		return domain.ActionIntent{}, ErrPermissionConfiguration
	}
	intent := domain.ActionIntent{
		Operation:              domain.ActionOperationRestartDeployment,
		OperationSchemaVersion: domain.ActionOperationRestartDeployment.SchemaVersion(),
		PolicyVersion:          domain.ActionPolicyVersion, PermissionProfile: policy.Profile,
		Risk: domain.RiskReview, Effect: domain.CapabilityEffectClusterMutation,
		PolicyGeneration: policy.Generation, Scope: scope.Snapshot(), NamespaceAccess: scope.NamespaceAccess,
		Target: target, Parameters: domain.ActionParameters{Kind: domain.ActionParametersNone},
		DataCategories:     domain.ActionDataResourceMetadata,
		AllowedSinks:       domain.ActionSinkTerminal | domain.ActionSinkKubernetesAPI,
		NetworkEffects:     domain.ActionNetworkKubernetesAPI,
		Limits:             domain.ActionLimits{Timeout: domain.RestartDeploymentActionTimeout, MaximumItems: 1},
		VerificationPlanID: domain.RestartDeploymentVerificationPlanID,
		ReasonSummary:      reason, RiskSummary: domain.RestartDeploymentRiskSummary,
	}
	if intent.ValidateRestartDeployment() != nil {
		return domain.ActionIntent{}, ErrPermissionConfiguration
	}
	return intent, nil
}

// CreateSessionRule advances policy generation, invalidating the source
// envelope; the returned rule applies only to freshly canonicalized actions.
func (manager *PermissionManager) CreateSessionRule(ctx context.Context, command CreateSessionPermissionRuleCommand) (SessionPermissionRule, error) {
	if manager == nil || ctx == nil || command.Actor != PermissionChangeActorLocalUser || !command.RuleID.Valid() ||
		!command.SessionID.Valid() || command.Envelope.Validate() != nil || command.Envelope.SessionID != command.SessionID ||
		command.Envelope.Intent.Risk != domain.RiskReview || !domain.ValidResourceName(command.TargetNamePrefix) ||
		!validCoordinatorTime(command.CreatedAt) || !validCoordinatorTime(command.ExpiresAt) ||
		command.CreatedAt.Before(command.Envelope.RequestedAt) || !command.CreatedAt.Before(command.Envelope.ExpiresAt) {
		return SessionPermissionRule{}, ErrPermissionRuleInvalid
	}
	parameterDigest, container, executable, argumentPrefix, err := permissionRuleParameterBinding(
		command.Envelope.Intent.Parameters, command.ArgumentPrefixCount,
	)
	if err != nil {
		return SessionPermissionRule{}, err
	}
	var created SessionPermissionRule
	var retained []SessionPermissionRule
	err = manager.advance(ctx, func(current PermissionPolicy, generation domain.PolicyGeneration) (PermissionPolicy, error) {
		intent := command.Envelope.Intent
		sourceRoute := EvaluatePermission(current, PermissionEvaluationInput{
			Operation: intent.Operation, Effect: intent.Effect, Risk: intent.Risk,
			CapabilityAdmitted: true, CapabilityEnabled: true,
		})
		if manager.sessionID != command.SessionID || intent.PermissionProfile != current.Profile ||
			intent.PolicyGeneration != current.Generation ||
			(sourceRoute.Disposition != domain.ReviewDispositionHuman &&
				sourceRoute.Disposition != domain.ReviewDispositionReviewer) {
			return PermissionPolicy{}, ErrPermissionRuleInvalid
		}
		if len(manager.rules) >= MaxSessionPermissionRules {
			return PermissionPolicy{}, ErrPermissionRuleInvalid
		}
		retained = make([]SessionPermissionRule, 0, len(manager.rules)+1)
		for _, existing := range manager.rules {
			if existing.ID == command.RuleID {
				return PermissionPolicy{}, ErrPermissionRuleInvalid
			}
			existing.PolicyGeneration = generation
			retained = append(retained, existing)
		}
		current.Generation = generation
		created = SessionPermissionRule{
			ID: command.RuleID, SessionID: command.SessionID, Operation: intent.Operation,
			Scope: intent.Scope, NamespaceAccess: intent.NamespaceAccess,
			TargetAPIVersion: intent.Target.Resource.APIVersion, TargetKind: intent.Target.Resource.Kind,
			TargetNamespace: intent.Target.Resource.Namespace, TargetNamePrefix: command.TargetNamePrefix,
			TargetSubresource: intent.Target.Subresource, ParameterKind: intent.Parameters.Kind,
			ParameterDigest: parameterDigest, Container: container, Executable: executable, ArgumentPrefix: argumentPrefix,
			Effect: intent.Effect, Risk: intent.Risk,
			DataCategories: intent.DataCategories, AllowedSinks: intent.AllowedSinks,
			NetworkEffects: intent.NetworkEffects, NetworkDestinationHash: intent.NetworkDestinationHash,
			Limits:           intent.Limits,
			PolicyGeneration: generation, CreatedAt: command.CreatedAt, ExpiresAt: command.ExpiresAt,
		}
		if created.Validate() != nil || manager.sessionID != command.SessionID {
			return PermissionPolicy{}, ErrPermissionRuleInvalid
		}
		retained = append(retained, created)
		return current, nil
	}, func() { manager.rules = retained })
	if err != nil {
		return SessionPermissionRule{}, err
	}
	return created, nil
}

func permissionRuleParameterBinding(
	parameters domain.ActionParameters,
	prefixCount int,
) (domain.ActionDigest, string, string, domain.ActionArguments, error) {
	if parameters.Validate() != nil || prefixCount < 0 {
		return "", "", "", domain.ActionArguments{}, ErrPermissionRuleInvalid
	}
	if parameters.Kind != domain.ActionParametersRemoteArgv && parameters.Kind != domain.ActionParametersLocalArgv {
		if prefixCount != 0 {
			return "", "", "", domain.ActionArguments{}, ErrPermissionRuleInvalid
		}
		return parameters.Digest(), "", "", domain.ActionArguments{}, nil
	}
	values := parameters.Arguments.Values()
	if prefixCount < 1 || prefixCount > len(values) {
		return "", "", "", domain.ActionArguments{}, ErrPermissionRuleInvalid
	}
	if parameters.Kind == domain.ActionParametersLocalArgv && prefixCount != len(values) {
		return "", "", "", domain.ActionArguments{}, ErrPermissionRuleInvalid
	}
	prefix, err := domain.NewActionArguments(values[:prefixCount])
	if err != nil {
		return "", "", "", domain.ActionArguments{}, ErrPermissionRuleInvalid
	}
	digest := domain.ActionDigest("")
	if parameters.Kind == domain.ActionParametersLocalArgv {
		digest = parameters.Digest()
	}
	return digest, parameters.Container, parameters.Executable, prefix, nil
}

// RevokeSessionRule removes explicit process authority and advances generation.
func (manager *PermissionManager) RevokeSessionRule(ctx context.Context, actor PermissionChangeActor, ruleID domain.PermissionRuleID) error {
	if manager == nil || actor != PermissionChangeActorLocalUser || !ruleID.Valid() {
		return ErrPermissionRuleInvalid
	}
	var retained []SessionPermissionRule
	return manager.advance(ctx, func(current PermissionPolicy, generation domain.PolicyGeneration) (PermissionPolicy, error) {
		found := false
		for _, rule := range manager.rules {
			found = found || rule.ID == ruleID
		}
		if !found {
			return PermissionPolicy{}, ErrPermissionRuleNotFound
		}
		current.Generation = generation
		retained = make([]SessionPermissionRule, 0, len(manager.rules)-1)
		for _, rule := range manager.rules {
			if rule.ID == ruleID {
				continue
			}
			rule.PolicyGeneration = generation
			retained = append(retained, rule)
		}
		return current, nil
	}, func() { manager.rules = retained })
}

// Policy returns a defensive local snapshot without performing I/O.
func (manager *PermissionManager) Policy() (PermissionPolicy, bool) {
	if manager == nil {
		return PermissionPolicy{}, false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return clonePermissionPolicy(manager.policy), manager.healthy
}

// ActionCurrent performs the final no-I/O generation/profile/Session check.
func (manager *PermissionManager) ActionCurrent(envelope domain.ActionEnvelope) bool {
	if manager == nil || envelope.Validate() != nil {
		return false
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.healthy && manager.sessionID == envelope.SessionID &&
		manager.policy.Profile == envelope.Intent.PermissionProfile &&
		manager.policy.Generation == envelope.Intent.PolicyGeneration
}

// PermissionStatus is content-free and safe for /permissions and /status.
type PermissionStatus struct {
	Profile              domain.PermissionProfile
	PolicyGeneration     domain.PolicyGeneration
	FullAccessAllowed    bool
	HighRiskAcknowledged bool
	Healthy              bool
	SessionID            domain.SessionID
	CustomRoutes         []CustomPermissionRoute
	SessionRules         []SessionPermissionRuleStatus
}

type SessionPermissionRuleStatus struct {
	ID                     domain.PermissionRuleID
	Operation              domain.ActionOperation
	Scope                  domain.ScopeSnapshot
	NamespaceAccess        domain.NamespaceAccessPolicy
	PolicyGeneration       domain.PolicyGeneration
	TargetAPIVersion       string
	TargetKind             string
	TargetNamespace        string
	TargetSubresource      string
	TargetPrefix           string
	ParameterKind          domain.ActionParameterKind
	ParameterDigest        domain.ActionDigest
	Container              string
	Executable             string
	ArgumentPrefix         domain.ActionArguments
	Effect                 domain.CapabilityEffectClass
	Risk                   domain.RiskClass
	DataCategories         domain.ActionDataCategories
	AllowedSinks           domain.ActionSinks
	NetworkEffects         domain.ActionNetworkEffects
	NetworkDestinationHash domain.ActionDigest
	Limits                 domain.ActionLimits
	CreatedAt              time.Time
	ExpiresAt              time.Time
}

// Status returns an independent local snapshot without repository or external I/O.
func (manager *PermissionManager) Status(now time.Time) PermissionStatus {
	if manager == nil || !validCoordinatorTime(now) {
		return PermissionStatus{}
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	status := PermissionStatus{
		Profile: manager.policy.Profile, PolicyGeneration: manager.policy.Generation,
		FullAccessAllowed: manager.policy.FullAccessAllowed, HighRiskAcknowledged: manager.policy.HighRiskAcknowledged,
		Healthy: manager.healthy, SessionID: manager.sessionID,
		CustomRoutes: append([]CustomPermissionRoute(nil), manager.policy.CustomRoutes...),
		SessionRules: make([]SessionPermissionRuleStatus, 0, len(manager.rules)),
	}
	for _, rule := range manager.rules {
		if now.Before(rule.ExpiresAt) {
			status.SessionRules = append(status.SessionRules, SessionPermissionRuleStatus{
				ID: rule.ID, Operation: rule.Operation, Scope: rule.Scope, NamespaceAccess: rule.NamespaceAccess,
				PolicyGeneration: rule.PolicyGeneration, TargetAPIVersion: rule.TargetAPIVersion,
				TargetKind: rule.TargetKind, TargetNamespace: rule.TargetNamespace,
				TargetSubresource: rule.TargetSubresource, TargetPrefix: rule.TargetNamePrefix,
				ParameterKind: rule.ParameterKind, ParameterDigest: rule.ParameterDigest,
				Container: rule.Container, Executable: rule.Executable, ArgumentPrefix: rule.ArgumentPrefix,
				Effect: rule.Effect, Risk: rule.Risk, DataCategories: rule.DataCategories,
				AllowedSinks: rule.AllowedSinks, NetworkEffects: rule.NetworkEffects,
				NetworkDestinationHash: rule.NetworkDestinationHash, Limits: rule.Limits,
				CreatedAt: rule.CreatedAt, ExpiresAt: rule.ExpiresAt,
			})
		}
	}
	return status
}
