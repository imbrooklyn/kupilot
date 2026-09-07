package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	// ExportSummarySchemaVersion identifies the only admitted local export projection.
	ExportSummarySchemaVersion = "kupilot.export-summary.v4"
	// MaxExportSummaryBytes is the complete post-redaction Markdown ceiling.
	MaxExportSummaryBytes = 2 * 1024 * 1024
	// MaxExportMessages bounds committed conversation records in one export.
	MaxExportMessages = 100
	// MaxExportDiagnoses bounds structured historic Diagnoses in one export.
	MaxExportDiagnoses = 10
	// MaxExportEvidence bounds referenced Evidence summaries in one export.
	MaxExportEvidence = 100

	maxExportTargetBytes         = 4096
	maxExportMessageBytes        = 4096
	maxExportAnswerMarkdownBytes = MaxAnswerMarkdownBytes
	maxExportDiagnosisTextBytes  = 2048
	maxExportEvidenceFactBytes   = 2048
	maxExportEvidencePathBytes   = 1024
	maxExportDiagnosisItems      = 5
	maxExportPrerequisites       = 5
	maxExportCitationsPerItem    = 10
	maxExportMarkdownOverhead    = 128 * 1024
	exportSummaryTimestampLayout = "2006-01-02T15:04:05.000Z"
)

var (
	// ErrInvalidExportSummary reports an invalid or non-allowlisted export projection.
	ErrInvalidExportSummary = errors.New("the Session export summary is invalid")
	// ErrExportSummaryLimit reports a complete export that cannot fit its fixed ceiling.
	ErrExportSummaryLimit = errors.New("the Session export summary exceeds its fixed limit")
	// ErrSessionExportUnavailable does not distinguish a missing, archived, deleted, or ineligible Session.
	ErrSessionExportUnavailable = errors.New("the Session is unavailable for export")
)

// ExportSummaryIntent binds one explicit current-Session target and one
// privacy-review confirmation to the fixed export schema.
type ExportSummaryIntent struct {
	SessionID       domain.SessionID
	TargetPath      string
	ExpectedCurrent bool
	Confirmed       bool
	SchemaVersion   string
}

// Validate rejects implicit targets, stale historic intent, and schema drift.
func (intent ExportSummaryIntent) Validate() error {
	if !intent.SessionID.Valid() || !intent.ExpectedCurrent || !intent.Confirmed ||
		intent.SchemaVersion != ExportSummarySchemaVersion || len(intent.TargetPath) < 1 ||
		len(intent.TargetPath) > maxExportTargetBytes || !utf8.ValidString(intent.TargetPath) ||
		strings.TrimSpace(intent.TargetPath) != intent.TargetPath {
		return ErrInvalidUICommand
	}
	for _, character := range intent.TargetPath {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			return ErrInvalidUICommand
		}
	}
	return nil
}

// SessionExportResult identifies a completed local publication without
// returning its sensitive filesystem target.
type SessionExportResult struct {
	SessionID     domain.SessionID
	SchemaVersion string
}

// Validate checks the fixed content-free success projection.
func (result SessionExportResult) Validate() error {
	if !result.SessionID.Valid() || result.SchemaVersion != ExportSummarySchemaVersion {
		return ErrInvalidUIEvent
	}
	return nil
}

// ExportTextProcessor applies the fixed sensitive-value policy before and after rendering.
type ExportTextProcessor interface {
	Process(string, int) (security.TextResult, error)
	ProcessLines(string, int) (security.TextResult, error)
}

// ExportFile is the narrow Application-to-filesystem publication intent.
type ExportFile struct {
	TargetPath string
	Content    []byte
}

// Validate checks only delivery-neutral bounds. Filesystem path policy remains in the adapter.
func (file ExportFile) Validate() error {
	if len(file.TargetPath) < 1 || len(file.TargetPath) > maxExportTargetBytes || !utf8.ValidString(file.TargetPath) ||
		strings.ContainsRune(file.TargetPath, 0) || len(file.Content) < 1 || len(file.Content) > MaxExportSummaryBytes ||
		!utf8.Valid(file.Content) {
		return ErrInvalidExportSummary
	}
	return nil
}

// ExportFileWriter atomically publishes one already-projected bounded summary.
type ExportFileWriter interface {
	WriteSummary(context.Context, ExportFile) error
}

// ExportSessionRecord is the complete Session metadata allowlist read from SQLite.
type ExportSessionRecord struct {
	ID             domain.SessionID
	Title          string
	PrivacyMode    domain.PrivacyMode
	LastScope      *domain.ScopeCandidate
	CreatedAt      time.Time
	LastActivityAt time.Time
	UpdatedAt      time.Time
}

// ExportMessageRecord is one committed user or final assistant source record.
type ExportMessageRecord struct {
	Role      domain.MessageRole
	Content   string
	CreatedAt time.Time
}

// ExportDiagnosisRecord contains the final free-form answer and its bounded
// Evidence and proposed-action metadata.
type ExportDiagnosisRecord struct {
	AnswerMarkdown     string
	ConfirmedFacts     []domain.ConfirmedFact
	Hypotheses         []domain.Hypothesis
	MissingInformation []domain.MissingInformation
	RecommendedActions []domain.RecommendedAction
	ClaimCoverage      []domain.ClaimEvidenceCoverage
	Completeness       domain.AnswerCompletenessManifest
	Clarification      *domain.ClarificationRequest
	CreatedAt          time.Time
}

// ExportEvidenceRecord is one independently eligible accepted Evidence derivative.
type ExportEvidenceRecord struct {
	ID               domain.EvidenceID
	Category         domain.EvidenceCategory
	Resource         domain.ResourceRef
	ResourceType     domain.ResourceType
	PolicyVersion    string
	PolicyGeneration domain.PolicyGeneration
	Fact             string
	SourcePath       string
	ObservedAt       time.Time
	RedactionCount   int
	Truncated        bool
	Partial          bool
}

// SessionExportSnapshot is one consistent SQLite allowlist projection.
type SessionExportSnapshot struct {
	Session        ExportSessionRecord
	ContextSummary *domain.SessionContextSummary
	Messages       []ExportMessageRecord
	Diagnoses      []ExportDiagnosisRecord
	Evidence       []ExportEvidenceRecord
	Truncated      bool
}

// SessionExportReader loads one bounded consistent standard-persistence projection.
type SessionExportReader interface {
	ReadExportSnapshot(context.Context, domain.SessionID) (SessionExportSnapshot, error)
}

// ExportSummary is the versioned project-owned schema rendered as Markdown.
type ExportSummary struct {
	SchemaVersion  string
	ExportedAt     time.Time
	Truncated      bool
	Session        ExportSummarySession
	ContextSummary *ExportSummaryContext
	Messages       []ExportSummaryMessage
	Diagnoses      []ExportSummaryDiagnosis
	Evidence       []ExportSummaryEvidence
}

// ExportSummarySession contains only safe display metadata.
type ExportSummarySession struct {
	ID             domain.SessionID
	Title          string
	PrivacyMode    domain.PrivacyMode
	Context        string
	Namespace      string
	CreatedAt      time.Time
	LastActivityAt time.Time
}

// ExportSummaryMessage is one redacted committed conversation item.
type ExportSummaryMessage struct {
	Role      domain.MessageRole
	Content   string
	CreatedAt time.Time
}

