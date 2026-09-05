package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	// RemediationPolicyVersion identifies the exact typed mutation catalog.
	RemediationPolicyVersion = "kupilot.remediation-policy/v1"
	// MaxRemediationPlanMembers prevents one approval from expanding into an
	// unbounded multi-object operation.
	MaxRemediationPlanMembers = 128
	// RemediationGracePeriodSeconds is the sole admitted non-zero Pod grace
	// period for delete and drain operations.
	RemediationGracePeriodSeconds int64 = 30

	ScaleVerificationPlanID       = "workload-scale-observation/v1"
	RollbackVerificationPlanID    = "deployment-rollback-rollout/v1"
	PodDeleteVerificationPlanID   = "owned-pod-replacement/v1"
	CordonVerificationPlanID      = "node-unschedulable-true/v1"
	UncordonVerificationPlanID    = "node-unschedulable-false/v1"
	DrainVerificationPlanID       = "node-drain-plan/v1"
	TypedRemediationActionTimeout = 5 * time.Minute
)

var ErrInvalidRemediation = errors.New("typed remediation data is invalid")

// RemediationMemberRole fixes why one object is part of a composite action.
type RemediationMemberRole string

const (
	RemediationMemberRollbackSource RemediationMemberRole = "rollback_source"
	RemediationMemberPodController  RemediationMemberRole = "pod_controller"
	RemediationMemberDrainPod       RemediationMemberRole = "drain_pod"
	RemediationMemberDrainPDB       RemediationMemberRole = "drain_pdb"
)

func (role RemediationMemberRole) valid() bool {
	switch role {
	case RemediationMemberRollbackSource, RemediationMemberPodController,
		RemediationMemberDrainPod, RemediationMemberDrainPDB:
		return true
	default:
		return false
	}
}

// RemediationPlanMember is one safe immutable projection in an approved
// target set. No selector, object body, patch, or eviction request crosses the
// Domain boundary.
type RemediationPlanMember struct {
	Role               RemediationMemberRole
	Resource           ResourceRef
	Fingerprint        ActionDigest
	Revision           int64
	Order              int
	DisruptionsAllowed int32
}

func (member RemediationPlanMember) validate() error {
	if !member.Role.valid() || member.Resource.Validate() != nil || member.Resource.UID == "" ||
		member.Resource.ResourceVersion == "" || member.Revision < 0 || member.Order < 0 ||
		member.DisruptionsAllowed < 0 || member.Fingerprint != "" && !member.Fingerprint.Valid() {
		return ErrInvalidRemediation
	}
	switch member.Role {
	case RemediationMemberRollbackSource:
		if member.Resource.APIVersion != "apps/v1" || member.Resource.Kind != "ReplicaSet" ||
			member.Revision < 1 || member.Order != 0 || member.DisruptionsAllowed != 0 || !member.Fingerprint.Valid() {
			return ErrInvalidRemediation
		}
	case RemediationMemberPodController:
		if member.Resource.Namespace == "" || member.Order != 0 || member.Revision != 0 ||
			member.DisruptionsAllowed != 0 || member.Fingerprint != "" {
			return ErrInvalidRemediation
		}
	case RemediationMemberDrainPod:
		if member.Resource.APIVersion != "v1" || member.Resource.Kind != "Pod" || member.Resource.Namespace == "" ||
			member.Order < 1 || member.Revision != 0 || member.DisruptionsAllowed != 0 || !member.Fingerprint.Valid() {
			return ErrInvalidRemediation
		}
	case RemediationMemberDrainPDB:
		if member.Resource.APIVersion != "policy/v1" || member.Resource.Kind != "PodDisruptionBudget" ||
			member.Resource.Namespace == "" || member.Order != 0 || member.Revision != 0 || member.Fingerprint != "" {
			return ErrInvalidRemediation
		}
	}
	return nil
}

