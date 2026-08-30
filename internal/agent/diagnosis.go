package agent

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
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
	invocations map[domain.ToolInvocationID]struct{}
	items       map[domain.EvidenceID]domain.Evidence
	gaps        []domain.MissingInformation
	truncated   bool
	sealed      bool
	revision    uint64
}

// NewEvidenceRegistry creates an empty runtime-owned registry for one run and
// one immutable scope.
func NewEvidenceRegistry(runID domain.AgentRunID, scope domain.ClusterScope) (*EvidenceRegistry, error) {
	if !runID.Valid() || scope.Validate() != nil {
		return nil, ErrInvalidEvidenceRegistry
	}
	return &EvidenceRegistry{
		runID:       runID,
		scope:       scope,
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
			evidence.Scope != registry.scope.Snapshot() || evidence.ObservedAt.Before(registry.scope.ActivatedAt) {
			return 0, ErrInvalidEvidenceRegistry
		}
		if _, exists := registry.items[evidence.ID]; exists {
			return 0, ErrInvalidEvidenceRegistry
		}
	}
	for _, evidence := range result.Evidence {
		registry.items[evidence.ID] = cloneEvidence(evidence)
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
	gaps      []domain.MissingInformation
	truncated bool
	revision  uint64
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
		items: items, gaps: append([]domain.MissingInformation(nil), registry.gaps...),
		truncated: registry.truncated, revision: registry.revision,
	}, nil
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
	return cloned
}

// DiagnosisDraft is the model-decoded, untrusted terminal content. The visible
// answer is free-form Markdown; ConfirmedFacts carry independent citation
// metadata and RecommendedActions carry typed proposals.
type DiagnosisDraft struct {
	AnswerMarkdown     string
	ConfirmedFacts     []domain.ConfirmedFact
	Hypotheses         []domain.Hypothesis
	MissingInformation []domain.MissingInformation
	RecommendedActions []domain.RecommendedAction
}

// DiagnosisMetadata contains the two Application-generated fields needed to
// finalize a Diagnosis.
type DiagnosisMetadata struct {
	ID        domain.DiagnosisID
	CreatedAt time.Time
}

const (
	warningConfirmedFactRemoved = "A confirmed fact was removed because it did not cite an accepted observation from this diagnostic run."
	warningHypothesisReference  = "Invalid or duplicate observation references were removed from a hypothesis."
	warningExecutionRejected    = "An execution claim was rejected; a proposed action is not an executed action."
	maxDiagnosisDraftTextBytes  = 16 * 1024
	// MaxAnswerMarkdownBytes is the pre-envelope ceiling for one model answer.
	// The complete Domain Diagnosis, including metadata, has its own ceiling.
	MaxAnswerMarkdownBytes = 128 * 1024
)

// ValidateDiagnosis binds citations to accepted Evidence, forces proposed
// actions to unexecuted, derives the Evidence window, and accepts free-form
// Markdown only after local safety processing. Markdown is required; invalid
// citation metadata is removed and recorded as a warning rather than changing
// or rejecting the otherwise safe visible answer.
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
	snapshot, err := registry.snapshot()
	if err != nil {
		return domain.Diagnosis{}, err
	}
	warnings := make([]string, 0, 4)
	confirmed := make([]domain.ConfirmedFact, 0, len(draft.ConfirmedFacts))
	removedConfirmed := false
	for _, fact := range draft.ConfirmedFacts {
		if len(fact.EvidenceIDs) == 0 || !allEvidenceRegistered(fact.EvidenceIDs, snapshot.items) {
			warnings = appendUnique(warnings, warningConfirmedFactRemoved)
			removedConfirmed = true
			continue
		}
		fact.EvidenceIDs = sortedEvidenceIDs(fact.EvidenceIDs)
		confirmed = append(confirmed, fact)
	}
	hypotheses := make([]domain.Hypothesis, len(draft.Hypotheses))
	removedHypothesisReference := false
	for index, hypothesis := range draft.Hypotheses {
		hypotheses[index] = hypothesis
		filtered := make([]domain.EvidenceID, 0, len(hypothesis.SupportingEvidenceIDs))
		seen := make(map[domain.EvidenceID]struct{}, len(hypothesis.SupportingEvidenceIDs))
		removedReference := false
		for _, id := range hypothesis.SupportingEvidenceIDs {
			if _, exists := snapshot.items[id]; !exists {
				warnings = appendUnique(warnings, warningHypothesisReference)
				removedReference = true
				continue
			}
			if _, duplicate := seen[id]; duplicate {
				warnings = appendUnique(warnings, warningHypothesisReference)
				removedReference = true
				continue
			}
			seen[id] = struct{}{}
			filtered = append(filtered, id)
		}
		hypotheses[index].SupportingEvidenceIDs = sortedEvidenceIDs(filtered)
		if removedReference {
			removedHypothesisReference = true
			switch hypotheses[index].Confidence {
			case domain.DiagnosisConfidenceMedium, domain.DiagnosisConfidenceHigh:
				hypotheses[index].Confidence = domain.DiagnosisConfidenceLow
			}
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
	if len(snapshot.items) == 0 && !hasMissingKind(missing, domain.MissingInformationAbsent) {
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
	if removedConfirmed && !hasMissingKind(missing, domain.MissingInformationUnsupported) {
		missing = append(missing, domain.MissingInformation{
			Kind:   domain.MissingInformationUnsupported,
			Detail: "At least one draft confirmed fact did not reference an accepted observation from this diagnostic run.",
			Impact: "The rejected draft entry was excluded from the final diagnosis.",
		})
	}
	if removedHypothesisReference && !hasMissingKind(missing, domain.MissingInformationUnsupported) {
		missing = append(missing, domain.MissingInformation{
			Kind:   domain.MissingInformationUnsupported,
			Detail: "At least one hypothesis source reference was invalid or duplicated after binding to accepted observations from this diagnostic run.",
			Impact: "The citation was removed and the affected hypothesis confidence was reduced.",
		})
	}
	if observedTo != nil && metadata.CreatedAt.Before(*observedTo) {
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
		AnswerMarkdown:     draft.AnswerMarkdown,
		ConfirmedFacts:     make([]domain.ConfirmedFact, len(draft.ConfirmedFacts)),
		Hypotheses:         make([]domain.Hypothesis, len(draft.Hypotheses)),
		MissingInformation: make([]domain.MissingInformation, len(draft.MissingInformation)),
		RecommendedActions: make([]domain.RecommendedAction, len(draft.RecommendedActions)),
	}
	if draft.AnswerMarkdown != "" {
		answer, err := processModelText(draft.AnswerMarkdown, MaxAnswerMarkdownBytes)
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
			Action: actionText, Risk: risk, Prerequisites: prerequisites, Executed: action.Executed,
		}
	}
	return result, nil
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