// ExportSummaryContext is the redacted safe summary and content-free coverage
// explanation. It is historic model context, never operational authority.
type ExportSummaryContext struct {
	Text             string
	SchemaVersion    string
	PolicyVersion    string
	CoveredFirstID   domain.MessageID
	CoveredThroughID domain.MessageID
	CoveredCount     int
	CoveredBytes     int
	CoverageDigest   string
	GeneratedAt      time.Time
	AgentProfile     string
	AgentOriginHash  string
	Redacted         bool
	Truncated        bool
	Degraded         bool
}

// ExportSummaryDiagnosis is one redacted free-form answer with independently
// verifiable metadata.
type ExportSummaryDiagnosis struct {
	AnswerMarkdown     string
	ConfirmedFacts     []domain.ConfirmedFact
	Hypotheses         []domain.Hypothesis
	MissingInformation []domain.MissingInformation
	RecommendedActions []domain.RecommendedAction
	ClaimCoverage      []domain.ClaimEvidenceCoverage
	Completeness       domain.AnswerCompletenessManifest
	Clarification      *domain.ClarificationRequest
	CreatedAt          time.Time
}

// ExportSummaryEvidence is one available or expired referenced Evidence projection.
type ExportSummaryEvidence struct {
	ID               domain.EvidenceID
	State            domain.EvidenceDetailState
	Category         domain.EvidenceCategory
	ResourceType     domain.ResourceType
	APIVersion       string
	Kind             string
	Namespace        string
	Name             string
	PolicyVersion    string
	PolicyGeneration domain.PolicyGeneration
	Fact             string
	SourcePath       string
	ObservedAt       time.Time
	Redacted         bool
	Truncated        bool
}

// ProjectExportSummary applies the field allowlist, per-field redaction, and source caps.
func ProjectExportSummary(
	snapshot SessionExportSnapshot,
	exportedAt time.Time,
	processor ExportTextProcessor,
) (ExportSummary, error) {
	if processor == nil || !validCoordinatorTime(exportedAt) || !validExportSession(snapshot.Session) {
		return ExportSummary{}, ErrInvalidExportSummary
	}
	if len(snapshot.Evidence) > MaxExportEvidence {
		return ExportSummary{}, ErrExportSummaryLimit
	}
	title, err := processExportSingleLine(processor, snapshot.Session.Title, 512)
	if err != nil {
		return ExportSummary{}, ErrInvalidExportSummary
	}
	summary := ExportSummary{
		SchemaVersion: ExportSummarySchemaVersion,
		ExportedAt:    exportedAt.UTC().Truncate(time.Millisecond),
		Truncated:     snapshot.Truncated || title.Truncated,
		Session: ExportSummarySession{
			ID: snapshot.Session.ID, Title: title.Value, PrivacyMode: snapshot.Session.PrivacyMode,
			CreatedAt:      snapshot.Session.CreatedAt.UTC().Truncate(time.Millisecond),
			LastActivityAt: snapshot.Session.LastActivityAt.UTC().Truncate(time.Millisecond),
		},
	}
	if snapshot.Session.LastScope != nil {
		contextName, processErr := processExportSingleLine(processor, snapshot.Session.LastScope.Context, 253)
		if processErr != nil {
			return ExportSummary{}, ErrInvalidExportSummary
		}
		namespace, processErr := processExportSingleLine(processor, snapshot.Session.LastScope.Namespace, 63)
		if processErr != nil || contextName.Value == "" || namespace.Value == "" {
			return ExportSummary{}, ErrInvalidExportSummary
		}
		summary.Session.Context = contextName.Value
		summary.Session.Namespace = namespace.Value
		summary.Truncated = summary.Truncated || contextName.Truncated || namespace.Truncated
	}
	if snapshot.ContextSummary != nil {
		projected, processErr := projectExportContextSummary(
			*snapshot.ContextSummary,
			snapshot.Session.ID,
			processor,
		)
		if processErr != nil {
			return ExportSummary{}, processErr
		}
		summary.ContextSummary = &projected
		summary.Truncated = summary.Truncated || projected.Truncated
	}

	messageLimit := min(len(snapshot.Messages), MaxExportMessages)
	if len(snapshot.Messages) > messageLimit {
		summary.Truncated = true
	}
	summary.Messages = make([]ExportSummaryMessage, 0, messageLimit)
	for _, record := range snapshot.Messages[:messageLimit] {
		if (record.Role != domain.MessageRoleUser && record.Role != domain.MessageRoleAssistant) ||
			!validCoordinatorTime(record.CreatedAt) || !utf8.ValidString(record.Content) || strings.TrimSpace(record.Content) == "" {
			return ExportSummary{}, ErrInvalidExportSummary
		}
		content, processErr := processExportText(processor, record.Content, maxExportMessageBytes)
		if processErr != nil || content.Value == "" {
			return ExportSummary{}, ErrInvalidExportSummary
		}
		summary.Truncated = summary.Truncated || content.Truncated
		summary.Messages = append(summary.Messages, ExportSummaryMessage{
			Role: record.Role, Content: content.Value, CreatedAt: record.CreatedAt.UTC().Truncate(time.Millisecond),
		})
	}

	diagnosisLimit := min(len(snapshot.Diagnoses), MaxExportDiagnoses)
	if len(snapshot.Diagnoses) > diagnosisLimit {
		summary.Truncated = true
	}
	summary.Diagnoses = make([]ExportSummaryDiagnosis, 0, diagnosisLimit)
	for _, record := range snapshot.Diagnoses[:diagnosisLimit] {
		projected, changed, projectErr := projectExportDiagnosis(record, processor)
		if projectErr != nil {
			return ExportSummary{}, projectErr
		}
		summary.Truncated = summary.Truncated || changed
		summary.Diagnoses = append(summary.Diagnoses, projected)
	}

	referenced := exportReferencedEvidence(summary.Diagnoses)
	evidenceByID := make(map[domain.EvidenceID]ExportEvidenceRecord, len(snapshot.Evidence))
	for _, record := range snapshot.Evidence {
		if !validExportEvidence(record) {
			return ExportSummary{}, ErrInvalidExportSummary
		}
		if _, duplicate := evidenceByID[record.ID]; duplicate {
			return ExportSummary{}, ErrInvalidExportSummary
		}
		evidenceByID[record.ID] = record
	}
	if len(referenced) > MaxExportEvidence {
		referenced = referenced[:MaxExportEvidence]
		summary.Truncated = true
	}
	summary.Evidence = make([]ExportSummaryEvidence, 0, len(referenced))
	for _, id := range referenced {
		record, available := evidenceByID[id]
		if !available {
			summary.Evidence = append(summary.Evidence, ExportSummaryEvidence{ID: id, State: domain.EvidenceDetailExpired})
			continue
		}
		fact, processErr := processExportText(processor, record.Fact, maxExportEvidenceFactBytes)
		if processErr != nil || fact.Value == "" {
			return ExportSummary{}, ErrInvalidExportSummary
		}
		path := ""
		pathResult := exportProcessedText{}
		if record.SourcePath != "" {
			pathResult, processErr = processExportSingleLine(processor, record.SourcePath, maxExportEvidencePathBytes)
			if processErr != nil {
				return ExportSummary{}, ErrInvalidExportSummary
			}
			path = pathResult.Value
		}
		resourceType, typeOK := exportEvidenceResourceType(record)
		if !typeOK {
			return ExportSummary{}, ErrInvalidExportSummary
		}
		namespace := exportProcessedText{}
		if resourceType.Namespaced() {
			namespace, processErr = processExportSingleLine(processor, record.Resource.Namespace, 63)
			if processErr != nil || namespace.Value == "" {
				return ExportSummary{}, ErrInvalidExportSummary
			}
		}
		name, processErr := processExportSingleLine(processor, record.Resource.Name, 253)
		if processErr != nil || name.Value == "" {
			return ExportSummary{}, ErrInvalidExportSummary
		}
		evidenceTruncated := record.Truncated || record.Partial || fact.Truncated || pathResult.Truncated || namespace.Truncated || name.Truncated
		state := domain.EvidenceDetailAvailable
		if evidenceTruncated {
			state = domain.EvidenceDetailPartial
		}
		summary.Truncated = summary.Truncated || evidenceTruncated
		summary.Evidence = append(summary.Evidence, ExportSummaryEvidence{
			ID: id, State: state, Category: record.Category,
			ResourceType: resourceType, APIVersion: record.Resource.APIVersion, Kind: record.Resource.Kind,
			Namespace: namespace.Value, Name: name.Value,
			PolicyVersion: record.PolicyVersion, PolicyGeneration: record.PolicyGeneration,
			Fact: fact.Value, SourcePath: path, ObservedAt: record.ObservedAt.UTC().Truncate(time.Millisecond),
			Redacted:  record.RedactionCount > 0 || fact.Redacted || pathResult.Redacted || namespace.Redacted || name.Redacted,
			Truncated: evidenceTruncated,
		})
	}
	return summary, nil
}

