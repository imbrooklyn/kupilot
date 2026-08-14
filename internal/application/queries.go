package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	// MaxUIQueryCandidates bounds every completion source before delivery.
	MaxUIQueryCandidates = 50
	maxUIFilterBytes     = 512
	maxUICandidateStatus = 256
)

var (
	// ErrInvalidUIStartIntent reports an invalid fixed startup intent.
	ErrInvalidUIStartIntent = errors.New("UI start intent is invalid")
	// ErrInvalidUIQuery reports invalid completion or resume query data.
	ErrInvalidUIQuery = errors.New("UI query data is invalid")
	// ErrInvalidUIQueryResult reports an invalid bounded query projection.
	ErrInvalidUIQueryResult = errors.New("UI query result is invalid")
	// ErrSessionResumeUnavailable is the non-disclosing missing, corrupt, or
	// otherwise ineligible exact-resume outcome.
	ErrSessionResumeUnavailable = errors.New("the Session is unavailable for resume")
	// ErrSessionNotResumable is the stable minimal-persistence outcome.
	ErrSessionNotResumable = errors.New("the Session cannot be resumed (session_not_resumable)")
	// ErrNoResumableSession reports an empty global resume set.
	ErrNoResumableSession = errors.New("no resumable Session is available")
)

// UIStartKind identifies the four Session start paths admitted by the product.
type UIStartKind string

const (
	UIStartNew          UIStartKind = "new"
	UIStartResumePicker UIStartKind = "resume_picker"
	UIStartResumeID     UIStartKind = "resume_id"
	UIStartResumeLast   UIStartKind = "resume_last"
)

// UIStartIntent is the delivery-neutral startup state consumed by the TUI.
type UIStartIntent struct {
	Kind                UIStartKind
	SessionID           domain.SessionID
	ExplicitScope       bool
	ConfiguredContext   string
	ConfiguredNamespace string
}

// UISessionState is the bounded current Session projection used by delivery.
type UISessionState struct {
	ID          domain.SessionID
	Title       string
	PrivacyMode domain.PrivacyMode
	Resumed     bool
}

func (state UISessionState) validate() bool {
	return state.ID.Valid() && validUIBoundedText(state.Title, 0, 512) &&
		(state.PrivacyMode == domain.PrivacyModeStandard || state.PrivacyMode == domain.PrivacyModeMinimal)
}

// UIStartResult is the side-effect result of one fixed CLI start intent.
type UIStartResult struct {
	Intent         UIStartIntent
	Session        *UISessionState
	ScopeCandidate *domain.ScopeCandidate
}

// Validate checks that only a new start creates a Session immediately.
func (result UIStartResult) Validate() error {
	if result.Intent.Validate() != nil {
		return ErrInvalidUIQueryResult
	}
	if result.Intent.Kind == UIStartNew {
		if result.Session == nil || !result.Session.validate() || result.Session.Resumed || result.ScopeCandidate != nil {
			return ErrInvalidUIQueryResult
		}
		return nil
	}
	if result.Session != nil {
		return ErrInvalidUIQueryResult
	}
	if result.ScopeCandidate != nil && result.ScopeCandidate.Validate() != nil {
		return ErrInvalidUIQueryResult
	}
	return nil
}

// ResumeSessionRecord is safe global picker metadata returned by a bounded
// history or search adapter before delivery projection.
type ResumeSessionRecord struct {
	ID          domain.SessionID
	Title       string
	UpdatedAt   time.Time
	PrivacyMode domain.PrivacyMode
	LastScope   *domain.ScopeCandidate
}

// ResumedSessionRecord contains only a validated standard Session and bounded
// committed safe history. Historic values never carry live authority.
type ResumedSessionRecord struct {
	Session  domain.Session
	Messages []domain.Message
}

// SessionResumeStore owns the three explicit history reads admitted by the
// product. Bare startup has no method that can query history implicitly.
type SessionResumeStore interface {
	ListResumable(context.Context, int) ([]ResumeSessionRecord, error)
	ResumeByID(context.Context, domain.SessionID) (ResumedSessionRecord, error)
	ResumeLatest(context.Context) (ResumedSessionRecord, error)
}

// StartupMaintenance owns mandatory bounded recovery and retention work. It
// performs no model, Tool, or Kubernetes action.
type StartupMaintenance interface {
	RecoverInterrupted(context.Context, time.Time) error
	CleanupRetention(context.Context, time.Time) error
}

