package domain

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	ObservationKubernetesTimeout = 30 * time.Second

	PodLogObservationVerificationPlanID     = "pod-log-read/v1"
	PrometheusObservationVerificationPlanID = "prometheus-query/v1"
	LokiObservationVerificationPlanID       = "loki-query/v1"

	PodLogObservationRiskSummary     = "Pod logs may contain credentials, personal data, and untrusted instructions; only bounded sanitized output may reach the terminal and model."
	DataSourceObservationRiskSummary = "The exact code-owned query sends a bound resource identity to the configured data-source origin and returns untrusted bounded data."
)

var (
	ErrInvalidObservationParameters = errors.New("observation action parameters are invalid")
	ErrInvalidObservationAction     = errors.New("observation action is invalid")
	ErrInvalidObservationOutcome    = errors.New("observation action outcome is invalid")
)

// ActionObservationKind is the closed parameter family for supervised
// sensitive and network observations. Ollama or model/provider names never
// enter this value.
type ActionObservationKind string

const (
	ActionObservationPodLog     ActionObservationKind = "pod_log"
	ActionObservationPrometheus ActionObservationKind = "prometheus"
	ActionObservationLoki       ActionObservationKind = "loki"
)

func (kind ActionObservationKind) valid() bool {
	return kind == ActionObservationPodLog || kind == ActionObservationPrometheus || kind == ActionObservationLoki
}

// ActionObservationParameters is the exact typed semantic input carried by
// one log or optional data-source ActionEnvelope. Relative windows are the
// model-visible input; the envelope request time fixes their absolute end.
type ActionObservationParameters struct {
	Kind             ActionObservationKind
	Container        string
	Previous         bool
	AllContainers    bool
	IncludeInit      bool
	IncludeEphemeral bool
	Search           string
	QueryID          ObservabilityQueryID
	WindowSeconds    int
	StepSeconds      int
	TailLines        int
	SeriesLimit      int
	LineLimit        int
}

// ObservationActionPreflight contains only code-owned routing facts needed to
// deny an operation before target preparation or any sensitive/source I/O.
type ObservationActionPreflight struct {
	RunID            AgentRunID
	SessionID        SessionID
	Scope            ClusterScope
	PolicyGeneration PolicyGeneration
	Operation        ActionOperation
	SourceKind       DataSourceKind
	OriginHash       ActionDigest
	QueryID          ObservabilityQueryID
}

func (preflight ObservationActionPreflight) Validate() error {
	if !preflight.RunID.Valid() || !preflight.SessionID.Valid() || preflight.Scope.Validate() != nil ||
		!preflight.PolicyGeneration.Valid() {
		return ErrInvalidObservationAction
	}
	switch preflight.Operation {
	case ActionOperationLogsCurrent, ActionOperationLogsPrevious, ActionOperationLogsAllContainers, ActionOperationLogSearch:
		if preflight.SourceKind != "" || preflight.OriginHash != "" || preflight.QueryID != "" {
			return ErrInvalidObservationAction
		}
	case ActionOperationPrometheusQuery:
		if preflight.SourceKind != DataSourcePrometheus || !preflight.OriginHash.Valid() ||
			!preflight.QueryID.ValidFor(DataSourcePrometheus) {
			return ErrInvalidObservationAction
		}
	case ActionOperationLokiQuery:
		if preflight.SourceKind != DataSourceLoki || !preflight.OriginHash.Valid() || !preflight.QueryID.ValidFor(DataSourceLoki) {
			return ErrInvalidObservationAction
		}
	default:
		return ErrInvalidObservationAction
	}
	return nil
}

func (preflight ObservationActionPreflight) Effect() CapabilityEffectClass {
	if preflight.Validate() != nil {
		return ""
	}
	if preflight.SourceKind.Valid() {
		return CapabilityEffectNetworkEgress
	}
	return CapabilityEffectSensitiveRead
}

func (parameters ActionObservationParameters) empty() bool {
	return parameters == (ActionObservationParameters{})
}