// RenderExportSummary emits deterministic Markdown and applies the complete-output guard.
func RenderExportSummary(summary ExportSummary, processor ExportTextProcessor) ([]byte, error) {
	if processor == nil || summary.SchemaVersion != ExportSummarySchemaVersion || !validCoordinatorTime(summary.ExportedAt) ||
		!validExportSummarySession(summary.Session) ||
		!validExportContextSummary(summary.ContextSummary, summary.Session.ID) || len(summary.Messages) > MaxExportMessages ||
		len(summary.Diagnoses) > MaxExportDiagnoses || len(summary.Evidence) > MaxExportEvidence {
		return nil, ErrInvalidExportSummary
	}
	for _, evidence := range summary.Evidence {
		if !validExportSummaryEvidence(evidence) {
			return nil, ErrInvalidExportSummary
		}
	}
	var builder strings.Builder
	builder.Grow(min(MaxExportSummaryBytes, maxExportMarkdownOverhead+len(summary.Messages)*maxExportMessageBytes))
	builder.WriteString("# Kupilot Session Summary\n\n")
	fmt.Fprintf(&builder, "Schema: `%s`\n\n", ExportSummarySchemaVersion)
	fmt.Fprintf(&builder, "Exported at: `%s`\n\n", exportTimestamp(summary.ExportedAt))
	fmt.Fprintf(&builder, "Truncated: `%t`\n\n", summary.Truncated)
	builder.WriteString("## Session\n\n")
	fmt.Fprintf(&builder, "- ID: `%s`\n", summary.Session.ID)
	fmt.Fprintf(&builder, "- Title: %s\n", escapeExportMarkdown(summary.Session.Title))
	fmt.Fprintf(&builder, "- Persistence mode: `%s`\n", summary.Session.PrivacyMode)
	fmt.Fprintf(&builder, "- Created at: `%s`\n", exportTimestamp(summary.Session.CreatedAt))
	fmt.Fprintf(&builder, "- Last active: `%s`\n", exportTimestamp(summary.Session.LastActivityAt))
	if summary.Session.Context != "" && summary.Session.Namespace != "" {
		fmt.Fprintf(&builder, "- Historic Context: %s\n", escapeExportMarkdown(summary.Session.Context))
		fmt.Fprintf(&builder, "- Historic Namespace: %s\n", escapeExportMarkdown(summary.Session.Namespace))
	}

	builder.WriteString("\n## Model context\n")
	if summary.ContextSummary == nil {
		builder.WriteString("\n_No persisted model-context summary._\n")
	} else {
		contextSummary := summary.ContextSummary
		builder.WriteString("\nThis is untrusted historic conversation context only. It does not restore scope, Evidence, Tool state, budgets, permissions, reviews, approvals, or action authority.\n\n")
		fmt.Fprintf(&builder, "- Summary schema: `%s`\n", contextSummary.SchemaVersion)
		fmt.Fprintf(&builder, "- Context policy: `%s`\n", contextSummary.PolicyVersion)
		fmt.Fprintf(&builder, "- Covered first Message: `%s`\n", contextSummary.CoveredFirstID)
		fmt.Fprintf(&builder, "- Covered through Message: `%s`\n", contextSummary.CoveredThroughID)
		fmt.Fprintf(&builder, "- Covered Messages: `%d`\n", contextSummary.CoveredCount)
		fmt.Fprintf(&builder, "- Covered content bytes: `%d`\n", contextSummary.CoveredBytes)
		fmt.Fprintf(&builder, "- Coverage digest: `%s`\n", contextSummary.CoverageDigest)
		fmt.Fprintf(&builder, "- Generated at: `%s`\n", exportTimestamp(contextSummary.GeneratedAt))
		fmt.Fprintf(&builder, "- Agent profile: `%s`\n", contextSummary.AgentProfile)
		fmt.Fprintf(&builder, "- Agent origin hash: `%s`\n", contextSummary.AgentOriginHash)
		fmt.Fprintf(&builder, "- Redacted: `%t`\n", contextSummary.Redacted)
		fmt.Fprintf(&builder, "- Truncated: `%t`\n", contextSummary.Truncated)
		fmt.Fprintf(&builder, "- Degraded: `%t`\n\n", contextSummary.Degraded)
		writeExportQuote(&builder, contextSummary.Text)
	}

	builder.WriteString("\n## Conversation\n")
	if len(summary.Messages) == 0 {
		builder.WriteString("\n_None._\n")
	}
	for index, message := range summary.Messages {
		fmt.Fprintf(&builder, "\n### Message %d\n\n", index+1)
		fmt.Fprintf(&builder, "- Role: `%s`\n", message.Role)
		fmt.Fprintf(&builder, "- Created at: `%s`\n\n", exportTimestamp(message.CreatedAt))
		writeExportQuote(&builder, message.Content)
	}

	builder.WriteString("\n## Diagnoses\n")
	if len(summary.Diagnoses) == 0 {
		builder.WriteString("\n_None._\n")
	}
	for index, diagnosis := range summary.Diagnoses {
		fmt.Fprintf(&builder, "\n### Diagnosis %d\n\n", index+1)
		fmt.Fprintf(&builder, "Created at: `%s`\n", exportTimestamp(diagnosis.CreatedAt))
		builder.WriteString("\n#### Answer\n\n")
		writeExportQuote(&builder, diagnosis.AnswerMarkdown)
		writeConfirmedFacts(&builder, diagnosis.ConfirmedFacts)
		writeHypotheses(&builder, diagnosis.Hypotheses)
		writeMissingInformation(&builder, diagnosis.MissingInformation)
		writeRecommendedActions(&builder, diagnosis.RecommendedActions)
		writeClaimCoverage(&builder, diagnosis.ClaimCoverage)
		writeAnswerCompleteness(&builder, diagnosis.Completeness)
		writeClarification(&builder, diagnosis.Clarification)
	}

	builder.WriteString("\n## Evidence references\n")
	if len(summary.Evidence) == 0 {
		builder.WriteString("\n_None._\n")
	}
	for _, evidence := range summary.Evidence {
		fmt.Fprintf(&builder, "\n### `%s`\n\n", evidence.ID)
		fmt.Fprintf(&builder, "- State: %s\n", evidence.State)
		if evidence.State == domain.EvidenceDetailExpired {
			builder.WriteString("- Detail: removed by retention policy and not reconstructed\n")
			continue
		}
		fmt.Fprintf(&builder, "- Category: `%s`\n", evidence.Category)
		resourceName := evidence.Name
		if evidence.Namespace != "" {
			resourceName = evidence.Namespace + "/" + evidence.Name
		}
		fmt.Fprintf(&builder, "- Resource: `%s %s` (`%s`)\n", evidence.Kind, resourceName, evidence.APIVersion)
		fmt.Fprintf(&builder, "- API resource: `%s %s` (`%s`)\n", evidence.ResourceType.APIVersion(), evidence.ResourceType.Resource, evidence.ResourceType.Scope)
		if evidence.PolicyVersion != "" {
			fmt.Fprintf(&builder, "- Evidence policy: `%s` (generation `%d`)\n", evidence.PolicyVersion, evidence.PolicyGeneration)
		}
		fmt.Fprintf(&builder, "- Observed at: `%s`\n", exportTimestamp(evidence.ObservedAt))
		fmt.Fprintf(&builder, "- Redacted: `%t`\n- Truncated: `%t`\n", evidence.Redacted, evidence.Truncated)
		if evidence.SourcePath != "" {
			fmt.Fprintf(&builder, "- Source path: %s\n", escapeExportMarkdown(evidence.SourcePath))
		}
		builder.WriteString("\n")
		writeExportQuote(&builder, evidence.Fact)
	}
	builder.WriteString("\n---\n\nThis file is a bounded, redacted local summary. It excludes raw Tools, raw logs, complete prompts, model traffic, credentials, Secrets, and approval authority. It is not encrypted and deleting it does not guarantee forensic erasure.\n")

	guarded, err := processor.ProcessLines(builder.String(), MaxExportSummaryBytes)
	if err != nil {
		return nil, ErrInvalidExportSummary
	}
	if guarded.Truncated || len(guarded.Value) > MaxExportSummaryBytes {
		return nil, ErrExportSummaryLimit
	}
	return []byte(guarded.Value), nil
}