func (member RemediationPlanMember) canonical() string {
	return strings.Join([]string{
		string(member.Role), member.Resource.APIVersion, member.Resource.Kind, member.Resource.Namespace,
		member.Resource.Name, member.Resource.UID, member.Resource.ResourceVersion,
		string(member.Fingerprint), strconv.FormatInt(member.Revision, 10), strconv.Itoa(member.Order),
		strconv.FormatInt(int64(member.DisruptionsAllowed), 10),
	}, "\n")
}

// RemediationTargetSet is immutable-by-construction and preserves execution
// order. The Kubernetes preparer supplies a deterministic order; callers
// cannot append targets after approval.
type RemediationTargetSet struct {
	members [MaxRemediationPlanMembers]RemediationPlanMember
	count   uint8
}

func NewRemediationTargetSet(members []RemediationPlanMember) (RemediationTargetSet, error) {
	var result RemediationTargetSet
	if len(members) < 1 || len(members) > MaxRemediationPlanMembers {
		return result, ErrInvalidRemediation
	}
	seen := make(map[string]struct{}, len(members))
	lastDrainOrder := 0
	for index, member := range members {
		if member.validate() != nil {
			return RemediationTargetSet{}, ErrInvalidRemediation
		}
		identity := strings.Join([]string{member.Resource.APIVersion, member.Resource.Kind, member.Resource.Namespace, member.Resource.Name, member.Resource.UID}, "\x00")
		if _, exists := seen[identity]; exists {
			return RemediationTargetSet{}, ErrInvalidRemediation
		}
		seen[identity] = struct{}{}
		if member.Role == RemediationMemberDrainPod {
			if member.Order != lastDrainOrder+1 {
				return RemediationTargetSet{}, ErrInvalidRemediation
			}
			lastDrainOrder = member.Order
		}
		result.members[index] = member
		result.count++
	}
	return result, nil
}

func (set RemediationTargetSet) Members() []RemediationPlanMember {
	if int(set.count) > len(set.members) {
		return nil
	}
	return append([]RemediationPlanMember(nil), set.members[:set.count]...)
}

func (set RemediationTargetSet) Validate() error {
	rebuilt, err := NewRemediationTargetSet(set.Members())
	if err != nil || rebuilt != set {
		return ErrInvalidRemediation
	}
	return nil
}