// Validate checks that only exact-ID resume carries a Session identifier.
func (intent UIStartIntent) Validate() error {
	if (intent.ConfiguredContext != "" && !domain.ValidContextName(intent.ConfiguredContext)) ||
		(intent.ConfiguredNamespace != "" && !domain.ValidNamespaceName(intent.ConfiguredNamespace)) ||
		intent.ExplicitScope && intent.ConfiguredContext == "" && intent.ConfiguredNamespace == "" {
		return ErrInvalidUIStartIntent
	}
	switch intent.Kind {
	case UIStartNew, UIStartResumePicker, UIStartResumeLast:
		if intent.SessionID != "" {
			return ErrInvalidUIStartIntent
		}
	case UIStartResumeID:
		if !intent.SessionID.Valid() {
			return ErrInvalidUIStartIntent
		}
	default:
		return ErrInvalidUIStartIntent
	}
	return nil
}

// UICompletionKind identifies one fixed typed Picker source.
type UICompletionKind string

const (
	UICompletionContext   UICompletionKind = "context"
	UICompletionNamespace UICompletionKind = "namespace"
	UICompletionResource  UICompletionKind = "resource"
	UICompletionSession   UICompletionKind = "session"
)

// UICompletionQuery requests bounded safe candidates for the sole composer.
type UICompletionQuery struct {
	RequestID       uint64
	Kind            UICompletionKind
	Filter          string
	ScopeGeneration int64
	Limit           int
	ResourceKind    domain.ResourceKind
}

// Validate checks identity, scope binding, fixed Kind, and hard limits.
func (query UICompletionQuery) Validate() error {
	if query.RequestID == 0 || query.ScopeGeneration < 0 || query.Limit < 1 || query.Limit > MaxUIQueryCandidates ||
		len(query.Filter) > maxUIFilterBytes || !utf8.ValidString(query.Filter) {
		return ErrInvalidUIQuery
	}
	switch query.Kind {
	case UICompletionContext, UICompletionSession:
		if query.ResourceKind != "" {
			return ErrInvalidUIQuery
		}
	case UICompletionNamespace:
		if query.ScopeGeneration < 1 || query.ResourceKind != "" {
			return ErrInvalidUIQuery
		}
	case UICompletionResource:
		if query.ScopeGeneration < 1 || query.ResourceKind != "" && !query.ResourceKind.Valid() {
			return ErrInvalidUIQuery
		}
	default:
		return ErrInvalidUIQuery
	}
	return nil
}

// UIQueryFailureCode is a fixed non-disclosing completion failure class.
type UIQueryFailureCode string

const (
	UIQueryUnavailable     UIQueryFailureCode = "unavailable"
	UIQueryForbidden       UIQueryFailureCode = "forbidden"
	UIQueryTimeout         UIQueryFailureCode = "timeout"
	UIQueryNotResumable    UIQueryFailureCode = "session_not_resumable"
	UIQueryConsentRequired UIQueryFailureCode = "consent_required"
)

func (code UIQueryFailureCode) valid() bool {
	return code == UIQueryUnavailable || code == UIQueryForbidden || code == UIQueryTimeout ||
		code == UIQueryNotResumable || code == UIQueryConsentRequired
}

func (code UIQueryFailureCode) validOperational() bool {
	return code == UIQueryUnavailable || code == UIQueryForbidden || code == UIQueryTimeout
}

// UIContextCandidate contains only a safe Context display name.
type UIContextCandidate struct {
	Name    string
	Current bool
}

// UINamespaceCandidate contains only one current-Context Namespace name.
type UINamespaceCandidate struct {
	Name string
}

// UIResourceCandidate is a bounded current-Namespace Picker projection.
type UIResourceCandidate struct {
	APIVersion string
	Kind       domain.ResourceKind
	Namespace  string
	Name       string
	Status     string
}

// UISessionCandidate contains safe resume metadata and no Message preview.
type UISessionCandidate struct {
	ID                  domain.SessionID
	Title               string
	UpdatedAtUnixMillis int64
	Context             string
	Namespace           string
	PrivacyMode         domain.PrivacyMode
}

// UICompletionResult contains exactly the payload selected by Kind.
type UICompletionResult struct {
	RequestID       uint64
	Kind            UICompletionKind
	ScopeGeneration int64
	Failure         UIQueryFailureCode
	Contexts        []UIContextCandidate
	Namespaces      []UINamespaceCandidate
	Resources       []UIResourceCandidate
	Sessions        []UISessionCandidate
}

