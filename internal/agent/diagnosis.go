package agent

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

var (
	// ErrInvalidEvidenceRegistry reports mismatched, duplicate, late, or invalid
	// runtime Evidence without echoing its content.
	ErrInvalidEvidenceRegistry = errors.New("runtime Evidence registry data is invalid")
	// ErrInvalidDiagnosisDraft reports a draft that cannot become a bounded,
	// provenance-checked answer.
	ErrInvalidDiagnosisDraft = errors.New("Diagnosis draft data is invalid")
)

// EvidenceRegistry accepts Evidence only through a matching BoundToolCall and
// ToolResult. Model and user drafts have no registration operation.
type EvidenceRegistry struct {
	mu sync.Mutex

	runID       domain.AgentRunID
	scope       domain.ClusterScope
	policy      domain.PolicyGeneration
	invocations map[domain.ToolInvocationID]struct{}
	items       map[domain.EvidenceID]domain.Evidence
	order       []domain.EvidenceID
	sources     []evidenceSourceCheck
	gaps        []domain.MissingInformation
	truncated   bool
	sealed      bool
	revision    uint64
}

type evidenceSourceCheck struct {
	sourceHash  string
	subjectHash string
	state       domain.SourceCoverageState
	evidenceIDs []domain.EvidenceID
	observedAt  time.Time
}

// NewEvidenceRegistry creates an empty runtime-owned registry for one run and
// one immutable scope.
func NewEvidenceRegistry(runID domain.AgentRunID, scope domain.ClusterScope, policies ...domain.PolicyGeneration) (*EvidenceRegistry, error) {
	if !runID.Valid() || scope.Validate() != nil {
		return nil, ErrInvalidEvidenceRegistry
	}
	policy := domain.PolicyGeneration(0)
	if len(policies) > 1 || len(policies) == 1 && !policies[0].Valid() {
		return nil, ErrInvalidEvidenceRegistry
	}
	if len(policies) == 1 {
		policy = policies[0]
	}
	return &EvidenceRegistry{
		runID:       runID,
		scope:       scope,
		policy:      policy,
		invocations: make(map[domain.ToolInvocationID]struct{}),
		items:       make(map[domain.EvidenceID]domain.Evidence),
	}, nil
}