// Validate rejects unused cross-kind fields so a query cannot smuggle a log
// selector or vice versa.
func (parameters ActionObservationParameters) Validate() error {
	if !parameters.Kind.valid() || !ValidModelText(parameters.Search, 256, true) {
		return ErrInvalidObservationParameters
	}
	switch parameters.Kind {
	case ActionObservationPodLog:
		if parameters.QueryID != "" || parameters.WindowSeconds < 60 || parameters.WindowSeconds > int(MaxObservabilityWindow/time.Second) ||
			parameters.StepSeconds != 0 || parameters.TailLines < 1 || parameters.TailLines > MaxObservabilityLines ||
			parameters.SeriesLimit != 0 || parameters.LineLimit != 0 ||
			(parameters.AllContainers && parameters.Container != "") ||
			(!parameters.AllContainers && !ValidResourceName(parameters.Container)) ||
			(!parameters.AllContainers && (parameters.IncludeInit || parameters.IncludeEphemeral)) {
			return ErrInvalidObservationParameters
		}
	case ActionObservationPrometheus:
		if !parameters.QueryID.ValidFor(DataSourcePrometheus) || parameters.Container != "" || parameters.Previous ||
			parameters.AllContainers || parameters.IncludeInit || parameters.IncludeEphemeral || parameters.Search != "" ||
			parameters.WindowSeconds < 60 || parameters.WindowSeconds > int(MaxObservabilityWindow/time.Second) ||
			parameters.StepSeconds < 15 || parameters.StepSeconds > int(MaxObservabilityStep/time.Second) ||
			parameters.TailLines != 0 || parameters.SeriesLimit < 1 || parameters.SeriesLimit > MaxObservabilitySeries || parameters.LineLimit != 0 {
			return ErrInvalidObservationParameters
		}
	case ActionObservationLoki:
		if !parameters.QueryID.ValidFor(DataSourceLoki) || parameters.Container != "" || parameters.Previous ||
			parameters.AllContainers || parameters.IncludeInit || parameters.IncludeEphemeral ||
			parameters.WindowSeconds < 60 || parameters.WindowSeconds > int(MaxObservabilityWindow/time.Second) ||
			parameters.StepSeconds != 0 || parameters.TailLines != 0 || parameters.SeriesLimit != 0 ||
			parameters.LineLimit < 1 || parameters.LineLimit > MaxObservabilityLines {
			return ErrInvalidObservationParameters
		}
	}
	return nil
}