func (set RemediationTargetSet) Digest() ActionDigest {
	if set.Validate() != nil {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("kupilot.remediation-target-set/v1\n")
	for _, member := range set.Members() {
		value := member.canonical()
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return ActionDigest(hex.EncodeToString(digest[:]))
}

// RemediationActionPlan is produced only by a fresh Kubernetes preparation
// read. Model text can select the typed operation, candidate target, and one
// bounded parameter, but cannot supply identity or a target set.
type RemediationActionPlan struct {
	RunID            AgentRunID
	SessionID        SessionID
	Scope            ClusterScope
	PolicyGeneration PolicyGeneration
	Operation        ActionOperation
	Target           ActionTarget
	Parameters       ActionParameters
	TargetSet        RemediationTargetSet
	ReasonSummary    string
	Limits           ActionLimits
	PreparedAt       time.Time
}

func (plan RemediationActionPlan) Validate() error {
	if !plan.RunID.Valid() || !plan.SessionID.Valid() || plan.Scope.Validate() != nil || !plan.PolicyGeneration.Valid() ||
		plan.Target.Validate() != nil || plan.Parameters.Validate() != nil || !ValidActionReasonSummary(plan.ReasonSummary) ||
		plan.Limits.Validate() != nil || plan.Limits.Timeout != TypedRemediationActionTimeout || plan.Limits.MaximumOutput != 0 ||
		plan.PreparedAt.IsZero() || plan.PreparedAt.Location() != time.UTC || plan.PreparedAt.UnixMilli() < 0 ||
		!plan.PreparedAt.Equal(time.UnixMilli(plan.PreparedAt.UnixMilli()).UTC()) || plan.PreparedAt.Before(plan.Scope.ActivatedAt) {
		return ErrInvalidRemediation
	}
	resource := plan.Target.Resource
	if !plan.Scope.AllowsReference(resource) {
		return ErrInvalidRemediation
	}
	for _, member := range plan.TargetSet.Members() {
		if !plan.Scope.AllowsReference(member.Resource) {
			return ErrInvalidRemediation
		}
	}
	switch plan.Operation {
	case ActionOperationScaleWorkload:
		if resource.APIVersion != "apps/v1" || resource.Kind != "Deployment" && resource.Kind != "StatefulSet" ||
			resource.Namespace == "" || plan.Target.Subresource != "scale" || !validSHA256Hex(plan.Target.Fingerprint) ||
			plan.Target.Generation < 1 || plan.Target.Revision != 0 || plan.Target.TargetCount != 0 ||
			plan.Parameters.Kind != ActionParametersReplicaTarget || plan.TargetSet != (RemediationTargetSet{}) ||
			plan.Parameters.ReplicaCurrent == plan.Parameters.ReplicaTarget ||
			plan.Limits.MaximumItems != 1 || plan.Limits.MaximumBytes != 0 || plan.Limits.MaximumLines != 0 {
			return ErrInvalidRemediation
		}
	case ActionOperationRollbackDeployment:
		if resource.APIVersion != "apps/v1" || resource.Kind != "Deployment" || resource.Namespace == "" ||
			plan.Target.Subresource != "" || !validSHA256Hex(plan.Target.Fingerprint) || plan.Target.Generation < 1 ||
			plan.Target.Revision < 1 || plan.Parameters.Kind != ActionParametersRevision || plan.TargetSet.Validate() != nil ||
			plan.TargetSet.Digest() != plan.Target.TargetSetDigest || plan.Target.TargetCount != 1 || len(plan.TargetSet.Members()) != 1 ||
			plan.TargetSet.Members()[0].Role != RemediationMemberRollbackSource ||
			plan.TargetSet.Members()[0].Revision != plan.Parameters.Revision || plan.Target.Revision <= plan.Parameters.Revision ||
			plan.Limits.MaximumItems != 1 || plan.Limits.MaximumBytes != 0 || plan.Limits.MaximumLines != 0 {
			return ErrInvalidRemediation
		}
	case ActionOperationDeleteOwnedPod:
		if resource.APIVersion != "v1" || resource.Kind != "Pod" || resource.Namespace == "" ||
			plan.Target.Subresource != "" || !validSHA256Hex(plan.Target.Fingerprint) || plan.Target.Generation != 0 || plan.Target.Revision != 0 ||
			plan.Parameters.Kind != ActionParametersPodDelete || plan.TargetSet.Validate() != nil ||
			plan.TargetSet.Digest() != plan.Target.TargetSetDigest || plan.Target.TargetCount != 1 || len(plan.TargetSet.Members()) != 1 ||
			plan.TargetSet.Members()[0].Role != RemediationMemberPodController ||
			plan.Limits.MaximumItems != 1 || plan.Limits.MaximumBytes != 0 || plan.Limits.MaximumLines != 0 {
			return ErrInvalidRemediation
		}
	case ActionOperationCordonNode, ActionOperationUncordonNode:
		want := plan.Operation == ActionOperationCordonNode
		if resource.APIVersion != "v1" || resource.Kind != "Node" || resource.Namespace != "" || plan.Target.Subresource != "" ||
			plan.Target.Fingerprint != "" || plan.Target.Generation != 0 || plan.Target.Revision != 0 || plan.Target.TargetCount != 0 ||
			plan.Parameters.Kind != ActionParametersNodeScheduling || plan.Parameters.Unschedulable != want ||
			plan.TargetSet != (RemediationTargetSet{}) || plan.Limits.MaximumItems != 1 || plan.Limits.MaximumBytes != 0 || plan.Limits.MaximumLines != 0 {
			return ErrInvalidRemediation
		}
	case ActionOperationDrainNode:
		if plan.Scope.NamespaceAccess != NamespaceAccessAll ||
			resource.APIVersion != "v1" || resource.Kind != "Node" || resource.Namespace != "" || plan.Target.Subresource != "" ||
			plan.Target.Fingerprint != "" || plan.Target.Generation != 0 || plan.Target.Revision != 0 ||
			plan.Parameters.Kind != ActionParametersDrainPlan || plan.TargetSet.Validate() != nil ||
			plan.TargetSet.Digest() != plan.Target.TargetSetDigest || plan.Parameters.PlanDigest != plan.Target.TargetSetDigest ||
			plan.Target.TargetCount != len(plan.TargetSet.Members()) || plan.Parameters.PlanTargetCount != plan.Target.TargetCount ||
			plan.Limits.MaximumItems != plan.drainPodCount()+1 || plan.Limits.MaximumItems < 1 ||
			plan.Limits.MaximumBytes != 0 || plan.Limits.MaximumLines != 0 || plan.validateDrainMembers() != nil {
			return ErrInvalidRemediation
		}
	default:
		return ErrInvalidRemediation
	}
	return nil
}

func (plan RemediationActionPlan) drainPodCount() int {
	count := 0
	for _, member := range plan.TargetSet.Members() {
		if member.Role == RemediationMemberDrainPod {
			count++
		}
	}
	return count
}

func (plan RemediationActionPlan) validateDrainMembers() error {
	pods := 0
	for _, member := range plan.TargetSet.Members() {
		switch member.Role {
		case RemediationMemberDrainPod:
			pods++
		case RemediationMemberDrainPDB:
		default:
			return ErrInvalidRemediation
		}
	}
	if pods < 1 {
		return ErrInvalidRemediation
	}
	return nil
}

func (plan RemediationActionPlan) Risk() RiskClass {
	if plan.Validate() != nil {
		return RiskDeny
	}
	switch plan.Operation {
	case ActionOperationScaleWorkload:
		if plan.Parameters.ReplicaTarget == plan.Parameters.ReplicaCurrent+1 {
			return RiskReview
		}
		return RiskCritical
	case ActionOperationRollbackDeployment, ActionOperationDrainNode:
		return RiskCritical
	case ActionOperationDeleteOwnedPod, ActionOperationCordonNode, ActionOperationUncordonNode:
		return RiskReview
	default:
		return RiskDeny
	}
}

func (plan RemediationActionPlan) VerificationPlanID() string {
	switch plan.Operation {
	case ActionOperationScaleWorkload:
		return ScaleVerificationPlanID
	case ActionOperationRollbackDeployment:
		return RollbackVerificationPlanID
	case ActionOperationDeleteOwnedPod:
		return PodDeleteVerificationPlanID
	case ActionOperationCordonNode:
		return CordonVerificationPlanID
	case ActionOperationUncordonNode:
		return UncordonVerificationPlanID
	case ActionOperationDrainNode:
		return DrainVerificationPlanID
	default:
		return ""
	}
}

func (plan RemediationActionPlan) riskSummary() string {
	switch plan.Operation {
	case ActionOperationScaleWorkload:
		if plan.Risk() == RiskReview {
			return "Scaling the exact workload up by one changes its desired replica count and resource use."
		}
		return "Scaling the exact workload to this replica count may reduce availability or materially change resource use."
	case ActionOperationRollbackDeployment:
		return "Rolling back the exact Deployment replaces its Pod template with one bound prior revision and may reduce availability."
	case ActionOperationDeleteOwnedPod:
		return "Deleting the exact controller-owned Pod with a non-zero grace period may temporarily reduce availability."
	case ActionOperationCordonNode:
		return "Cordoning the exact Node prevents new Pods from being scheduled on it."
	case ActionOperationUncordonNode:
		return "Uncordoning the exact Node allows new Pods to be scheduled on it."
	case ActionOperationDrainNode:
		return "Draining the exact Node cordons it and evicts only the complete approved Pod set, which may reduce availability."
	default:
		return ""
	}
}

// Intent creates the immutable common envelope input after deterministic
// permission routing has supplied the current profile and generation.
func (plan RemediationActionPlan) Intent(profile PermissionProfile) (ActionIntent, error) {
	if plan.Validate() != nil || !profile.Valid() {
		return ActionIntent{}, ErrInvalidRemediation
	}
	intent := ActionIntent{
		Operation: plan.Operation, OperationSchemaVersion: plan.Operation.SchemaVersion(),
		PolicyVersion: ActionPolicyVersion, PermissionProfile: profile, Risk: plan.Risk(),
		Effect: CapabilityEffectClusterMutation, PolicyGeneration: plan.PolicyGeneration,
		Scope: plan.Scope.Snapshot(), NamespaceAccess: plan.Scope.NamespaceAccess,
		Target: plan.Target, Parameters: plan.Parameters,
		DataCategories: ActionDataResourceMetadata | ActionDataProjectedStatus,
		AllowedSinks:   ActionSinkTerminal | ActionSinkKubernetesAPI,
		NetworkEffects: ActionNetworkKubernetesAPI, Limits: plan.Limits,
		VerificationPlanID: plan.VerificationPlanID(), ReasonSummary: plan.ReasonSummary,
		RiskSummary: plan.riskSummary(),
	}
	if intent.Validate() != nil || ValidateRemediationIntent(intent) != nil || !plan.MatchesIntent(intent) {
		return ActionIntent{}, ErrInvalidRemediation
	}
	return intent, nil
}

func ValidateRemediationIntent(intent ActionIntent) error {
	if intent.Validate() != nil || intent.Effect != CapabilityEffectClusterMutation || intent.Stdin || intent.TTY || intent.Shell ||
		intent.DataCategories != ActionDataResourceMetadata|ActionDataProjectedStatus ||
		intent.AllowedSinks != ActionSinkTerminal|ActionSinkKubernetesAPI || intent.NetworkEffects != ActionNetworkKubernetesAPI ||
		intent.NetworkDestinationHash != "" {
		return ErrInvalidRemediation
	}
	plan := RemediationActionPlan{
		RunID: "00000000-0000-7000-8000-000000000001", SessionID: "00000000-0000-7000-8000-000000000002",
		Scope:            ClusterScope{Context: intent.Scope.Context, Namespace: intent.Scope.Namespace, NamespaceAccess: intent.NamespaceAccess, Generation: intent.Scope.Generation, ActivatedAt: time.UnixMilli(1).UTC()},
		PolicyGeneration: intent.PolicyGeneration, Operation: intent.Operation, Target: intent.Target,
		Parameters: intent.Parameters, ReasonSummary: intent.ReasonSummary, Limits: intent.Limits, PreparedAt: time.UnixMilli(1).UTC(),
	}
	if intent.Target.TargetCount > 0 {
		// An intent deliberately carries only the target-set digest. Full set
		// validation is performed against the retained project-owned plan.
		switch intent.Operation {
		case ActionOperationRollbackDeployment:
			if intent.Target.TargetCount != 1 || intent.Parameters.Kind != ActionParametersRevision {
				return ErrInvalidRemediation
			}
		case ActionOperationDeleteOwnedPod:
			if intent.Target.TargetCount != 1 || intent.Parameters.Kind != ActionParametersPodDelete {
				return ErrInvalidRemediation
			}
		case ActionOperationDrainNode:
			if intent.Parameters.Kind != ActionParametersDrainPlan || intent.Parameters.PlanDigest != intent.Target.TargetSetDigest ||
				intent.Parameters.PlanTargetCount != intent.Target.TargetCount {
				return ErrInvalidRemediation
			}
		default:
			return ErrInvalidRemediation
		}
		return validateRemediationIntentFields(intent)
	}
	_ = plan
	return validateRemediationIntentFields(intent)
}

func validateRemediationIntentFields(intent ActionIntent) error {
	switch intent.Operation {
	case ActionOperationScaleWorkload:
		if intent.Target.Resource.APIVersion != "apps/v1" ||
			intent.Target.Resource.Kind != "Deployment" && intent.Target.Resource.Kind != "StatefulSet" ||
			intent.Target.Resource.Namespace != intent.Scope.Namespace || intent.Target.Subresource != "scale" ||
			!validSHA256Hex(intent.Target.Fingerprint) || intent.Target.Generation < 1 || intent.Target.TargetCount != 0 ||
			intent.Parameters.Kind != ActionParametersReplicaTarget || intent.VerificationPlanID != ScaleVerificationPlanID {
			return ErrInvalidRemediation
		}
		if intent.Parameters.ReplicaCurrent == intent.Parameters.ReplicaTarget {
			return ErrInvalidRemediation
		}
		wantRisk := RiskCritical
		if intent.Parameters.ReplicaTarget == intent.Parameters.ReplicaCurrent+1 {
			wantRisk = RiskReview
		}
		if intent.Risk != wantRisk {
			return ErrInvalidRemediation
		}
	case ActionOperationRollbackDeployment:
		if intent.Target.Resource.APIVersion != "apps/v1" || intent.Target.Resource.Kind != "Deployment" ||
			intent.Target.Resource.Namespace != intent.Scope.Namespace || intent.Target.Generation < 1 || intent.Target.Revision < 1 ||
			intent.Target.TargetCount != 1 || intent.Parameters.Kind != ActionParametersRevision || intent.Risk != RiskCritical ||
			intent.Target.Revision <= intent.Parameters.Revision || intent.VerificationPlanID != RollbackVerificationPlanID {
			return ErrInvalidRemediation
		}
	case ActionOperationDeleteOwnedPod:
		if intent.Target.Resource.APIVersion != "v1" || intent.Target.Resource.Kind != "Pod" ||
			intent.Target.Resource.Namespace != intent.Scope.Namespace || intent.Target.TargetCount != 1 ||
			intent.Parameters.Kind != ActionParametersPodDelete || intent.Parameters.GracePeriodSeconds != RemediationGracePeriodSeconds ||
			intent.Risk != RiskReview || intent.VerificationPlanID != PodDeleteVerificationPlanID {
			return ErrInvalidRemediation
		}
	case ActionOperationCordonNode, ActionOperationUncordonNode:
		want := intent.Operation == ActionOperationCordonNode
		verification := UncordonVerificationPlanID
		if want {
			verification = CordonVerificationPlanID
		}
		if intent.Target.Resource.APIVersion != "v1" || intent.Target.Resource.Kind != "Node" || intent.Target.Resource.Namespace != "" ||
			intent.Parameters.Kind != ActionParametersNodeScheduling || intent.Parameters.Unschedulable != want || intent.Risk != RiskReview ||
			intent.Target.TargetCount != 0 || intent.VerificationPlanID != verification {
			return ErrInvalidRemediation
		}
	case ActionOperationDrainNode:
		if intent.NamespaceAccess != NamespaceAccessAll ||
			intent.Target.Resource.APIVersion != "v1" || intent.Target.Resource.Kind != "Node" || intent.Target.Resource.Namespace != "" ||
			intent.Target.TargetCount < 1 || intent.Parameters.Kind != ActionParametersDrainPlan ||
			intent.Parameters.GracePeriodSeconds != RemediationGracePeriodSeconds || intent.Parameters.PlanDigest != intent.Target.TargetSetDigest ||
			intent.Parameters.PlanTargetCount != intent.Target.TargetCount || intent.Risk != RiskCritical || intent.VerificationPlanID != DrainVerificationPlanID {
			return ErrInvalidRemediation
		}
	default:
		return ErrInvalidRemediation
	}
	return nil
}

func (plan RemediationActionPlan) MatchesIntent(intent ActionIntent) bool {
	if plan.Validate() != nil || ValidateRemediationIntent(intent) != nil {
		return false
	}
	return intent.Operation == plan.Operation && intent.PolicyGeneration == plan.PolicyGeneration &&
		intent.Scope == plan.Scope.Snapshot() && intent.NamespaceAccess == plan.Scope.NamespaceAccess &&
		intent.Target == plan.Target && intent.Parameters == plan.Parameters && intent.Limits == plan.Limits &&
		intent.ReasonSummary == plan.ReasonSummary && intent.Risk == plan.Risk() &&
		intent.RiskSummary == plan.riskSummary() && intent.VerificationPlanID == plan.VerificationPlanID()
}

// RemediationAttemptState separates definite rejection from a request that
// may have reached Kubernetes. It never implies verification.
type RemediationAttemptState string

const (
	RemediationNotAttempted RemediationAttemptState = "not_attempted"
	RemediationAccepted     RemediationAttemptState = "request_accepted"
	RemediationFailed       RemediationAttemptState = "request_failed"
	RemediationUnknown      RemediationAttemptState = "request_outcome_unknown"
)

type RemediationAttempt struct {
	State          RemediationAttemptState
	AcceptedCount  int
	AttemptedCount int
	ErrorClass     SafeErrorClass
}

func (attempt RemediationAttempt) Validate(maximum int) error {
	if maximum < 1 || attempt.AcceptedCount < 0 || attempt.AttemptedCount < 0 ||
		attempt.AcceptedCount > attempt.AttemptedCount || attempt.AttemptedCount > maximum {
		return ErrInvalidRemediation
	}
	switch attempt.State {
	case RemediationNotAttempted:
		if attempt.AcceptedCount != 0 || attempt.AttemptedCount != 0 || attempt.ErrorClass != "" {
			return ErrInvalidRemediation
		}
	case RemediationAccepted:
		if attempt.AttemptedCount < 1 || attempt.AcceptedCount != attempt.AttemptedCount || attempt.ErrorClass != "" {
			return ErrInvalidRemediation
		}
	case RemediationFailed, RemediationUnknown:
		if attempt.AttemptedCount < 1 || !attempt.ErrorClass.Valid() {
			return ErrInvalidRemediation
		}
	default:
		return ErrInvalidRemediation
	}
	return nil
}

type RemediationVerificationState string

const (
	RemediationVerificationNotAttempted RemediationVerificationState = "verification_not_attempted"
	RemediationVerified                 RemediationVerificationState = "verified"
	RemediationVerificationFailed       RemediationVerificationState = "verification_failed"
	RemediationVerificationUnavailable  RemediationVerificationState = "verification_unavailable"
	RemediationVerificationTimedOut     RemediationVerificationState = "verification_timed_out"
)

type RemediationResult struct {
	Operation    ActionOperation
	Attempt      RemediationAttempt
	Verification RemediationVerificationState
	VerifiedAt   time.Time
	ErrorClass   SafeErrorClass
}

func (result RemediationResult) Validate(maximum int) error {
	if !result.Operation.Valid() || result.Attempt.Validate(maximum) != nil {
		return ErrInvalidRemediation
	}
	switch result.Verification {
	case RemediationVerificationNotAttempted:
		if !result.VerifiedAt.IsZero() || result.ErrorClass != "" {
			return ErrInvalidRemediation
		}
	case RemediationVerified:
		if result.Attempt.State != RemediationAccepted || result.VerifiedAt.IsZero() || result.VerifiedAt.Location() != time.UTC || result.ErrorClass != "" {
			return ErrInvalidRemediation
		}
	case RemediationVerificationFailed, RemediationVerificationUnavailable, RemediationVerificationTimedOut:
		if result.Attempt.State != RemediationAccepted || !result.VerifiedAt.IsZero() || !result.ErrorClass.Valid() {
			return ErrInvalidRemediation
		}
	default:
		return ErrInvalidRemediation
	}
	return nil
}
