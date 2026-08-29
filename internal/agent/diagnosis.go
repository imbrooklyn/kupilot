package agent

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var (
	// ErrInvalidEvidenceRegistry reports mismatched, duplicate, late, or invalid
	// runtime Evidence without echoing its content.
	ErrInvalidEvidenceRegistry = errors.New("runtime Evidence registry data is invalid")
	// ErrInvalidDiagnosisDraft reports a draft that cannot become a bounded
	// four-part Diagnosis after provenance enforcement.
	ErrInvalidDiagnosisDraft = errors.New("Diagnosis draft data is invalid")
)

// EvidenceRegistry accepts Evidence only through a matching BoundToolCall and
// ToolResult. Model and user drafts have no registration operation.
type EvidenceRegistry struct {
	mu sync.Mutex

	runID             domain.AgentRunID
	scope             domain.ClusterScope
	invocations       map[domain.ToolInvocationID]struct{}
	items             map[domain.EvidenceID]domain.Evidence
	resourceSummaries map[domain.EvidenceID]domain.ResourceSummary
	truncated         bool
	sealed            bool
	revision          uint64
}

// NewEvidenceRegistry creates an empty runtime-owned registry for one run and
// one immutable scope.
func NewEvidenceRegistry(runID domain.AgentRunID, scope domain.ClusterScope) (*EvidenceRegistry, error) {
	if !runID.Valid() || scope.Validate() != nil {
		return nil, ErrInvalidEvidenceRegistry
	}
	return &EvidenceRegistry{
		runID:             runID,
		scope:             scope,
		invocations:       make(map[domain.ToolInvocationID]struct{}),
		items:             make(map[domain.EvidenceID]domain.Evidence),
		resourceSummaries: make(map[domain.EvidenceID]domain.ResourceSummary),
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
	for index, evidence := range result.Evidence {
		registry.items[evidence.ID] = cloneEvidence(evidence)
		if len(result.ResourceSummaries) > 0 {
			registry.resourceSummaries[evidence.ID] = cloneResourceSummary(result.ResourceSummaries[index])
		}
	}
	registry.invocations[result.InvocationID] = struct{}{}
	if result.Truncation.Truncated {
		registry.truncated = true
	}
	if len(result.Evidence) > 0 || result.Truncation.Truncated {
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
	items             map[domain.EvidenceID]domain.Evidence
	resourceSummaries map[domain.EvidenceID]domain.ResourceSummary
	truncated         bool
	revision          uint64
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
	resourceSummaries := make(map[domain.EvidenceID]domain.ResourceSummary, len(registry.resourceSummaries))
	for id, summary := range registry.resourceSummaries {
		resourceSummaries[id] = cloneResourceSummary(summary)
	}
	return evidenceSnapshot{
		items: items, resourceSummaries: resourceSummaries,
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

func cloneResourceSummary(summary domain.ResourceSummary) domain.ResourceSummary {
	cloned := summary
	cloned.Owners = append([]domain.ResourceOwner(nil), summary.Owners...)
	return cloned
}

// DiagnosisDraft is the model-decoded, untrusted four-part content. Runtime
// injects identity, scope, observation times, warnings, and rendered Markdown.
type DiagnosisDraft struct {
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
	warningExecutionRejected    = "An execution claim was rejected; recommendations are not executed in v0.1."
	maxDiagnosisDraftTextBytes  = 16 * 1024
)

// ValidateDiagnosis rejects unsupported fact entries, removes unsupported
// hypothesis references, forces recommendations to unexecuted, derives the
// Evidence window, and renders only the validated four collections.
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
	actions := make([]domain.RecommendedAction, len(draft.RecommendedActions))
	for index, action := range draft.RecommendedActions {
		actions[index] = action
		actions[index].Prerequisites = append([]string(nil), action.Prerequisites...)
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
	diagnosis.AnswerMarkdown = renderDiagnosisMarkdown(diagnosis, snapshot.items, snapshot.resourceSummaries)
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
		ConfirmedFacts:     make([]domain.ConfirmedFact, len(draft.ConfirmedFacts)),
		Hypotheses:         make([]domain.Hypothesis, len(draft.Hypotheses)),
		MissingInformation: make([]domain.MissingInformation, len(draft.MissingInformation)),
		RecommendedActions: make([]domain.RecommendedAction, len(draft.RecommendedActions)),
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
			Action: actionText, Risk: risk, Prerequisites: prerequisites, Executed: action.Executed,
		}
	}
	return result, nil
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

func renderDiagnosisMarkdown(
	diagnosis domain.Diagnosis,
	evidenceByID map[domain.EvidenceID]domain.Evidence,
	resourceSummaries map[domain.EvidenceID]domain.ResourceSummary,
) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Scope: %s / %s\n", diagnosis.Scope.Context, diagnosis.Scope.Namespace)
	if diagnosis.ObservedFrom == nil || diagnosis.ObservedTo == nil {
		builder.WriteString("Observed: unavailable\n\n")
	} else if diagnosis.ObservedFrom.Equal(*diagnosis.ObservedTo) {
		fmt.Fprintf(&builder, "Observed: %s\n\n", diagnosis.ObservedFrom.Format(time.RFC3339Nano))
	} else {
		fmt.Fprintf(&builder, "Observed: %s to %s\n\n", diagnosis.ObservedFrom.Format(time.RFC3339Nano), diagnosis.ObservedTo.Format(time.RFC3339Nano))
	}
	builder.WriteString("## Confirmed facts\n\n")
	if len(diagnosis.ConfirmedFacts) == 0 {
		builder.WriteString("None.\n")
	} else if rows, ok := diagnosisResourceStatusRows(diagnosis.ConfirmedFacts, evidenceByID, resourceSummaries); ok {
		builder.WriteString(renderDiagnosisResourceStatusTable(rows))
	} else {
		for _, fact := range diagnosis.ConfirmedFacts {
			fmt.Fprintf(&builder, "- %s\n", markdownConfirmedFactText(fact.Statement, len(fact.EvidenceIDs)))
		}
	}
	builder.WriteString("\n## Hypotheses\n\n")
	if len(diagnosis.Hypotheses) == 0 {
		builder.WriteString("None.\n")
	}
	for _, hypothesis := range diagnosis.Hypotheses {
		fmt.Fprintf(&builder, "- %s\n  Confidence: %s\n  Check: %s\n",
			markdownBulletText(hypothesis.Statement), hypothesis.Confidence, markdownBulletText(hypothesis.Falsifier))
	}
	builder.WriteString("\n## Missing information\n\n")
	if len(diagnosis.MissingInformation) == 0 {
		builder.WriteString("None.\n")
	}
	for _, missing := range diagnosis.MissingInformation {
		fmt.Fprintf(&builder, "- %s: %s\n  Impact: %s\n",
			missingInformationLabel(missing.Kind), markdownBulletText(missing.Detail), markdownBulletText(missing.Impact))
	}
	builder.WriteString("\n## Recommended actions\n\n")
	if len(diagnosis.RecommendedActions) == 0 {
		builder.WriteString("None.\n")
	}
	for _, action := range diagnosis.RecommendedActions {
		fmt.Fprintf(&builder, "- %s\n  Risk: %s\n", markdownBulletText(action.Action), markdownBulletText(action.Risk))
		if len(action.Prerequisites) > 0 {
			builder.WriteString("  Before acting: ")
			for index, prerequisite := range action.Prerequisites {
				if index > 0 {
					builder.WriteString("; ")
				}
				builder.WriteString(markdownBulletText(prerequisite))
			}
			builder.WriteByte('\n')
		}
		builder.WriteString("  Status: Not executed.\n")
	}
	return builder.String()
}

func missingInformationLabel(kind domain.MissingInformationKind) string {
	switch kind {
	case domain.MissingInformationAbsent:
		return "Unavailable"
	case domain.MissingInformationForbidden:
		return "Not permitted"
	case domain.MissingInformationUnsupported:
		return "Unsupported"
	case domain.MissingInformationStale:
		return "Outdated"
	case domain.MissingInformationConflicting:
		return "Conflicting"
	case domain.MissingInformationTruncated:
		return "Partial"
	case domain.MissingInformationSensitiveOutputBlocked:
		return "Sensitive value withheld"
	default:
		return "Unavailable"
	}
}

func markdownBulletText(value string) string {
	return strings.ReplaceAll(value, "\n", "\n    ")
}

const minBulkFactEvidenceCount = 4

func markdownConfirmedFactText(value string, evidenceCount int) string {
	if evidenceCount < minBulkFactEvidenceCount {
		return markdownBulletText(value)
	}
	for _, separator := range []string{";", string(rune(0xff1b))} {
		parts := strings.Split(value, separator)
		if len(parts) == 1 {
			continue
		}
		for index := 1; index < len(parts); index++ {
			parts[index] = strings.TrimLeft(parts[index], " \n")
		}
		value = strings.Join(parts, separator+"\n")
	}
	return markdownBulletText(value)
}

type diagnosisResourceStatusRow struct {
	summary domain.ResourceSummary
}

func diagnosisResourceStatusRows(
	facts []domain.ConfirmedFact,
	evidenceByID map[domain.EvidenceID]domain.Evidence,
	resourceSummaries map[domain.EvidenceID]domain.ResourceSummary,
) ([]diagnosisResourceStatusRow, bool) {
	if len(facts) == 0 || len(evidenceByID) == 0 || len(resourceSummaries) == 0 {
		return nil, false
	}
	rows := make([]diagnosisResourceStatusRow, 0, len(facts))
	seen := make(map[domain.EvidenceID]struct{}, len(facts))
	resourceKind := ""
	for _, fact := range facts {
		if len(fact.EvidenceIDs) != 1 {
			return nil, false
		}
		id := fact.EvidenceIDs[0]
		if _, duplicate := seen[id]; duplicate {
			return nil, false
		}
		evidence, exists := evidenceByID[id]
		if !exists || evidence.Category != domain.EvidenceCategoryResourceStatus {
			return nil, false
		}
		summary, exists := resourceSummaries[id]
		if !exists || summary.Validate() != nil || summary.Reference != evidence.Resource {
			return nil, false
		}
		if resourceKind == "" {
			resourceKind = summary.Reference.Kind
		} else if summary.Reference.Kind != resourceKind {
			return nil, false
		}
		seen[id] = struct{}{}
		rows = append(rows, diagnosisResourceStatusRow{
			summary: cloneResourceSummary(summary),
		})
	}
	sort.Slice(rows, func(left, right int) bool {
		return rows[left].summary.Reference.Name < rows[right].summary.Reference.Name
	})
	return rows, true
}

func renderDiagnosisResourceStatusTable(rows []diagnosisResourceStatusRow) string {
	kind := domain.ResourceKind(rows[0].summary.Reference.Kind)
	headers := []string{"NAME", "STATUS"}
	values := make([][]string, 0, len(rows))
	for _, row := range rows {
		name := row.summary.Reference.Name
		status := row.summary.Status
		switch kind {
		case domain.ResourceKindPod:
			headers = []string{"NAME", "READY", "STATUS"}
			values = append(values, []string{name, countRatio(status.Ready, status.Desired), podDisplayStatus(status)})
		case domain.ResourceKindDeployment:
			headers = []string{"NAME", "READY", "AVAILABLE", "REASON"}
			values = append(values, []string{name, countRatio(status.Ready, status.Desired), countRatio(status.Available, status.Desired), tableValue(status.Reason)})
		case domain.ResourceKindReplicaSet:
			headers = []string{"NAME", "DESIRED", "READY", "AVAILABLE", "REASON"}
			values = append(values, []string{name, optionalCount(status.Desired), optionalCount(status.Ready), optionalCount(status.Available), tableValue(status.Reason)})
		case domain.ResourceKindJob:
			headers = []string{"NAME", "STATUS", "COMPLETIONS", "FAILED", "REASON"}
			values = append(values, []string{name, tableValue(status.Phase), countRatio(status.Succeeded, status.Desired), optionalCount(status.Failed), tableValue(status.Reason)})
		case domain.ResourceKindService:
			headers = []string{"NAME", "TYPE"}
			values = append(values, []string{name, tableValue(status.ServiceType)})
		default:
			values = append(values, []string{name, tableValue(status.Phase)})
		}
	}
	return renderDiagnosisColumns(headers, values)
}

func podDisplayStatus(status domain.ResourceStatus) string {
	if status.Reason != "" {
		return tableValue(status.Reason)
	}
	return tableValue(status.Phase)
}

func countRatio(value, desired domain.OptionalCount) string {
	if value.Present && desired.Present {
		return fmt.Sprintf("%d/%d", value.Value, desired.Value)
	}
	return optionalCount(value)
}

func optionalCount(value domain.OptionalCount) string {
	if !value.Present {
		return "-"
	}
	return fmt.Sprintf("%d", value.Value)
}

func tableValue(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "-"
	}
	return value
}

func renderDiagnosisColumns(headers []string, rows [][]string) string {
	widths := make([]int, len(headers))
	for index, header := range headers {
		widths[index] = len(header)
	}
	for _, row := range rows {
		for index, value := range row {
			widths[index] = max(widths[index], len(value))
		}
	}
	var builder strings.Builder
	writeDiagnosisColumns(&builder, headers, widths)
	for _, row := range rows {
		writeDiagnosisColumns(&builder, row, widths)
	}
	return builder.String()
}

func writeDiagnosisColumns(builder *strings.Builder, values []string, widths []int) {
	for index, value := range values {
		if index > 0 {
			builder.WriteString("  ")
		}
		if index == len(values)-1 {
			builder.WriteString(value)
			continue
		}
		fmt.Fprintf(builder, "%-*s", widths[index], value)
	}
	builder.WriteByte('\n')
}