func validExportSession(record ExportSessionRecord) bool {
	return record.ID.Valid() && record.PrivacyMode == domain.PrivacyModeStandard &&
		validCoordinatorTime(record.CreatedAt) && validCoordinatorTime(record.LastActivityAt) && validCoordinatorTime(record.UpdatedAt) &&
		!record.LastActivityAt.Before(record.CreatedAt) && !record.UpdatedAt.Before(record.LastActivityAt) &&
		utf8.ValidString(record.Title) && len(record.Title) <= 512 &&
		(record.LastScope == nil || record.LastScope.Validate() == nil)
}

func validExportSummarySession(session ExportSummarySession) bool {
	return session.ID.Valid() && session.PrivacyMode == domain.PrivacyModeStandard &&
		validCoordinatorTime(session.CreatedAt) && validCoordinatorTime(session.LastActivityAt) &&
		!session.LastActivityAt.Before(session.CreatedAt) && utf8.ValidString(session.Title) && len(session.Title) <= 512 &&
		((session.Context == "" && session.Namespace == "") ||
			(validExportSingleLine(session.Context, 253) && validExportSingleLine(session.Namespace, 63)))
}

func validExportSingleLine(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsAny(value, "\r\n")
}

func projectExportContextSummary(
	source domain.SessionContextSummary,
	sessionID domain.SessionID,
	processor ExportTextProcessor,
) (ExportSummaryContext, error) {
	if source.Validate() != nil || source.SessionID != sessionID {
		return ExportSummaryContext{}, ErrInvalidExportSummary
	}
	text, err := processExportText(processor, source.Text, domain.MaxSessionSummaryBytes)
	if err != nil || text.Value == "" {
		return ExportSummaryContext{}, ErrInvalidExportSummary
	}
	result := ExportSummaryContext{
		Text: text.Value, SchemaVersion: source.SchemaVersion, PolicyVersion: source.PolicyVersion,
		CoveredFirstID: source.CoveredFirstID, CoveredThroughID: source.CoveredThroughID,
		CoveredCount: source.CoveredCount, CoveredBytes: source.CoveredBytes, CoverageDigest: source.CoverageDigest,
		GeneratedAt: source.GeneratedAt.UTC().Truncate(time.Millisecond), AgentProfile: source.AgentProfile,
		AgentOriginHash: source.AgentOriginHash, Redacted: text.Redacted,
		Truncated: source.Truncated || text.Truncated, Degraded: source.Degraded,
	}
	if !validExportContextSummary(&result, sessionID) {
		return ExportSummaryContext{}, ErrInvalidExportSummary
	}
	return result, nil
}

func validExportContextSummary(summary *ExportSummaryContext, sessionID domain.SessionID) bool {
	if summary == nil {
		return true
	}
	value := domain.SessionContextSummary{
		SessionID: sessionID, Text: summary.Text, SummaryHash: domain.SHA256Hex(summary.Text),
		SchemaVersion: summary.SchemaVersion, PolicyVersion: summary.PolicyVersion,
		CoveredFirstID: summary.CoveredFirstID, CoveredThroughID: summary.CoveredThroughID,
		CoveredCount: summary.CoveredCount, CoveredBytes: summary.CoveredBytes,
		CoverageDigest: summary.CoverageDigest, GeneratedAt: summary.GeneratedAt,
		AgentProfile: summary.AgentProfile, AgentOriginHash: summary.AgentOriginHash,
		Truncated: summary.Truncated, Degraded: summary.Degraded,
	}
	return value.Validate() == nil
}

func validExportEvidence(record ExportEvidenceRecord) bool {
	resourceType, ok := exportEvidenceResourceType(record)
	return ok && record.ID.Valid() && record.Category.Valid() && domain.ValidateResourceRefForType(record.Resource, resourceType) == nil &&
		validEvidencePolicyBinding(record.PolicyVersion, record.PolicyGeneration) &&
		validCoordinatorTime(record.ObservedAt) && record.RedactionCount >= 0 &&
		utf8.ValidString(record.Fact) && strings.TrimSpace(record.Fact) != "" && utf8.ValidString(record.SourcePath) &&
		(record.PolicyVersion != domain.ObservabilityPolicyVersion ||
			allowedUIEvidenceSourcePath(record.Category, record.Resource, record.PolicyVersion, record.SourcePath))
}