// Validate checks result identity, payload exclusivity, bounds, and candidates.
func (result UICompletionResult) Validate() error {
	if result.RequestID == 0 || result.ScopeGeneration < 0 || !validCompletionKind(result.Kind) {
		return ErrInvalidUIQueryResult
	}
	if (result.Kind == UICompletionNamespace || result.Kind == UICompletionResource) && result.ScopeGeneration < 1 {
		return ErrInvalidUIQueryResult
	}
	count := len(result.Contexts) + len(result.Namespaces) + len(result.Resources) + len(result.Sessions)
	if count > MaxUIQueryCandidates {
		return ErrInvalidUIQueryResult
	}
	if result.Failure != "" {
		if !result.Failure.validOperational() || count != 0 {
			return ErrInvalidUIQueryResult
		}
		return nil
	}

	switch result.Kind {
	case UICompletionContext:
		if len(result.Namespaces)+len(result.Resources)+len(result.Sessions) != 0 || !validContextCandidates(result.Contexts) {
			return ErrInvalidUIQueryResult
		}
	case UICompletionNamespace:
		if len(result.Contexts)+len(result.Resources)+len(result.Sessions) != 0 || !validNamespaceCandidates(result.Namespaces) {
			return ErrInvalidUIQueryResult
		}
	case UICompletionResource:
		if len(result.Contexts)+len(result.Namespaces)+len(result.Sessions) != 0 || !validResourceCandidates(result.Resources) {
			return ErrInvalidUIQueryResult
		}
	case UICompletionSession:
		if len(result.Contexts)+len(result.Namespaces)+len(result.Resources) != 0 || !validSessionCandidates(result.Sessions) {
			return ErrInvalidUIQueryResult
		}
	}
	return nil
}

func validCompletionKind(kind UICompletionKind) bool {
	return kind == UICompletionContext || kind == UICompletionNamespace ||
		kind == UICompletionResource || kind == UICompletionSession
}

func validContextCandidates(candidates []UIContextCandidate) bool {
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !validUIBoundedText(candidate.Name, 1, 253) || duplicateUIValue(seen, candidate.Name) {
			return false
		}
	}
	return true
}

func validNamespaceCandidates(candidates []UINamespaceCandidate) bool {
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !validUIBoundedText(candidate.Name, 1, 63) || duplicateUIValue(seen, candidate.Name) {
			return false
		}
	}
	return true
}

func validResourceCandidates(candidates []UIResourceCandidate) bool {
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		reference := domain.ResourceRef{
			APIVersion: candidate.APIVersion,
			Kind:       string(candidate.Kind),
			Namespace:  candidate.Namespace,
			Name:       candidate.Name,
		}
		key := candidate.APIVersion + "\x00" + string(candidate.Kind) + "\x00" + candidate.Namespace + "\x00" + candidate.Name
		if reference.Validate() != nil || !validUIBoundedText(candidate.Status, 0, maxUICandidateStatus) || duplicateUIValue(seen, key) {
			return false
		}
	}
	return true
}

func validSessionCandidates(candidates []UISessionCandidate) bool {
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !candidate.ID.Valid() || candidate.PrivacyMode != domain.PrivacyModeStandard || candidate.UpdatedAtUnixMillis < 0 ||
			!validUIBoundedText(candidate.Title, 0, 512) ||
			(candidate.Context == "") != (candidate.Namespace == "") ||
			candidate.Context != "" && (!validUIBoundedText(candidate.Context, 1, 253) || !validUIBoundedText(candidate.Namespace, 1, 63)) ||
			duplicateUIValue(seen, string(candidate.ID)) {
			return false
		}
	}
	return true
}

func validUIBoundedText(value string, minimum, maximum int) bool {
	return utf8.ValidString(value) && len(value) >= minimum && len(value) <= maximum
}

func duplicateUIValue(seen map[string]struct{}, value string) bool {
	if _, exists := seen[value]; exists {
		return true
	}
	seen[value] = struct{}{}
	return false
}

// UIResumeMode identifies exact-ID and global-last resume loading.
type UIResumeMode string

const (
	UIResumeExact UIResumeMode = "exact"
	UIResumeLast  UIResumeMode = "last"
)

// UIResumeRequest loads one eligible safe conversation container.
type UIResumeRequest struct {
	RequestID uint64
	Mode      UIResumeMode
	SessionID domain.SessionID
}