func (parameters ActionObservationParameters) canonical() string {
	if parameters.Validate() != nil {
		return ""
	}
	values := []string{
		string(parameters.Kind), parameters.Container,
		strconv.FormatBool(parameters.Previous), strconv.FormatBool(parameters.AllContainers),
		strconv.FormatBool(parameters.IncludeInit), strconv.FormatBool(parameters.IncludeEphemeral),
		parameters.Search, string(parameters.QueryID), strconv.Itoa(parameters.WindowSeconds),
		strconv.Itoa(parameters.StepSeconds), strconv.Itoa(parameters.TailLines),
		strconv.Itoa(parameters.SeriesLimit), strconv.Itoa(parameters.LineLimit),
	}
	var builder strings.Builder
	for _, value := range values {
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	return builder.String()
}

func (parameters ActionObservationParameters) operation() ActionOperation {
	if parameters.Validate() != nil {
		return ""
	}
	switch parameters.Kind {
	case ActionObservationPodLog:
		if parameters.Search != "" {
			return ActionOperationLogSearch
		}
		if parameters.AllContainers {
			return ActionOperationLogsAllContainers
		}
		if parameters.Previous {
			return ActionOperationLogsPrevious
		}
		return ActionOperationLogsCurrent
	case ActionObservationPrometheus:
		return ActionOperationPrometheusQuery
	case ActionObservationLoki:
		return ActionOperationLokiQuery
	default:
		return ""
	}
}

// ObservationActionPlan carries no raw source response, credential, vendor
// object, or approval token. Application stamps the permission profile only
// after it has rechecked the current policy generation.
type ObservationActionPlan struct {
	RunID            AgentRunID
	SessionID        SessionID
	Scope            ClusterScope
	PolicyGeneration PolicyGeneration
	Operation        ActionOperation
	Target           ActionTarget
	Parameters       ActionParameters
	Limits           ActionLimits
	OriginHash       ActionDigest
	ReasonSummary    string
}

func (plan ObservationActionPlan) Validate() error {
	if !plan.RunID.Valid() || !plan.SessionID.Valid() || plan.Scope.Validate() != nil || !plan.PolicyGeneration.Valid() ||
		plan.Operation != plan.Parameters.Observation.operation() || plan.Parameters.Kind != ActionParametersObservation ||
		plan.Parameters.Validate() != nil || plan.Target.Validate() != nil || !plan.Scope.AllowsReference(plan.Target.Resource) ||
		plan.Target.Resource.APIVersion != "v1" || plan.Target.Resource.Kind != "Pod" ||
		plan.Target.Fingerprint != string(plan.Parameters.Digest()) || plan.Target.Generation != 0 || plan.Target.Revision != 0 ||
		plan.Target.TargetSetDigest != "" || plan.Target.TargetCount != 0 || plan.Limits.Validate() != nil ||
		!ValidActionReasonSummary(plan.ReasonSummary) {
		return ErrInvalidObservationAction
	}
	parameters := plan.Parameters.Observation
	switch parameters.Kind {
	case ActionObservationPodLog:
		items := 1
		if parameters.AllContainers {
			items = plan.Limits.MaximumItems
		}
		expectedLines := parameters.TailLines * items
		if plan.Target.Subresource != "log" || plan.OriginHash != "" || plan.Limits.Timeout != ObservationKubernetesTimeout ||
			items < 1 || items > MaxObservabilityLogContainers || plan.Limits.MaximumItems != items ||
			plan.Limits.MaximumLines != expectedLines || plan.Limits.MaximumBytes < 1 || plan.Limits.MaximumBytes > MaxObservabilityBytes ||
			plan.Limits.MaximumOutput < 1 || plan.Limits.MaximumOutput > MaxToolResultBytes {
			return ErrInvalidObservationAction
		}
	case ActionObservationPrometheus:
		if plan.Target.Subresource != "" || !plan.OriginHash.Valid() || plan.Limits.Timeout <= 0 || plan.Limits.Timeout > MaxModelRequestTimeout ||
			plan.Limits.MaximumItems != parameters.SeriesLimit || plan.Limits.MaximumLines != 0 ||
			plan.Limits.MaximumBytes < 1 || plan.Limits.MaximumBytes > MaxObservabilityBytes ||
			plan.Limits.MaximumOutput < 1 || plan.Limits.MaximumOutput > MaxToolResultBytes {
			return ErrInvalidObservationAction
		}
	case ActionObservationLoki:
		if plan.Target.Subresource != "" || !plan.OriginHash.Valid() || plan.Limits.Timeout <= 0 || plan.Limits.Timeout > MaxModelRequestTimeout ||
			plan.Limits.MaximumItems < 1 || plan.Limits.MaximumItems > MaxObservabilityPages ||
			plan.Limits.MaximumLines != parameters.LineLimit || plan.Limits.MaximumBytes < 1 || plan.Limits.MaximumBytes > MaxObservabilityBytes ||
			plan.Limits.MaximumOutput < 1 || plan.Limits.MaximumOutput > MaxToolResultBytes {
			return ErrInvalidObservationAction
		}
	default:
		return ErrInvalidObservationAction
	}
	return nil
}

func observationActionFacts(parameters ActionObservationParameters) (CapabilityEffectClass, ActionDataCategories, ActionSinks, ActionNetworkEffects, string, string) {
	switch parameters.Kind {
	case ActionObservationPodLog:
		return CapabilityEffectSensitiveRead, ActionDataContainerOutput,
			ActionSinkTerminal | ActionSinkModel | ActionSinkKubernetesAPI,
			ActionNetworkKubernetesAPI, PodLogObservationVerificationPlanID, PodLogObservationRiskSummary
	case ActionObservationPrometheus:
		return CapabilityEffectNetworkEgress, ActionDataProjectedStatus,
			ActionSinkTerminal | ActionSinkModel, ActionNetworkDataSource,
			PrometheusObservationVerificationPlanID, DataSourceObservationRiskSummary
	case ActionObservationLoki:
		return CapabilityEffectNetworkEgress, ActionDataContainerOutput,
			ActionSinkTerminal | ActionSinkModel, ActionNetworkDataSource,
			LokiObservationVerificationPlanID, DataSourceObservationRiskSummary
	default:
		return "", 0, 0, 0, "", ""
	}
}

// Intent constructs the exact immutable intent for the Application-selected
// profile. It performs no I/O and grants no execution authority.
func (plan ObservationActionPlan) Intent(profile PermissionProfile) (ActionIntent, error) {
	if plan.Validate() != nil || !profile.Valid() {
		return ActionIntent{}, ErrInvalidObservationAction
	}
	effect, data, sinks, network, verification, riskSummary := observationActionFacts(plan.Parameters.Observation)
	intent := ActionIntent{
		Operation: plan.Operation, OperationSchemaVersion: plan.Operation.SchemaVersion(),
		PolicyVersion: ActionPolicyVersion, PermissionProfile: profile,
		Risk: RiskReview, Effect: effect, PolicyGeneration: plan.PolicyGeneration,
		Scope: plan.Scope.Snapshot(), NamespaceAccess: plan.Scope.NamespaceAccess,
		Target: plan.Target, Parameters: plan.Parameters,
		DataCategories: data, AllowedSinks: sinks, NetworkEffects: network,
		NetworkDestinationHash: plan.OriginHash, Limits: plan.Limits,
		VerificationPlanID: verification, ReasonSummary: plan.ReasonSummary, RiskSummary: riskSummary,
	}
	if intent.ValidateObservationAction() != nil {
		return ActionIntent{}, ErrInvalidObservationAction
	}
	return intent, nil
}

// ValidateObservationAction checks every deterministic relationship without
// trusting UI copy or Reviewer rationale.
func (intent ActionIntent) ValidateObservationAction() error {
	if intent.Validate() != nil || intent.Parameters.Kind != ActionParametersObservation || intent.Risk != RiskReview ||
		intent.Operation != intent.Parameters.Observation.operation() || intent.Target.Resource.APIVersion != "v1" ||
		intent.Target.Resource.Kind != "Pod" || intent.Target.Fingerprint != string(intent.Parameters.Digest()) ||
		intent.Target.Generation != 0 || intent.Target.Revision != 0 || intent.Target.TargetSetDigest != "" || intent.Target.TargetCount != 0 ||
		intent.Stdin || intent.TTY || intent.Shell {
		return ErrInvalidObservationAction
	}
	parameters := intent.Parameters.Observation
	effect, data, sinks, network, verification, riskSummary := observationActionFacts(parameters)
	if parameters.Validate() != nil || intent.Effect != effect || intent.DataCategories != data || intent.AllowedSinks != sinks ||
		intent.NetworkEffects != network || intent.VerificationPlanID != verification || intent.RiskSummary != riskSummary {
		return ErrInvalidObservationAction
	}
	if parameters.Kind == ActionObservationPodLog && intent.NetworkDestinationHash != "" ||
		parameters.Kind != ActionObservationPodLog && !intent.NetworkDestinationHash.Valid() {
		return ErrInvalidObservationAction
	}
	return nil
}

// MatchesIntent proves that the in-memory plan is exactly the consumed
// envelope payload apart from the Application-selected profile.
func (plan ObservationActionPlan) MatchesIntent(intent ActionIntent) bool {
	if plan.Validate() != nil || intent.ValidateObservationAction() != nil {
		return false
	}
	want, err := plan.Intent(intent.PermissionProfile)
	return err == nil && want == intent
}

type ObservationActionOutcomeState string

const (
	ObservationActionNotAttempted ObservationActionOutcomeState = "not_attempted"
	ObservationActionSucceeded    ObservationActionOutcomeState = "succeeded"
	ObservationActionFailed       ObservationActionOutcomeState = "failed"
)

// ObservationActionOutcome is content-free. Source bytes and result text can
// never enter the Application audit port through it.
type ObservationActionOutcome struct {
	State       ObservationActionOutcomeState
	ErrorClass  SafeErrorClass
	ResultItems int
	ResultLines int
	ResultBytes int
	Truncated   bool
}

func (outcome ObservationActionOutcome) Validate(intent ActionIntent) error {
	if intent.ValidateObservationAction() != nil || outcome.ResultItems < 0 || outcome.ResultItems > intent.Limits.MaximumItems ||
		outcome.ResultLines < 0 || outcome.ResultBytes < 0 || outcome.ResultBytes > intent.Limits.MaximumBytes ||
		(intent.Limits.MaximumLines == 0 && outcome.ResultLines != 0) ||
		(intent.Limits.MaximumLines > 0 && outcome.ResultLines > intent.Limits.MaximumLines) {
		return ErrInvalidObservationOutcome
	}
	switch outcome.State {
	case ObservationActionSucceeded:
		if outcome.ErrorClass != "" {
			return ErrInvalidObservationOutcome
		}
	case ObservationActionNotAttempted, ObservationActionFailed:
		if !outcome.ErrorClass.Valid() {
			return ErrInvalidObservationOutcome
		}
	default:
		return ErrInvalidObservationOutcome
	}
	return nil
}