func validExportSummaryEvidence(evidence ExportSummaryEvidence) bool {
	if !evidence.ID.Valid() || !evidence.State.Valid() {
		return false
	}
	if evidence.State == domain.EvidenceDetailExpired {
		return evidence == (ExportSummaryEvidence{ID: evidence.ID, State: domain.EvidenceDetailExpired})
	}
	reference := domain.ResourceRef{
		APIVersion: evidence.APIVersion, Kind: evidence.Kind,
		Namespace: evidence.Namespace, Name: evidence.Name,
	}
	return evidence.Category.Valid() && evidence.ResourceType.Validate() == nil &&
		domain.ValidateResourceRefForType(reference, evidence.ResourceType) == nil &&
		validEvidencePolicyBinding(evidence.PolicyVersion, evidence.PolicyGeneration) &&
		validCoordinatorTime(evidence.ObservedAt) && evidence.ObservedAt.Location() == time.UTC &&
		utf8.ValidString(evidence.Fact) && strings.TrimSpace(evidence.Fact) != "" && len(evidence.Fact) <= maxExportEvidenceFactBytes &&
		utf8.ValidString(evidence.SourcePath) && len(evidence.SourcePath) <= maxExportEvidencePathBytes &&
		(evidence.PolicyVersion != domain.ObservabilityPolicyVersion ||
			allowedUIEvidenceSourcePath(evidence.Category, reference, evidence.PolicyVersion, evidence.SourcePath)) &&
		(evidence.State == domain.EvidenceDetailPartial) == evidence.Truncated
}

func exportEvidenceResourceType(record ExportEvidenceRecord) (domain.ResourceType, bool) {
	if record.ResourceType != (domain.ResourceType{}) {
		return record.ResourceType, record.ResourceType.Validate() == nil
	}
	kind, found := domain.ResourceKindForReference(record.Resource)
	if !found {
		return domain.ResourceType{}, false
	}
	return domain.BuiltInResourceType(kind), true
}

type exportProcessedText struct {
	Value     string
	Truncated bool
	Redacted  bool
}

func processExportText(processor ExportTextProcessor, value string, limit int) (exportProcessedText, error) {
	processed, err := processor.ProcessLines(value, limit)
	if err != nil {
		return exportProcessedText{}, err
	}
	return exportProcessedText{
		Value: processed.Value, Truncated: processed.Truncated, Redacted: processed.RedactionCount > 0,
	}, nil
}

func processExportSingleLine(processor ExportTextProcessor, value string, limit int) (exportProcessedText, error) {
	processed, err := processor.Process(value, limit)
	if err != nil {
		return exportProcessedText{}, err
	}
	return exportProcessedText{
		Value: processed.Value, Truncated: processed.Truncated, Redacted: processed.RedactionCount > 0,
	}, nil
}