// AcceptToolResult atomically validates all candidate Evidence before adding
// any item. A rejected result leaves the registry unchanged.
func (registry *EvidenceRegistry) AcceptToolResult(call BoundToolCall, result domain.ToolResult) (int, error) {
	if registry == nil || call.Validate() != nil || result.Validate() != nil {
		return 0, ErrInvalidEvidenceRegistry
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.sealed || call.RunID() != registry.runID || call.Scope() != registry.scope ||
		result.InvocationID != call.InvocationID() || result.Name != call.Name() || result.Version != call.Version() ||
		result.Scope != registry.scope.Snapshot() || result.ObservedAt.Before(registry.scope.ActivatedAt) {
		return 0, ErrInvalidEvidenceRegistry
	}
	if _, exists := registry.invocations[result.InvocationID]; exists {
		return 0, ErrInvalidEvidenceRegistry
	}
	for _, evidence := range result.Evidence {
		if evidence.RunID != registry.runID || evidence.InvocationID != call.InvocationID() ||
			evidence.Scope != registry.scope.Snapshot() || evidence.ObservedAt.Before(registry.scope.ActivatedAt) ||
			registry.policy.Valid() && evidence.PolicyGeneration != registry.policy {
			return 0, ErrInvalidEvidenceRegistry
		}
		if _, exists := registry.items[evidence.ID]; exists {
			return 0, ErrInvalidEvidenceRegistry
		}
	}
	registry.sources = append(registry.sources, sourceCheckForResult(call, result))
	for _, evidence := range result.Evidence {
		registry.items[evidence.ID] = cloneEvidence(evidence)
		registry.order = append(registry.order, evidence.ID)
	}
	registry.invocations[result.InvocationID] = struct{}{}
	gapAdded := false
	if gap, ok := missingInformationForToolResult(result); ok && !hasMissingKind(registry.gaps, gap.Kind) {
		registry.gaps = append(registry.gaps, gap)
		gapAdded = true
	}
	if result.Truncation.Truncated {
		registry.truncated = true
	}
	if len(result.Evidence) > 0 || result.Truncation.Truncated || gapAdded {
		registry.revision++
	}
	return len(result.Evidence), nil
}

// Len returns the number of accepted current-run Evidence items.
func (registry *EvidenceRegistry) Len() int {
	if registry == nil {
		return 0
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.items)
}

type evidenceSnapshot struct {
	items     map[domain.EvidenceID]domain.Evidence
	order     []domain.EvidenceID
	gaps      []domain.MissingInformation
	truncated bool
	revision  uint64
	sources   []evidenceSourceCheck
}

func (registry *EvidenceRegistry) snapshot() (evidenceSnapshot, error) {
	if registry == nil {
		return evidenceSnapshot{}, ErrInvalidEvidenceRegistry
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.sealed {
		return evidenceSnapshot{}, ErrInvalidEvidenceRegistry
	}
	items := make(map[domain.EvidenceID]domain.Evidence, len(registry.items))
	for id, evidence := range registry.items {
		items[id] = cloneEvidence(evidence)
	}
	return evidenceSnapshot{
		items: items, order: append([]domain.EvidenceID(nil), registry.order...),
		gaps:      append([]domain.MissingInformation(nil), registry.gaps...),
		truncated: registry.truncated, revision: registry.revision,
		sources: cloneEvidenceSourceChecks(registry.sources),
	}, nil
}

func sourceCheckForResult(call BoundToolCall, result domain.ToolResult) evidenceSourceCheck {
	identity := call.Identity()
	sourceHash := domain.SHA256Hex(string(identity.Name) + "\x00" + identity.Version + "\x00" + identity.ArgumentsDigest)
	check := evidenceSourceCheck{
		sourceHash: sourceHash, subjectHash: domain.SHA256Hex(identity.ArgumentsDigest), observedAt: result.ObservedAt,
		evidenceIDs: make([]domain.EvidenceID, len(result.Evidence)),
	}
	for index, evidence := range result.Evidence {
		check.evidenceIDs[index] = evidence.ID
	}
	switch {
	case result.Status == domain.ToolResultStatusDenied:
		check.state = domain.SourceDenied
	case result.Error != nil && result.Error.Class == domain.SafeErrorClassTimeout:
		check.state = domain.SourceTimedOut
	case result.Error != nil && result.Error.Class == domain.SafeErrorClassStaleScope:
		check.state = domain.SourceStale
	case result.Status == domain.ToolResultStatusError:
		check.state = domain.SourceUnavailable
	case result.Truncation.Truncated:
		check.state = domain.SourceTruncated
	case result.Status == domain.ToolResultStatusPartial || result.Truncation.ReturnedCount > len(result.Evidence):
		check.state = domain.SourcePartial
	case len(result.Evidence) == 0:
		check.state = domain.SourceCheckedAbsent
	default:
		check.state = domain.SourceCheckedPresent
	}
	return check
}

func cloneEvidenceSourceChecks(checks []evidenceSourceCheck) []evidenceSourceCheck {
	result := make([]evidenceSourceCheck, len(checks))
	for index, check := range checks {
		result[index] = check
		result[index].evidenceIDs = append([]domain.EvidenceID(nil), check.evidenceIDs...)
	}
	return result
}

func (registry *EvidenceRegistry) seal(revision uint64) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.sealed || registry.revision != revision {
		return ErrInvalidEvidenceRegistry
	}
	registry.sealed = true
	return nil
}

func cloneEvidence(evidence domain.Evidence) domain.Evidence {
	cloned := evidence
	if evidence.SourcePath != nil {
		value := *evidence.SourcePath
		cloned.SourcePath = &value
	}
	if evidence.Severity != nil {
		value := *evidence.Severity
		cloned.Severity = &value
	}
	if evidence.ObservedFrom != nil {
		value := *evidence.ObservedFrom
		cloned.ObservedFrom = &value
	}
	if evidence.ObservedThrough != nil {
		value := *evidence.ObservedThrough
		cloned.ObservedThrough = &value
	}
	return cloned
}

// DiagnosisDraft is the model-decoded, untrusted terminal content. The visible
// answer is free-form Markdown; ConfirmedFacts carry independent citation
// metadata and RecommendedActions carry typed proposals.
type DiagnosisDraft struct {
	AnswerMarkdown        string
	ResponseSchemaVersion int
	ConfirmedFacts        []domain.ConfirmedFact
	Hypotheses            []domain.Hypothesis
	MissingInformation    []domain.MissingInformation
	RecommendedActions    []domain.RecommendedAction
	ClaimCoverage         []ClaimCoverageDraft
	SuggestedStopReason   domain.RunTerminalReason
	Clarification         *domain.ClarificationRequest
	Plan                  *domain.Plan
}

// ClaimCoverageDraft is untrusted model metadata before runtime provenance is
// attached. Run, scope, and policy fields are always supplied locally.
type ClaimCoverageDraft struct {
	Sequence    int
	Kind        domain.ClaimKind
	Text        string
	TextHash    string
	EvidenceIDs []domain.EvidenceID
	State       domain.ClaimCoverageState
}

// DiagnosisMetadata contains the two Application-generated fields needed to
// finalize a Diagnosis.
type DiagnosisMetadata struct {
	ID                      domain.DiagnosisID
	CreatedAt               time.Time
	PolicyGeneration        domain.PolicyGeneration
	AuthoritativeStopReason domain.RunTerminalReason
}

const (
	warningExecutionRejected   = "An execution claim was rejected; a proposed action is not an executed action."
	maxDiagnosisDraftTextBytes = 16 * 1024
	// MaxAnswerMarkdownBytes is the pre-envelope ceiling for one model answer.
	// The complete Domain Diagnosis, including metadata, has its own ceiling.
	MaxAnswerMarkdownBytes = 128 * 1024
)

// ValidateDiagnosis binds citations to accepted Evidence, forces proposed
// actions to unexecuted, derives the Evidence window, and accepts free-form
// Markdown only after local safety processing. Markdown is required and
// invalid claim or Evidence coverage rejects the terminal answer.
func ValidateDiagnosis(draft DiagnosisDraft, metadata DiagnosisMetadata, registry *EvidenceRegistry) (domain.Diagnosis, error) {
	if !metadata.ID.Valid() || metadata.CreatedAt.IsZero() || metadata.CreatedAt.Location() != time.UTC ||
		metadata.CreatedAt.Nanosecond()%int(time.Millisecond) != 0 {
		return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
	}
	var err error
	draft, err = sanitizeDiagnosisDraft(draft)
	if err != nil {
		return domain.Diagnosis{}, err
	}
	if draft.AnswerMarkdown == "" {
		return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
	}
	if draft.Plan != nil {
		rendered, renderErr := RenderPlanMarkdown(*draft.Plan)
		if renderErr != nil || rendered != draft.AnswerMarkdown || len(draft.RecommendedActions) != 0 {
			return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
		}
	}
	if draft.Clarification != nil {
		rendered, renderErr := RenderClarificationMarkdown(*draft.Clarification)
		if renderErr != nil || rendered != draft.AnswerMarkdown || draft.SuggestedStopReason != domain.RunTerminalNeedsUserInput ||
			len(draft.ConfirmedFacts) != 0 || len(draft.Hypotheses) != 0 || len(draft.MissingInformation) != 0 || len(draft.RecommendedActions) != 0 ||
			len(draft.ClaimCoverage) != 0 {
			return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
		}
	}
	snapshot, err := registry.snapshot()
	if err != nil {
		return domain.Diagnosis{}, err
	}
	warnings := make([]string, 0, 2)
	confirmed := make([]domain.ConfirmedFact, 0, len(draft.ConfirmedFacts))
	for _, fact := range draft.ConfirmedFacts {
		if len(fact.EvidenceIDs) == 0 || !allEvidenceRegistered(fact.EvidenceIDs, snapshot.items) {
			return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
		}
		if hasDuplicateEvidenceIDs(fact.EvidenceIDs) {
			return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
		}
		confirmed = append(confirmed, fact)
	}
	hypotheses := make([]domain.Hypothesis, len(draft.Hypotheses))
	for index, hypothesis := range draft.Hypotheses {
		hypotheses[index] = hypothesis
		for _, id := range hypothesis.SupportingEvidenceIDs {
			if _, exists := snapshot.items[id]; !exists {
				return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
			}
		}
		if hasDuplicateEvidenceIDs(hypothesis.SupportingEvidenceIDs) {
			return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
		}
	}
	missing := append([]domain.MissingInformation(nil), draft.MissingInformation...)
	for _, gap := range snapshot.gaps {
		if !hasMissingKind(missing, gap.Kind) {
			missing = append(missing, gap)
		}
	}
	actions := make([]domain.RecommendedAction, len(draft.RecommendedActions))
	for index, action := range draft.RecommendedActions {
		actions[index] = action
		actions[index].Prerequisites = append([]string(nil), action.Prerequisites...)
		actions[index].Target = cloneResourceRef(action.Target)
		if action.Executed {
			actions[index].Executed = false
			warnings = appendUnique(warnings, warningExecutionRejected)
		}
	}

	observedFrom, observedTo, detailState, truncated := evidenceWindow(snapshot.items)
	if snapshot.truncated {
		truncated = true
		detailState = domain.EvidenceDetailPartial
	}
	if draft.Clarification == nil && len(snapshot.items) == 0 && len(snapshot.sources) == 0 && !hasMissingKind(missing, domain.MissingInformationAbsent) {
		missing = append(missing, domain.MissingInformation{
			Kind:   domain.MissingInformationAbsent,
			Detail: "No accepted cluster observation was collected for this diagnostic run.",
			Impact: "No cluster observation can be presented as a confirmed fact.",
		})
	}
	if truncated && !hasMissingKind(missing, domain.MissingInformationTruncated) {
		missing = append(missing, domain.MissingInformation{
			Kind:   domain.MissingInformationTruncated,
			Detail: "At least one accepted observation was truncated by a fixed limit.",
			Impact: "The diagnosis cannot account for content outside the admitted result.",
		})
	}
	coverage, err := bindClaimCoverage(draft.ClaimCoverage, metadata.PolicyGeneration, registry, snapshot)
	if err != nil {
		return domain.Diagnosis{}, err
	}
	if observedTo != nil && metadata.CreatedAt.Before(*observedTo) {
		return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
	}

	responseSchemaVersion := draft.ResponseSchemaVersion
	if responseSchemaVersion == 0 {
		responseSchemaVersion = 1
	}
	manifest := buildAnswerCompleteness(coverage, missing, snapshot, draft.SuggestedStopReason, draft.Clarification != nil,
		responseSchemaVersion, metadata.AuthoritativeStopReason)
	if manifest.Validate() != nil {
		return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
	}
	diagnosis := domain.Diagnosis{
		ID:                   metadata.ID,
		RunID:                registry.runID,
		Scope:                registry.scope.Snapshot(),
		ConfirmedFacts:       confirmed,
		Hypotheses:           hypotheses,
		MissingInformation:   missing,
		RecommendedActions:   actions,
		ValidationWarnings:   warnings,
		ObservedFrom:         observedFrom,
		ObservedTo:           observedTo,
		CreatedAt:            metadata.CreatedAt,
		EvidenceDetailsState: detailState,
		ClaimCoverage:        coverage,
		Completeness:         manifest,
		Clarification:        cloneClarification(draft.Clarification),
		Plan:                 clonePlan(draft.Plan),
	}
	diagnosis.AnswerMarkdown = draft.AnswerMarkdown
	if diagnosis.Validate() != nil {
		return domain.Diagnosis{}, ErrInvalidDiagnosisDraft
	}
	if err := registry.seal(snapshot.revision); err != nil {
		return domain.Diagnosis{}, err
	}
	return diagnosis, nil
}

func sanitizeDiagnosisDraft(draft DiagnosisDraft) (DiagnosisDraft, error) {
	result := DiagnosisDraft{
		AnswerMarkdown:        draft.AnswerMarkdown,
		ResponseSchemaVersion: draft.ResponseSchemaVersion,
		ConfirmedFacts:        make([]domain.ConfirmedFact, len(draft.ConfirmedFacts)),
		Hypotheses:            make([]domain.Hypothesis, len(draft.Hypotheses)),
		MissingInformation:    make([]domain.MissingInformation, len(draft.MissingInformation)),
		RecommendedActions:    make([]domain.RecommendedAction, len(draft.RecommendedActions)),
		ClaimCoverage:         make([]ClaimCoverageDraft, len(draft.ClaimCoverage)),
		SuggestedStopReason:   draft.SuggestedStopReason,
	}
	if draft.Plan != nil {
		result.Plan = clonePlan(draft.Plan)
		if result.Plan == nil || result.Plan.Validate() != nil {
			return DiagnosisDraft{}, ErrInvalidDiagnosisDraft
		}
	}
	if draft.Clarification != nil {
		result.Clarification = cloneClarification(draft.Clarification)
		if result.Clarification == nil || result.Clarification.Validate() != nil {
			return DiagnosisDraft{}, ErrInvalidDiagnosisDraft
		}
		for questionIndex := range result.Clarification.Questions {
			prompt, err := sanitizeDiagnosisText(result.Clarification.Questions[questionIndex].Prompt)
			if err != nil {
				return DiagnosisDraft{}, err
			}
			result.Clarification.Questions[questionIndex].Prompt = prompt
			for choiceIndex := range result.Clarification.Questions[questionIndex].Choices {
				label, err := sanitizeDiagnosisText(result.Clarification.Questions[questionIndex].Choices[choiceIndex].Label)
				if err != nil {
					return DiagnosisDraft{}, err
				}
				result.Clarification.Questions[questionIndex].Choices[choiceIndex].Label = label
			}
		}
		if result.Clarification.Validate() != nil {
			return DiagnosisDraft{}, ErrInvalidDiagnosisDraft
		}
	}
	if draft.AnswerMarkdown != "" {
		answer, err := processModelMarkdown(draft.AnswerMarkdown, MaxAnswerMarkdownBytes)
		if err != nil || answer == "" {
			if errors.Is(err, ErrSensitiveModelTextBlocked) {
				return DiagnosisDraft{}, err
			}
			return DiagnosisDraft{}, ErrInvalidDiagnosisDraft
		}
		result.AnswerMarkdown = answer
	}
	for index, fact := range draft.ConfirmedFacts {
		statement, err := sanitizeDiagnosisText(fact.Statement)
		if err != nil {
			return DiagnosisDraft{}, err
		}
		result.ConfirmedFacts[index] = domain.ConfirmedFact{
			Statement: statement, EvidenceIDs: append([]domain.EvidenceID(nil), fact.EvidenceIDs...),
		}
	}
	for index, hypothesis := range draft.Hypotheses {
		statement, err := sanitizeDiagnosisText(hypothesis.Statement)
		if err != nil {
			return DiagnosisDraft{}, err
		}
		falsifier, err := sanitizeDiagnosisText(hypothesis.Falsifier)
		if err != nil {
			return DiagnosisDraft{}, err
		}
		result.Hypotheses[index] = domain.Hypothesis{
			Statement:             statement,
			SupportingEvidenceIDs: append([]domain.EvidenceID(nil), hypothesis.SupportingEvidenceIDs...),
			Confidence:            hypothesis.Confidence,
			Falsifier:             falsifier,
		}
	}
	for index, missing := range draft.MissingInformation {
		detail, err := sanitizeDiagnosisText(missing.Detail)
		if err != nil {
			return DiagnosisDraft{}, err
		}
		impact, err := sanitizeDiagnosisText(missing.Impact)
		if err != nil {
			return DiagnosisDraft{}, err
		}
		result.MissingInformation[index] = domain.MissingInformation{
			Kind: missing.Kind, Detail: detail, Impact: impact,
		}
	}
	for index, action := range draft.RecommendedActions {
		actionText, err := sanitizeDiagnosisText(action.Action)
		if err != nil {
			return DiagnosisDraft{}, err
		}
		risk, err := sanitizeDiagnosisText(action.Risk)
		if err != nil {
			return DiagnosisDraft{}, err
		}
		prerequisites := make([]string, len(action.Prerequisites))
		for prerequisiteIndex, prerequisite := range action.Prerequisites {
			prerequisites[prerequisiteIndex], err = sanitizeDiagnosisText(prerequisite)
			if err != nil {
				return DiagnosisDraft{}, err
			}
		}
		result.RecommendedActions[index] = domain.RecommendedAction{
			Operation: action.Operation, Target: cloneResourceRef(action.Target),
			Parameters: cloneProposedParameters(action.Parameters),
			Action:     actionText, Risk: risk, Prerequisites: prerequisites, Executed: action.Executed,
		}
	}
	for index, coverage := range draft.ClaimCoverage {
		if coverage.TextHash != domain.SHA256Hex(coverage.Text) {
			return DiagnosisDraft{}, ErrInvalidDiagnosisDraft
		}
		text, err := sanitizeDiagnosisText(coverage.Text)
		if err != nil {
			return DiagnosisDraft{}, err
		}
		result.ClaimCoverage[index] = ClaimCoverageDraft{
			Sequence: coverage.Sequence, Kind: coverage.Kind, Text: text, TextHash: domain.SHA256Hex(text),
			EvidenceIDs: append([]domain.EvidenceID(nil), coverage.EvidenceIDs...), State: coverage.State,
		}
	}
	return result, nil
}

func bindClaimCoverage(
	drafts []ClaimCoverageDraft,
	policy domain.PolicyGeneration,
	registry *EvidenceRegistry,
	snapshot evidenceSnapshot,
) ([]domain.ClaimEvidenceCoverage, error) {
	if len(drafts) > 100 || len(drafts) > 0 && !policy.Valid() {
		return nil, ErrInvalidDiagnosisDraft
	}
	positions := make(map[domain.EvidenceID]int, len(snapshot.order))
	for index, id := range snapshot.order {
		positions[id] = index
	}
	seenClaims := make(map[string]struct{}, len(drafts))
	result := make([]domain.ClaimEvidenceCoverage, len(drafts))
	for index, draft := range drafts {
		if draft.Sequence != index+1 || draft.TextHash != domain.SHA256Hex(draft.Text) {
			return nil, ErrInvalidDiagnosisDraft
		}
		if _, duplicate := seenClaims[draft.TextHash]; duplicate {
			return nil, ErrInvalidDiagnosisDraft
		}
		seenClaims[draft.TextHash] = struct{}{}
		lastPosition := -1
		for _, id := range draft.EvidenceIDs {
			evidence, exists := snapshot.items[id]
			position, ordered := positions[id]
			if !exists || !ordered || evidence.RunID != registry.runID || evidence.Scope != registry.scope.Snapshot() ||
				evidence.PolicyGeneration != policy || position <= lastPosition {
				return nil, ErrInvalidDiagnosisDraft
			}
			lastPosition = position
		}
		result[index] = domain.ClaimEvidenceCoverage{
			Sequence: draft.Sequence, Kind: draft.Kind, Text: draft.Text, TextHash: draft.TextHash,
			EvidenceIDs: append([]domain.EvidenceID(nil), draft.EvidenceIDs...), RunID: registry.runID,
			Scope: registry.scope.Snapshot(), PolicyGeneration: policy, State: draft.State,
		}
	}
	return result, nil
}

func hasDuplicateEvidenceIDs(ids []domain.EvidenceID) bool {
	seen := make(map[domain.EvidenceID]struct{}, len(ids))
	for _, id := range ids {
		if _, duplicate := seen[id]; duplicate {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

func cloneProposedParameters(parameters *domain.ProposedActionParameters) *domain.ProposedActionParameters {
	if parameters == nil {
		return nil
	}
	copy := *parameters
	return &copy
}

func clonePlan(plan *domain.Plan) *domain.Plan {
	if plan == nil {
		return nil
	}
	copy := *plan
	copy.Steps = append([]domain.PlanStep(nil), plan.Steps...)
	copy.Limitations = append([]string(nil), plan.Limitations...)
	return &copy
}

func cloneClarification(request *domain.ClarificationRequest) *domain.ClarificationRequest {
	if request == nil {
		return nil
	}
	copy := *request
	copy.Questions = append([]domain.ClarificationQuestion(nil), request.Questions...)
	for index := range copy.Questions {
		copy.Questions[index].Choices = append([]domain.ClarificationChoiceValue(nil), request.Questions[index].Choices...)
	}
	return &copy
}

// RenderClarificationMarkdown creates the only visible prose for a typed
// clarification result. Model prose cannot simulate this interaction.
func RenderClarificationMarkdown(request domain.ClarificationRequest) (string, error) {
	if request.Validate() != nil {
		return "", ErrInvalidDiagnosisDraft
	}
	var builder strings.Builder
	builder.WriteString("More information is needed:\n")
	for _, question := range request.Questions {
		builder.WriteString("\n")
		builder.WriteString(strconv.Itoa(question.Sequence))
		builder.WriteString(". ")
		builder.WriteString(question.Prompt)
		for _, choice := range question.Choices {
			builder.WriteString("\n   - ")
			builder.WriteString(choice.ID)
			builder.WriteString(": ")
			builder.WriteString(choice.Label)
		}
	}
	value := builder.String()
	if !domain.ValidModelText(value, MaxAnswerMarkdownBytes, false) {
		return "", ErrInvalidDiagnosisDraft
	}
	return value, nil
}

func buildAnswerCompleteness(
	claims []domain.ClaimEvidenceCoverage,
	limitations []domain.MissingInformation,
	snapshot evidenceSnapshot,
	suggested domain.RunTerminalReason,
	clarification bool,
	responseSchemaVersion int,
	authoritative domain.RunTerminalReason,
) domain.AnswerCompletenessManifest {
	sources := make([]domain.AnswerSourceCoverage, 0, max(1, len(snapshot.sources)))
	for index, check := range snapshot.sources {
		from, through := check.observedAt, check.observedAt
		sources = append(sources, domain.AnswerSourceCoverage{
			Sequence: index + 1, SourceHash: check.sourceHash, SubjectHash: check.subjectHash,
			State: check.state, EvidenceIDs: append([]domain.EvidenceID(nil), check.evidenceIDs...),
			Freshness: sourceFreshness(check.state), Conflict: domain.EvidenceConflictNone,
			ObservedFrom: &from, ObservedThrough: &through,
		})
	}
	if len(sources) == 0 {
		sources = append(sources, domain.AnswerSourceCoverage{
			Sequence: 1, SourceHash: domain.SHA256Hex("not_checked"), SubjectHash: domain.SHA256Hex("not_checked"),
			State: domain.SourceNotChecked, Freshness: domain.EvidenceFreshnessUnknown, Conflict: domain.EvidenceConflictNone,
		})
	}
	applyTypedEvidenceConflicts(sources, snapshot.items)
	// SuggestedStopReason is parsed and bounded but never carries terminal
	// authority. Domain derives the reason solely from accepted typed coverage.
	_ = suggested
	basis := domain.RunTerminalReasonFromCoverage
	reason, _ := domain.DeriveAnswerTerminalReason(claims, limitations, sources, false)
	if clarification {
		basis = domain.RunTerminalReasonFromClarification
		reason, _ = domain.DeriveAnswerTerminalReason(claims, limitations, sources, true)
	} else if authoritative == domain.RunTerminalBudgetExhausted {
		basis = domain.RunTerminalReasonFromRuntimeBudget
		reason = authoritative
	} else if authoritative != "" {
		return domain.AnswerCompletenessManifest{}
	}
	return domain.AnswerCompletenessManifest{
		SchemaVersion: domain.AnswerCompletenessSchemaVersion, ResponseSchemaVersion: responseSchemaVersion,
		Claims:      append([]domain.ClaimEvidenceCoverage(nil), claims...),
		Limitations: append([]domain.MissingInformation(nil), limitations...),
		Sources:     sources, StopReason: reason, StopReasonBasis: basis,
	}
}

func applyTypedEvidenceConflicts(sources []domain.AnswerSourceCoverage, evidence map[domain.EvidenceID]domain.Evidence) {
	for left := range sources {
		for right := left + 1; right < len(sources); right++ {
			if sources[left].SourceHash != sources[right].SourceHash {
				continue
			}
			for _, leftID := range sources[left].EvidenceIDs {
				leftEvidence, leftOK := evidence[leftID]
				for _, rightID := range sources[right].EvidenceIDs {
					rightEvidence, rightOK := evidence[rightID]
					if !leftOK || !rightOK || evidenceSubjectHash(leftEvidence) != evidenceSubjectHash(rightEvidence) ||
						leftEvidence.Fingerprint == rightEvidence.Fingerprint {
						continue
					}
					switch {
					case leftEvidence.ObservedAt.Before(rightEvidence.ObservedAt):
						markSourceSuperseded(&sources[left], rightID)
					case rightEvidence.ObservedAt.Before(leftEvidence.ObservedAt):
						markSourceSuperseded(&sources[right], leftID)
					default:
						markSourceConflicting(&sources[left])
						markSourceConflicting(&sources[right])
					}
				}
			}
		}
	}
}

func markSourceSuperseded(source *domain.AnswerSourceCoverage, successor domain.EvidenceID) {
	if source.Conflict == domain.EvidenceConflictDetected {
		return
	}
	source.Conflict = domain.EvidenceConflictSuperseded
	for _, existing := range source.SupersededByEvidenceIDs {
		if existing == successor {
			return
		}
	}
	source.SupersededByEvidenceIDs = append(source.SupersededByEvidenceIDs, successor)
	sort.Slice(source.SupersededByEvidenceIDs, func(left, right int) bool {
		return source.SupersededByEvidenceIDs[left] < source.SupersededByEvidenceIDs[right]
	})
}

func markSourceConflicting(source *domain.AnswerSourceCoverage) {
	source.Conflict = domain.EvidenceConflictDetected
	source.State = domain.SourceConflicting
	source.SupersededByEvidenceIDs = nil
}

func evidenceSubjectHash(evidence domain.Evidence) string {
	sourcePath := ""
	if evidence.SourcePath != nil {
		sourcePath = *evidence.SourcePath
	}
	return domain.SHA256Hex(string(evidence.Category) + "\x00" + evidence.Resource.APIVersion + "\x00" +
		evidence.Resource.Kind + "\x00" + evidence.Resource.Namespace + "\x00" + evidence.Resource.Name + "\x00" +
		evidence.Resource.UID + "\x00" + sourcePath + "\x00" + evidence.Series)
}

func sourceFreshness(state domain.SourceCoverageState) domain.EvidenceFreshnessState {
	if state == domain.SourceStale {
		return domain.EvidenceStale
	}
	return domain.EvidenceFreshnessUnknown
}

func processModelMarkdown(value string, maximumBytes int) (string, error) {
	if maximumBytes < 1 || len(value) > maximumBytes {
		return "", errInvalidModelText
	}
	processed, err := security.NewRedactor().ProcessLines(value, maximumBytes)
	if errors.Is(err, security.ErrSensitiveOutputBlocked) {
		return "", ErrSensitiveModelTextBlocked
	}
	if err != nil || processed.Truncated {
		return "", errInvalidModelText
	}
	return processed.Value, nil
}

func cloneResourceRef(reference *domain.ResourceRef) *domain.ResourceRef {
	if reference == nil {
		return nil
	}
	cloned := *reference
	return &cloned
}

func sanitizeDiagnosisText(value string) (string, error) {
	processed, err := processModelText(value, maxDiagnosisDraftTextBytes)
	if err != nil {
		if errors.Is(err, ErrSensitiveModelTextBlocked) {
			return "", err
		}
		return "", ErrInvalidDiagnosisDraft
	}
	if processed == "" {
		return "", ErrInvalidDiagnosisDraft
	}
	return processed, nil
}

func allEvidenceRegistered(ids []domain.EvidenceID, evidence map[domain.EvidenceID]domain.Evidence) bool {
	seen := make(map[domain.EvidenceID]struct{}, len(ids))
	for _, id := range ids {
		if _, exists := evidence[id]; !exists {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func sortedEvidenceIDs(ids []domain.EvidenceID) []domain.EvidenceID {
	result := append([]domain.EvidenceID(nil), ids...)
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func hasMissingKind(items []domain.MissingInformation, kind domain.MissingInformationKind) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func evidenceWindow(items map[domain.EvidenceID]domain.Evidence) (*time.Time, *time.Time, domain.EvidenceDetailState, bool) {
	if len(items) == 0 {
		return nil, nil, domain.EvidenceDetailAvailable, false
	}
	var earliest, latest time.Time
	truncated := false
	for _, evidence := range items {
		if earliest.IsZero() || evidence.ObservedAt.Before(earliest) {
			earliest = evidence.ObservedAt
		}
		if latest.IsZero() || evidence.ObservedAt.After(latest) {
			latest = evidence.ObservedAt
		}
		truncated = truncated || evidence.Truncated
	}
	state := domain.EvidenceDetailAvailable
	if truncated {
		state = domain.EvidenceDetailPartial
	}
	return &earliest, &latest, state, truncated
}

func missingInformationForToolResult(result domain.ToolResult) (domain.MissingInformation, bool) {
	if result.Error == nil {
		return domain.MissingInformation{}, false
	}
	switch result.Error.Class {
	case domain.SafeErrorClassPermissionDenied, domain.SafeErrorClassAuthenticationFailed:
		return domain.MissingInformation{
			Kind:   domain.MissingInformationForbidden,
			Detail: "A requested Kubernetes observation was denied by the active credentials.",
			Impact: "The answer cannot rely on the denied source.",
		}, true
	case domain.SafeErrorClassUnsupported, domain.SafeErrorClassPolicyDenied, domain.SafeErrorClassInvalidInput:
		return domain.MissingInformation{
			Kind:   domain.MissingInformationUnsupported,
			Detail: "A requested observation was outside the admitted capability policy.",
			Impact: "The answer cannot infer the unavailable fact from another source.",
		}, true
	case domain.SafeErrorClassStaleScope:
		return domain.MissingInformation{
			Kind:   domain.MissingInformationStale,
			Detail: "A requested observation became stale after the active scope changed.",
			Impact: "The stale result cannot support a current cluster claim.",
		}, true
	case domain.SafeErrorClassConflict:
		return domain.MissingInformation{
			Kind:   domain.MissingInformationConflicting,
			Detail: "A requested observation conflicted with current cluster state.",
			Impact: "The conflicting result cannot support a definitive claim.",
		}, true
	case domain.SafeErrorClassSensitiveOutputBlocked:
		return domain.MissingInformation{
			Kind:   domain.MissingInformationSensitiveOutputBlocked,
			Detail: "A requested observation was withheld by the sensitive-output policy.",
			Impact: "The withheld content was not transferred to the model or answer.",
		}, true
	default:
		return domain.MissingInformation{
			Kind:   domain.MissingInformationAbsent,
			Detail: "A requested Kubernetes observation did not complete successfully.",
			Impact: "The answer cannot rely on the unavailable source.",
		}, true
	}
}