// Validate checks that only exact mode carries a Session identifier.
func (request UIResumeRequest) Validate() error {
	if request.RequestID == 0 {
		return ErrInvalidUIQuery
	}
	switch request.Mode {
	case UIResumeExact:
		if !request.SessionID.Valid() {
			return ErrInvalidUIQuery
		}
	case UIResumeLast:
		if request.SessionID != "" {
			return ErrInvalidUIQuery
		}
	default:
		return ErrInvalidUIQuery
	}
	return nil
}

// UIResumedSession is safe history metadata with no live scope authority.
type UIResumedSession struct {
	ResumeRequestID uint64
	Session         UISessionCandidate
	SavedScope      *domain.ScopeCandidate
	SavedResource   *UIResourceCandidate
	History         []UIHistoryMessage
}

// UIHistoryMessage is bounded committed conversation content. Historic
// Evidence references carry display correlation only and no live run authority.
type UIHistoryMessage struct {
	Role               domain.MessageRole
	Format             domain.MessageFormat
	Content            string
	RunID              domain.AgentRunID
	EvidenceReferences []UIEvidenceReference
}

func (message UIHistoryMessage) valid() bool {
	if (message.Role != domain.MessageRoleUser && message.Role != domain.MessageRoleAssistant && message.Role != domain.MessageRoleSystemNotice) ||
		(message.Format != domain.MessageFormatPlain && message.Format != domain.MessageFormatMarkdown) ||
		!validUIBoundedText(message.Content, 1, MaxQuestionBytes) || len(message.EvidenceReferences) > 100 {
		return false
	}
	if message.RunID != "" && !message.RunID.Valid() || len(message.EvidenceReferences) > 0 && message.Role != domain.MessageRoleAssistant {
		return false
	}
	for _, reference := range message.EvidenceReferences {
		if reference.Validate() != nil || reference.RunID != message.RunID {
			return false
		}
	}
	return true
}

// UIResumeResult returns either one eligible Session or one fixed failure.
type UIResumeResult struct {
	RequestID uint64
	Mode      UIResumeMode
	Session   *UIResumedSession
	Failure   UIQueryFailureCode
}

// Validate checks request correlation and safe resumed metadata.
func (result UIResumeResult) Validate() error {
	if result.RequestID == 0 || result.Mode != UIResumeExact && result.Mode != UIResumeLast {
		return ErrInvalidUIQueryResult
	}
	if result.Failure != "" {
		if !result.Failure.valid() || result.Session != nil {
			return ErrInvalidUIQueryResult
		}
		return nil
	}
	if result.Session == nil || !validUIResumedSession(*result.Session, result.RequestID) {
		return ErrInvalidUIQueryResult
	}
	return nil
}

func validUIResumedSession(session UIResumedSession, requestID uint64) bool {
	if session.ResumeRequestID != requestID ||
		!validSessionCandidates([]UISessionCandidate{session.Session}) || len(session.History) > 100 {
		return false
	}
	for _, message := range session.History {
		if !message.valid() {
			return false
		}
	}
	if session.SavedScope != nil && session.SavedScope.Validate() != nil {
		return false
	}
	if session.SavedResource != nil {
		if session.SavedScope == nil || session.SavedResource.Namespace != session.SavedScope.Namespace ||
			!validResourceCandidates([]UIResourceCandidate{*session.SavedResource}) {
			return false
		}
	}
	return true
}

func (record ResumeSessionRecord) valid() bool {
	if !record.ID.Valid() || record.PrivacyMode != domain.PrivacyModeStandard ||
		!validUIBoundedText(record.Title, 0, 512) || record.UpdatedAt.IsZero() || record.UpdatedAt.UnixMilli() < 0 {
		return false
	}
	return record.LastScope == nil || record.LastScope.Validate() == nil
}

func (record ResumedSessionRecord) valid() bool {
	if record.Session.Validate() != nil || !record.Session.HasResumableMetadata() ||
		len(record.Messages) == 0 || len(record.Messages) > 100 {
		return false
	}
	for _, message := range record.Messages {
		if message.Validate() != nil || message.SessionID != record.Session.ID || message.Status != domain.MessageStatusCommitted {
			return false
		}
	}
	return true
}

func sessionRecordMatches(record ResumeSessionRecord, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" || strings.Contains(strings.ToLower(record.Title), filter) ||
		strings.Contains(strings.ToLower(record.UpdatedAt.UTC().Format("2006-01-02 15:04Z")), filter) {
		return true
	}
	return record.LastScope != nil && (strings.Contains(strings.ToLower("ctx/"+record.LastScope.Context), filter) ||
		strings.Contains(strings.ToLower("ns/"+record.LastScope.Namespace), filter))
}