func projectExportDiagnosis(record ExportDiagnosisRecord, processor ExportTextProcessor) (ExportSummaryDiagnosis, bool, error) {
	if !validCoordinatorTime(record.CreatedAt) || !utf8.ValidString(record.AnswerMarkdown) || strings.TrimSpace(record.AnswerMarkdown) == "" {
		return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
	}
	answer, err := processExportText(processor, record.AnswerMarkdown, maxExportAnswerMarkdownBytes)
	if err != nil || answer.Value == "" {
		return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
	}
	result := ExportSummaryDiagnosis{
		AnswerMarkdown: answer.Value,
		CreatedAt:      record.CreatedAt.UTC().Truncate(time.Millisecond),
	}
	truncated := answer.Truncated

	factLimit := min(len(record.ConfirmedFacts), maxExportDiagnosisItems)
	truncated = truncated || len(record.ConfirmedFacts) > factLimit
	for _, fact := range record.ConfirmedFacts[:factLimit] {
		ids, idsTruncated, err := boundedEvidenceIDs(fact.EvidenceIDs, true)
		if err != nil {
			return ExportSummaryDiagnosis{}, false, err
		}
		statement, err := processExportSingleLine(processor, fact.Statement, maxExportDiagnosisTextBytes)
		if err != nil || statement.Value == "" {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		result.ConfirmedFacts = append(result.ConfirmedFacts, domain.ConfirmedFact{Statement: statement.Value, EvidenceIDs: ids})
		truncated = truncated || idsTruncated || statement.Truncated
	}

	hypothesisLimit := min(len(record.Hypotheses), maxExportDiagnosisItems)
	truncated = truncated || len(record.Hypotheses) > hypothesisLimit
	for _, hypothesis := range record.Hypotheses[:hypothesisLimit] {
		if hypothesis.Confidence != domain.DiagnosisConfidenceLow && hypothesis.Confidence != domain.DiagnosisConfidenceMedium &&
			hypothesis.Confidence != domain.DiagnosisConfidenceHigh {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		ids, idsTruncated, err := boundedEvidenceIDs(hypothesis.SupportingEvidenceIDs, false)
		if err != nil {
			return ExportSummaryDiagnosis{}, false, err
		}
		statement, err := processExportSingleLine(processor, hypothesis.Statement, maxExportDiagnosisTextBytes)
		if err != nil || statement.Value == "" {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		falsifier, err := processExportSingleLine(processor, hypothesis.Falsifier, maxExportDiagnosisTextBytes)
		if err != nil || falsifier.Value == "" {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		result.Hypotheses = append(result.Hypotheses, domain.Hypothesis{
			Statement: statement.Value, SupportingEvidenceIDs: ids, Confidence: hypothesis.Confidence, Falsifier: falsifier.Value,
		})
		truncated = truncated || idsTruncated || statement.Truncated || falsifier.Truncated
	}

	missingLimit := min(len(record.MissingInformation), maxExportDiagnosisItems)
	truncated = truncated || len(record.MissingInformation) > missingLimit
	for _, missing := range record.MissingInformation[:missingLimit] {
		if !validExportMissingKind(missing.Kind) {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		detail, err := processExportSingleLine(processor, missing.Detail, maxExportDiagnosisTextBytes)
		if err != nil || detail.Value == "" {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		impact, err := processExportSingleLine(processor, missing.Impact, maxExportDiagnosisTextBytes)
		if err != nil || impact.Value == "" {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		result.MissingInformation = append(result.MissingInformation, domain.MissingInformation{
			Kind: missing.Kind, Detail: detail.Value, Impact: impact.Value,
		})
		truncated = truncated || detail.Truncated || impact.Truncated
	}

	actionLimit := min(len(record.RecommendedActions), maxExportDiagnosisItems)
	truncated = truncated || len(record.RecommendedActions) > actionLimit
	for _, action := range record.RecommendedActions[:actionLimit] {
		if action.Executed || action.Operation == "" && (action.Target != nil || action.Parameters != nil) || action.Operation != "" &&
			(action.Target == nil || action.Target.Validate() != nil || action.Target.UID != "" || action.Target.ResourceVersion != "") {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		actionText, err := processExportSingleLine(processor, action.Action, maxExportDiagnosisTextBytes)
		if err != nil || actionText.Value == "" {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		risk, err := processExportSingleLine(processor, action.Risk, maxExportDiagnosisTextBytes)
		if err != nil || risk.Value == "" {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		prerequisiteLimit := min(len(action.Prerequisites), maxExportPrerequisites)
		prerequisites := make([]string, 0, prerequisiteLimit)
		prerequisitesChanged := len(action.Prerequisites) > prerequisiteLimit
		for _, prerequisite := range action.Prerequisites[:prerequisiteLimit] {
			value, processErr := processExportSingleLine(processor, prerequisite, 1024)
			if processErr != nil || value.Value == "" {
				return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
			}
			prerequisites = append(prerequisites, value.Value)
			prerequisitesChanged = prerequisitesChanged || value.Truncated
		}
		projectedAction := domain.RecommendedAction{
			Operation: action.Operation, Action: actionText.Value, Risk: risk.Value,
			Prerequisites: prerequisites, Executed: false,
		}
		if action.Target != nil {
			target := *action.Target
			projectedAction.Target = &target
		}
		if action.Parameters != nil {
			parameters := *action.Parameters
			projectedAction.Parameters = &parameters
		}
		result.RecommendedActions = append(result.RecommendedActions, projectedAction)
		truncated = truncated || actionText.Truncated || risk.Truncated || prerequisitesChanged
	}

	coverageLimit := min(len(record.ClaimCoverage), maxExportDiagnosisItems)
	truncated = truncated || len(record.ClaimCoverage) > coverageLimit
	if len(record.ClaimCoverage) > 0 {
		validation := domain.Diagnosis{
			ID: "00000000-0000-7000-8000-000000009999", RunID: record.ClaimCoverage[0].RunID,
			Scope: record.ClaimCoverage[0].Scope, AnswerMarkdown: record.AnswerMarkdown,
			ClaimCoverage: record.ClaimCoverage, CreatedAt: record.CreatedAt,
		}
		if validation.Validate() != nil {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
	}
	for _, coverage := range record.ClaimCoverage[:coverageLimit] {
		claim, processErr := processExportText(processor, coverage.Text, maxExportDiagnosisTextBytes)
		if processErr != nil || claim.Value == "" {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
		ids, idsTruncated, idsErr := boundedEvidenceIDs(coverage.EvidenceIDs, coverage.Kind == domain.ClaimCurrentObservation)
		if idsErr != nil {
			return ExportSummaryDiagnosis{}, false, idsErr
		}
		projectedCoverage := coverage
		projectedCoverage.Text = claim.Value
		projectedCoverage.TextHash = domain.SHA256Hex(claim.Value)
		projectedCoverage.EvidenceIDs = ids
		result.ClaimCoverage = append(result.ClaimCoverage, projectedCoverage)
		truncated = truncated || claim.Truncated || idsTruncated
	}
	if len(result.ClaimCoverage) > 0 {
		validation := domain.Diagnosis{
			ID: "00000000-0000-7000-8000-000000009999", RunID: result.ClaimCoverage[0].RunID,
			Scope: result.ClaimCoverage[0].Scope, AnswerMarkdown: result.AnswerMarkdown,
			ClaimCoverage: result.ClaimCoverage, CreatedAt: result.CreatedAt,
		}
		if validation.Validate() != nil {
			return ExportSummaryDiagnosis{}, false, ErrInvalidExportSummary
		}
	}
	if !record.Completeness.Empty() {
		manifest, manifestErr := projectExportCompleteness(record.Completeness, result.ClaimCoverage, result.MissingInformation, truncated)
		if manifestErr != nil {
			return ExportSummaryDiagnosis{}, false, manifestErr
		}
		result.Completeness = manifest
	}
	if record.Clarification != nil {
		clarification, clarificationChanged, clarificationErr := projectExportClarification(*record.Clarification, processor)
		if clarificationErr != nil {
			return ExportSummaryDiagnosis{}, false, clarificationErr
		}
		result.Clarification = &clarification
		truncated = truncated || clarificationChanged
	}
	return result, truncated, nil
}

func projectExportCompleteness(
	manifest domain.AnswerCompletenessManifest,
	claims []domain.ClaimEvidenceCoverage,
	limitations []domain.MissingInformation,
	truncated bool,
) (domain.AnswerCompletenessManifest, error) {
	if manifest.Validate() != nil || len(manifest.Claims) < len(claims) || len(manifest.Limitations) < len(limitations) {
		return domain.AnswerCompletenessManifest{}, ErrInvalidExportSummary
	}
	result := domain.AnswerCompletenessManifest{
		SchemaVersion: manifest.SchemaVersion, ResponseSchemaVersion: manifest.ResponseSchemaVersion,
		Claims:          append([]domain.ClaimEvidenceCoverage(nil), claims...),
		Limitations:     append([]domain.MissingInformation(nil), limitations...),
		Sources:         make([]domain.AnswerSourceCoverage, len(manifest.Sources)),
		StopReason:      manifest.StopReason,
		StopReasonBasis: manifest.StopReasonBasis,
	}
	for index, source := range manifest.Sources {
		result.Sources[index] = source
		result.Sources[index].EvidenceIDs = append([]domain.EvidenceID(nil), source.EvidenceIDs...)
		if source.ObservedFrom != nil {
			value := *source.ObservedFrom
			result.Sources[index].ObservedFrom = &value
		}
		if source.ObservedThrough != nil {
			value := *source.ObservedThrough
			result.Sources[index].ObservedThrough = &value
		}
	}
	if truncated {
		if !hasExportMissingInformation(result.Limitations, domain.MissingInformationTruncated) {
			if len(result.Limitations) >= domain.MaxAnswerManifestItems {
				return domain.AnswerCompletenessManifest{}, ErrInvalidExportSummary
			}
			result.Limitations = append(result.Limitations, domain.MissingInformation{
				Kind:   domain.MissingInformationTruncated,
				Detail: "The exported answer projection reached a fixed output limit.",
				Impact: "The export does not contain every byte of the committed answer projection.",
			})
		}
		if result.StopReasonBasis == domain.RunTerminalReasonFromCoverage {
			result.StopReason, _ = domain.DeriveAnswerTerminalReason(result.Claims, result.Limitations, result.Sources, false)
		}
	}
	if result.Validate() != nil {
		return domain.AnswerCompletenessManifest{}, ErrInvalidExportSummary
	}
	return result, nil
}

func hasExportMissingInformation(items []domain.MissingInformation, kind domain.MissingInformationKind) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func projectExportClarification(
	request domain.ClarificationRequest,
	processor ExportTextProcessor,
) (domain.ClarificationRequest, bool, error) {
	if request.Validate() != nil {
		return domain.ClarificationRequest{}, false, ErrInvalidExportSummary
	}
	result := domain.ClarificationRequest{SchemaVersion: request.SchemaVersion, Questions: make([]domain.ClarificationQuestion, len(request.Questions))}
	changed := false
	for index, question := range request.Questions {
		prompt, err := processExportSingleLine(processor, question.Prompt, maxExportDiagnosisTextBytes)
		if err != nil || prompt.Value == "" {
			return domain.ClarificationRequest{}, false, ErrInvalidExportSummary
		}
		result.Questions[index] = domain.ClarificationQuestion{
			Sequence: question.Sequence, Kind: question.Kind, Prompt: prompt.Value,
			Choices: make([]domain.ClarificationChoiceValue, len(question.Choices)),
		}
		changed = changed || prompt.Truncated
		for choiceIndex, choice := range question.Choices {
			label, labelErr := processExportSingleLine(processor, choice.Label, maxExportDiagnosisTextBytes)
			if labelErr != nil || label.Value == "" {
				return domain.ClarificationRequest{}, false, ErrInvalidExportSummary
			}
			result.Questions[index].Choices[choiceIndex] = domain.ClarificationChoiceValue{ID: choice.ID, Label: label.Value}
			changed = changed || label.Truncated
		}
	}
	if result.Validate() != nil {
		return domain.ClarificationRequest{}, false, ErrInvalidExportSummary
	}
	return result, changed, nil
}

func boundedEvidenceIDs(ids []domain.EvidenceID, required bool) ([]domain.EvidenceID, bool, error) {
	if required && len(ids) == 0 {
		return nil, false, ErrInvalidExportSummary
	}
	limit := min(len(ids), maxExportCitationsPerItem)
	result := make([]domain.EvidenceID, limit)
	seen := make(map[domain.EvidenceID]struct{}, limit)
	for index, id := range ids[:limit] {
		if !id.Valid() {
			return nil, false, ErrInvalidExportSummary
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, false, ErrInvalidExportSummary
		}
		seen[id] = struct{}{}
		result[index] = id
	}
	return result, len(ids) > limit, nil
}

func validExportMissingKind(kind domain.MissingInformationKind) bool {
	switch kind {
	case domain.MissingInformationAbsent, domain.MissingInformationForbidden, domain.MissingInformationUnsupported,
		domain.MissingInformationStale, domain.MissingInformationConflicting, domain.MissingInformationTruncated,
		domain.MissingInformationSensitiveOutputBlocked:
		return true
	default:
		return false
	}
}

func exportReferencedEvidence(diagnoses []ExportSummaryDiagnosis) []domain.EvidenceID {
	set := make(map[domain.EvidenceID]struct{})
	for _, diagnosis := range diagnoses {
		for _, fact := range diagnosis.ConfirmedFacts {
			for _, id := range fact.EvidenceIDs {
				set[id] = struct{}{}
			}
		}
		for _, hypothesis := range diagnosis.Hypotheses {
			for _, id := range hypothesis.SupportingEvidenceIDs {
				set[id] = struct{}{}
			}
		}
		for _, coverage := range diagnosis.ClaimCoverage {
			for _, id := range coverage.EvidenceIDs {
				set[id] = struct{}{}
			}
		}
	}
	result := make([]domain.EvidenceID, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

// ExportEvidenceReferences applies the versioned Diagnosis and citation caps
// before an adapter reads any Evidence source rows.
func ExportEvidenceReferences(diagnoses []ExportDiagnosisRecord) ([]domain.EvidenceID, bool, error) {
	diagnosisLimit := min(len(diagnoses), MaxExportDiagnoses)
	truncated := len(diagnoses) > diagnosisLimit
	set := make(map[domain.EvidenceID]struct{})
	for _, diagnosis := range diagnoses[:diagnosisLimit] {
		factLimit := min(len(diagnosis.ConfirmedFacts), maxExportDiagnosisItems)
		truncated = truncated || len(diagnosis.ConfirmedFacts) > factLimit
		for _, fact := range diagnosis.ConfirmedFacts[:factLimit] {
			ids, idsTruncated, err := boundedEvidenceIDs(fact.EvidenceIDs, true)
			if err != nil {
				return nil, false, err
			}
			truncated = truncated || idsTruncated
			for _, id := range ids {
				set[id] = struct{}{}
			}
		}
		hypothesisLimit := min(len(diagnosis.Hypotheses), maxExportDiagnosisItems)
		truncated = truncated || len(diagnosis.Hypotheses) > hypothesisLimit
		for _, hypothesis := range diagnosis.Hypotheses[:hypothesisLimit] {
			ids, idsTruncated, err := boundedEvidenceIDs(hypothesis.SupportingEvidenceIDs, false)
			if err != nil {
				return nil, false, err
			}
			truncated = truncated || idsTruncated
			for _, id := range ids {
				set[id] = struct{}{}
			}
		}
		coverageLimit := min(len(diagnosis.ClaimCoverage), maxExportDiagnosisItems)
		truncated = truncated || len(diagnosis.ClaimCoverage) > coverageLimit
		for _, coverage := range diagnosis.ClaimCoverage[:coverageLimit] {
			ids, idsTruncated, err := boundedEvidenceIDs(coverage.EvidenceIDs, coverage.Kind == domain.ClaimCurrentObservation)
			if err != nil {
				return nil, false, err
			}
			truncated = truncated || idsTruncated
			for _, id := range ids {
				set[id] = struct{}{}
			}
		}
	}
	result := make([]domain.EvidenceID, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	if len(result) > MaxExportEvidence {
		result = result[:MaxExportEvidence]
		truncated = true
	}
	return result, truncated, nil
}

func exportTimestamp(value time.Time) string {
	return value.UTC().Truncate(time.Millisecond).Format(exportSummaryTimestampLayout)
}

func escapeExportMarkdown(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "{", "\\{", "}", "\\}",
		"[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "#", "\\#", "+", "\\+",
		"-", "\\-", "!", "\\!", "|", "\\|", ">", "\\>", "<", "\\<",
	)
	return replacer.Replace(value)
}

func writeExportQuote(builder *strings.Builder, value string) {
	for _, line := range strings.Split(escapeExportMarkdown(value), "\n") {
		fmt.Fprintf(builder, "> %s\n", line)
	}
}

func writeConfirmedFacts(builder *strings.Builder, values []domain.ConfirmedFact) {
	builder.WriteString("\n#### Confirmed facts\n")
	if len(values) == 0 {
		builder.WriteString("\n_None._\n")
		return
	}
	for index, fact := range values {
		fmt.Fprintf(builder, "\n%d. %s\n", index+1, escapeExportMarkdown(fact.Statement))
		writeEvidenceIDs(builder, "Evidence", fact.EvidenceIDs)
	}
}

func writeHypotheses(builder *strings.Builder, values []domain.Hypothesis) {
	builder.WriteString("\n#### Hypotheses\n")
	if len(values) == 0 {
		builder.WriteString("\n_None._\n")
		return
	}
	for index, hypothesis := range values {
		fmt.Fprintf(builder, "\n%d. %s\n", index+1, escapeExportMarkdown(hypothesis.Statement))
		fmt.Fprintf(builder, "   - Confidence: `%s`\n", hypothesis.Confidence)
		fmt.Fprintf(builder, "   - Falsifier: %s\n", escapeExportMarkdown(hypothesis.Falsifier))
		writeEvidenceIDs(builder, "Supporting Evidence", hypothesis.SupportingEvidenceIDs)
	}
}

func writeMissingInformation(builder *strings.Builder, values []domain.MissingInformation) {
	builder.WriteString("\n#### Missing information\n")
	if len(values) == 0 {
		builder.WriteString("\n_None._\n")
		return
	}
	for index, missing := range values {
		fmt.Fprintf(builder, "\n%d. `%s`: %s\n", index+1, missing.Kind, escapeExportMarkdown(missing.Detail))
		fmt.Fprintf(builder, "   - Impact: %s\n", escapeExportMarkdown(missing.Impact))
	}
}

func writeRecommendedActions(builder *strings.Builder, values []domain.RecommendedAction) {
	builder.WriteString("\n#### Recommended actions\n")
	if len(values) == 0 {
		builder.WriteString("\n_None._\n")
		return
	}
	for index, action := range values {
		fmt.Fprintf(builder, "\n%d. %s\n", index+1, escapeExportMarkdown(action.Action))
		if action.Operation != "" && action.Target != nil {
			fmt.Fprintf(builder, "   - Operation: `%s`\n", action.Operation)
			fmt.Fprintf(builder, "   - Target: `%s %s/%s`\n", action.Target.Kind, action.Target.Namespace, action.Target.Name)
		}
		fmt.Fprintf(builder, "   - Risk: %s\n", escapeExportMarkdown(action.Risk))
		builder.WriteString("   - Executed: `false`\n")
		for _, prerequisite := range action.Prerequisites {
			fmt.Fprintf(builder, "   - Prerequisite: %s\n", escapeExportMarkdown(prerequisite))
		}
	}
}

func writeClaimCoverage(builder *strings.Builder, values []domain.ClaimEvidenceCoverage) {
	builder.WriteString("\n#### Claim coverage\n")
	if len(values) == 0 {
		builder.WriteString("\n_None retained._\n")
		return
	}
	for _, coverage := range values {
		fmt.Fprintf(builder, "\n%d. `%s` · `%s`\n", coverage.Sequence, coverage.Kind, coverage.State)
		fmt.Fprintf(builder, "   - Claim hash: `%s`\n", coverage.TextHash)
		fmt.Fprintf(builder, "   - Source run: `%s`\n", coverage.RunID)
		fmt.Fprintf(builder, "   - Scope generation: `%d`\n", coverage.Scope.Generation)
		fmt.Fprintf(builder, "   - Policy generation: `%d`\n", coverage.PolicyGeneration)
		writeEvidenceIDs(builder, "Evidence", coverage.EvidenceIDs)
		writeExportQuote(builder, coverage.Text)
	}
}

func writeAnswerCompleteness(builder *strings.Builder, manifest domain.AnswerCompletenessManifest) {
	if manifest.Empty() {
		return
	}
	builder.WriteString("\n#### Answer completeness\n\n")
	fmt.Fprintf(builder, "- Schema: `%s`\n", manifest.SchemaVersion)
	fmt.Fprintf(builder, "- Stop reason: `%s`\n", manifest.StopReason)
	fmt.Fprintf(builder, "- Declared sources: `%d`\n", len(manifest.Sources))
	for _, source := range manifest.Sources {
		fmt.Fprintf(builder, "- Source %d: `%s` · `%s` · `%s`\n", source.Sequence, source.State, source.Freshness, source.Conflict)
		fmt.Fprintf(builder, "  - Source hash: `%s`\n", source.SourceHash)
		fmt.Fprintf(builder, "  - Subject hash: `%s`\n", source.SubjectHash)
		writeEvidenceIDs(builder, "Evidence", source.EvidenceIDs)
	}
	builder.WriteString("\nThis manifest proves structural ownership and declared coverage only; it does not prove semantic correctness or exhaustive facts.\n")
}

func writeClarification(builder *strings.Builder, request *domain.ClarificationRequest) {
	if request == nil {
		return
	}
	builder.WriteString("\n#### Structured clarification\n")
	for _, question := range request.Questions {
		fmt.Fprintf(builder, "\n%d. %s\n", question.Sequence, escapeExportMarkdown(question.Prompt))
		for _, choice := range question.Choices {
			fmt.Fprintf(builder, "   - `%s`: %s\n", choice.ID, escapeExportMarkdown(choice.Label))
		}
	}
}

func writeEvidenceIDs(builder *strings.Builder, label string, values []domain.EvidenceID) {
	if len(values) == 0 {
		return
	}
	encoded := make([]string, len(values))
	for index, id := range values {
		encoded[index] = "`" + string(id) + "`"
	}
	fmt.Fprintf(builder, "   - %s: %s\n", label, strings.Join(encoded, ", "))
}

func (coordinator *Coordinator) executeExportSessionCommand(
	ctx context.Context,
	command UICommand,
) (UICommandOutcome, error) {
	result := UICommandOutcome{Command: command.Kind, RequestID: command.RequestID}
	intent := *command.Export
	coordinator.mu.Lock()
	challenge := coordinator.privacyChallenge
	if challenge == nil || challenge.requestID != command.RequestID || challenge.revision != command.PrivacyRevision {
		coordinator.mu.Unlock()
		return UICommandOutcome{}, ErrPrivacyReviewStale
	}
	coordinator.privacyChallenge = nil
	coordinator.mu.Unlock()

	if err := coordinator.beginUIOperation(true); err != nil {
		return UICommandOutcome{}, err
	}
	defer coordinator.finishOperation()

	coordinator.mu.Lock()
	current := coordinator.currentSession
	currentMatches := current != nil && current.ID == intent.SessionID &&
		current.PrivacyMode == domain.PrivacyModeStandard
	coordinator.mu.Unlock()
	if !currentMatches || coordinator.exports == nil || coordinator.exportFiles == nil || coordinator.exportText == nil {
		result.Failure = UIQueryUnavailable
		return result, nil
	}

	snapshotContext, cancelSnapshot := context.WithTimeout(ctx, coordinator.persistenceLimit)
	snapshot, err := coordinator.exports.ReadExportSnapshot(snapshotContext, intent.SessionID)
	cancelSnapshot()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return UICommandOutcome{}, contextErr
		}
		if !errors.Is(err, ErrSessionExportUnavailable) && !errors.Is(err, ErrSessionNotResumable) {
			coordinator.markGlobalPersistenceDegraded()
		}
		result.Failure = UIQueryUnavailable
		return result, nil
	}
	if snapshot.Session.ID != intent.SessionID || snapshot.Session.PrivacyMode != domain.PrivacyModeStandard {
		result.Failure = UIQueryUnavailable
		return result, nil
	}

	exportedAt := coordinator.now()
	summary, err := ProjectExportSummary(snapshot, exportedAt, coordinator.exportText)
	if err != nil {
		result.Failure = UIQueryUnavailable
		return result, nil
	}
	content, err := RenderExportSummary(summary, coordinator.exportText)
	if err != nil {
		result.Failure = UIQueryUnavailable
		return result, nil
	}
	audit, err := coordinator.newSessionExportAudit(intent.SessionID, exportedAt)
	if err != nil {
		coordinator.markGlobalPersistenceDegraded()
		result.Failure = UIQueryUnavailable
		return result, nil
	}
	if err := coordinator.persist(ctx, func(operationContext context.Context) error {
		return coordinator.audits.Append(operationContext, audit)
	}); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return UICommandOutcome{}, contextErr
		}
		coordinator.markGlobalPersistenceDegraded()
		result.Failure = UIQueryUnavailable
		return result, nil
	}
	if err := coordinator.exportFiles.WriteSummary(ctx, ExportFile{TargetPath: intent.TargetPath, Content: content}); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return UICommandOutcome{}, contextErr
		}
		result.Failure = UIQueryUnavailable
		return result, nil
	}
	result.Export = &SessionExportResult{SessionID: intent.SessionID, SchemaVersion: ExportSummarySchemaVersion}
	return result, nil
}

func (coordinator *Coordinator) newSessionExportAudit(
	sessionID domain.SessionID,
	occurredAt time.Time,
) (domain.AuditEvent, error) {
	id, err := coordinator.auditIdentifiers.NewAuditEventID()
	if err != nil || !id.Valid() || !validCoordinatorTime(occurredAt) {
		return domain.AuditEvent{}, ErrPersistenceUnavailable
	}
	operation, policyVersion := "export_summary", ExportSummarySchemaVersion
	event := domain.AuditEvent{
		ID: id, SessionID: &sessionID, Type: domain.AuditEventSessionExportRequested,
		Actor: domain.AuditActorUser, Outcome: domain.AuditOutcomeSuccess,
		Details:    domain.AuditDetails{Operation: &operation, PolicyVersion: &policyVersion},
		OccurredAt: occurredAt,
	}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrPersistenceUnavailable
	}
	return event, nil
}
