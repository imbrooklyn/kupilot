// Package domain contains KuPilot-owned values and invariants without I/O.
package domain

import (
	"errors"
	"time"
	"unicode/utf8"
)

const (
	maxSessionTitleBytes    = 512
	maxSessionSummaryBytes  = 4096
	maxContextBytes         = 253
	maxNamespaceBytes       = 63
	maxAPIVersionBytes      = 253
	maxKindBytes            = 63
	maxResourceNameBytes    = 253
	maxResourceUIDBytes     = 256
	maxResourceVersionBytes = 256
)

var (
	// ErrInvalidSession reports a Session invariant failure without echoing data.
	ErrInvalidSession = errors.New("Session data is invalid")
	// ErrInvalidScopeCandidate reports an incomplete or oversized historic scope.
	ErrInvalidScopeCandidate = errors.New("scope candidate data is invalid")
	// ErrInvalidResourceRef reports an invalid bounded resource reference.
	ErrInvalidResourceRef = errors.New("ResourceRef data is invalid")
)

// SessionID is an opaque application-generated UUIDv7 Session identifier.
type SessionID string

// Valid reports whether the identifier is canonical lowercase UUIDv7 text.
func (id SessionID) Valid() bool {
	return validUUIDv7(string(id))
}

// SessionStatus is the durable conversation-container state.
type SessionStatus string

const (
	SessionStatusActive   SessionStatus = "active"
	SessionStatusArchived SessionStatus = "archived"
)

// PrivacyMode fixes which safe Session data is eligible for persistence.
type PrivacyMode string

const (
	PrivacyModeStandard PrivacyMode = "standard"
	PrivacyModeMinimal  PrivacyMode = "minimal"
)

// ScopeCandidate is historic display metadata that has no live authority.
type ScopeCandidate struct {
	Context   string
	Namespace string
}

// Validate checks the bounded complete historic scope shape.
func (candidate ScopeCandidate) Validate() error {
	if !validBoundedText(candidate.Context, 1, maxContextBytes) ||
		!validBoundedText(candidate.Namespace, 1, maxNamespaceBytes) {
		return ErrInvalidScopeCandidate
	}
	return nil
}

// ResourceRef is a bounded project-owned resource identity, never an object body.
type ResourceRef struct {
	APIVersion      string
	Kind            string
	Namespace       string
	Name            string
	UID             string
	ResourceVersion string
}

// Validate checks structural and byte bounds for a safe resource identity.
func (reference ResourceRef) Validate() error {
	if !validBoundedText(reference.APIVersion, 1, maxAPIVersionBytes) ||
		!validBoundedText(reference.Kind, 1, maxKindBytes) ||
		!allowedDirectResource(reference.APIVersion, reference.Kind) ||
		!validBoundedText(reference.Namespace, 1, maxNamespaceBytes) ||
		!validBoundedText(reference.Name, 1, maxResourceNameBytes) ||
		!validOptionalText(reference.UID, maxResourceUIDBytes) ||
		!validOptionalText(reference.ResourceVersion, maxResourceVersionBytes) {
		return ErrInvalidResourceRef
	}
	return nil
}

// Session is durable conversation metadata, not live scope or execution state.
type Session struct {
	ID               SessionID
	Title            string
	Status           SessionStatus
	PrivacyMode      PrivacyMode
	LastScope        *ScopeCandidate
	SelectedResource *ResourceRef
	Summary          *string
	Version          int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Validate checks the complete Session persistence contract.
func (session Session) Validate() error {
	if !session.ID.Valid() ||
		(session.Status != SessionStatusActive && session.Status != SessionStatusArchived) ||
		(session.PrivacyMode != PrivacyModeStandard && session.PrivacyMode != PrivacyModeMinimal) ||
		!validBoundedText(session.Title, 0, maxSessionTitleBytes) ||
		session.Version < 1 ||
		!validDurableTime(session.CreatedAt) ||
		!validDurableTime(session.UpdatedAt) ||
		session.UpdatedAt.Before(session.CreatedAt) {
		return ErrInvalidSession
	}
	if session.LastScope != nil {
		if err := session.LastScope.Validate(); err != nil {
			return ErrInvalidSession
		}
	}
	if session.SelectedResource != nil {
		if session.LastScope == nil ||
			session.SelectedResource.Namespace != session.LastScope.Namespace ||
			session.SelectedResource.Validate() != nil {
			return ErrInvalidSession
		}
	}
	if session.Summary != nil && !validBoundedText(*session.Summary, 1, maxSessionSummaryBytes) {
		return ErrInvalidSession
	}
	if session.PrivacyMode == PrivacyModeMinimal &&
		(session.Title != "" || session.LastScope != nil || session.SelectedResource != nil || session.Summary != nil) {
		return ErrInvalidSession
	}
	return nil
}

func allowedDirectResource(apiVersion, kind string) bool {
	switch {
	case apiVersion == "v1" && (kind == "Pod" || kind == "Service"):
		return true
	case apiVersion == "apps/v1" && (kind == "Deployment" || kind == "ReplicaSet"):
		return true
	case apiVersion == "batch/v1" && kind == "Job":
		return true
	default:
		return false
	}
}

// HasResumableMetadata reports the status and privacy part of resume eligibility.
// A committed safe Message must still be present before a Session is resumable.
func (session Session) HasResumableMetadata() bool {
	return session.Status == SessionStatusActive && session.PrivacyMode == PrivacyModeStandard
}

func validUUIDv7(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '7' {
		return false
	}
	if value[19] != '8' && value[19] != '9' && value[19] != 'a' && value[19] != 'b' {
		return false
	}
	for index, current := range []byte(value) {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if current < '0' || current > '9' {
			if current < 'a' || current > 'f' {
				return false
			}
		}
	}
	return true
}

func validBoundedText(value string, minimumBytes, maximumBytes int) bool {
	return utf8.ValidString(value) && len(value) >= minimumBytes && len(value) <= maximumBytes
}

func validOptionalText(value string, maximumBytes int) bool {
	return value == "" || validBoundedText(value, 1, maximumBytes)
}

func validDurableTime(value time.Time) bool {
	return !value.IsZero() && value.UnixMilli() >= 0
}

func sameInstant(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func sameResourceRef(left, right *ResourceRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
