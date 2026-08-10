package application

import (
	"errors"
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
	Kind      UIStartKind
	SessionID domain.SessionID
}

// Validate checks that only exact-ID resume carries a Session identifier.
func (intent UIStartIntent) Validate() error {
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
	UIQueryUnavailable UIQueryFailureCode = "unavailable"
	UIQueryForbidden   UIQueryFailureCode = "forbidden"
	UIQueryTimeout     UIQueryFailureCode = "timeout"
)

func (code UIQueryFailureCode) valid() bool {
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
		if !result.Failure.valid() || count != 0 {
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
	Session       UISessionCandidate
	SavedScope    *domain.ScopeCandidate
	SavedResource *UIResourceCandidate
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
	if result.Session == nil || !validSessionCandidates([]UISessionCandidate{result.Session.Session}) {
		return ErrInvalidUIQueryResult
	}
	if result.Session.SavedScope != nil && result.Session.SavedScope.Validate() != nil {
		return ErrInvalidUIQueryResult
	}
	if result.Session.SavedResource != nil {
		if result.Session.SavedScope == nil || result.Session.SavedResource.Namespace != result.Session.SavedScope.Namespace ||
			!validResourceCandidates([]UIResourceCandidate{*result.Session.SavedResource}) {
			return ErrInvalidUIQueryResult
		}
	}
	return nil
}
