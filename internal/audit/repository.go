// Package audit owns the consumer contracts for structured local audit and maintenance.
package audit

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	// MaxPageSize bounds one audit-history query.
	MaxPageSize = 100
	// MaxCleanupBatchSize bounds each category in one retention transaction.
	MaxCleanupBatchSize = 100
	// DefaultOperationalDetailRetentionDays is the accepted safe-detail default.
	DefaultOperationalDetailRetentionDays = 30
	// ReadAuditRetentionDays is the fixed lifecycle-audit period.
	ReadAuditRetentionDays = 90
	// WriteAuditRetentionDays is the fixed future write-audit period.
	WriteAuditRetentionDays = 180
)

var (
	ErrInvalidRepositoryRequest = errors.New("audit repository request is invalid")
	ErrAuditEventNotFound       = errors.New("the requested AuditEvent was not found")
	ErrAuditEventNotEligible    = errors.New("the AuditEvent is not eligible for durable storage")
	ErrSettingNotFound          = errors.New("the requested setting was not found")
	ErrSettingConflict          = errors.New("the setting changed before the requested update")
)

// EventAppender owns immutable structured AuditEvent writes.
type EventAppender interface {
	Append(context.Context, domain.AuditEvent) error
}

// EventReader owns exact and bounded Session-linked audit reads.
type EventReader interface {
	GetByID(context.Context, domain.AuditEventID) (domain.AuditEvent, error)
	ListBySession(context.Context, PageRequest) (Page, error)
}

// SettingStore owns the small typed non-secret settings lifecycle.
type SettingStore interface {
	Put(context.Context, domain.Setting) error
	Get(context.Context, domain.SettingKey) (domain.Setting, error)
	Delete(context.Context, domain.SettingKey) error
}

// RetentionCleaner owns one caller-scheduled bounded cleanup transaction.
type RetentionCleaner interface {
	Cleanup(context.Context, CleanupRequest) (CleanupResult, error)
}

// Cursor is an exclusive descending audit-history boundary.
type Cursor struct {
	OccurredAt time.Time
	ID         domain.AuditEventID
}

// PageRequest selects one bounded Session-owned audit page.
type PageRequest struct {
	SessionID domain.SessionID
	Limit     int
	Before    *Cursor
}

// Validate checks the required Session, page limit, and complete cursor.
func (request PageRequest) Validate() error {
	if !request.SessionID.Valid() || request.Limit < 1 || request.Limit > MaxPageSize {
		return ErrInvalidRepositoryRequest
	}
	if request.Before != nil && (!request.Before.ID.Valid() || request.Before.OccurredAt.IsZero() || request.Before.OccurredAt.UnixMilli() < 0 ||
		request.Before.OccurredAt.Location() != time.UTC || request.Before.OccurredAt.Nanosecond()%int(time.Millisecond) != 0) {
		return ErrInvalidRepositoryRequest
	}
	return nil
}

// Page contains one stable descending AuditEvent page.
type Page struct {
	Events []domain.AuditEvent
	Next   *Cursor
}

// CleanupRequest carries an injected UTC instant and bounded detail policy.
type CleanupRequest struct {
	Now                            time.Time
	OperationalDetailRetentionDays int
	BatchSize                      int
}

// Validate checks UTC-millisecond policy input without reading a clock.
func (request CleanupRequest) Validate() error {
	probe := domain.Setting{
		Key:           domain.SettingOperationalDetailRetentionDays,
		IntegerValue:  int64(request.OperationalDetailRetentionDays),
		SchemaVersion: 1,
		UpdatedAt:     request.Now,
	}
	if request.Now.IsZero() || request.Now.UnixMilli() < 0 || request.Now.Nanosecond()%int(time.Millisecond) != 0 ||
		request.BatchSize < 1 || request.BatchSize > MaxCleanupBatchSize || probe.Validate() != nil {
		return ErrInvalidRepositoryRequest
	}
	return nil
}

// DetailCutoff returns the inclusive expiration boundary.
func (request CleanupRequest) DetailCutoff() time.Time {
	return request.Now.UTC().Add(-time.Duration(request.OperationalDetailRetentionDays) * 24 * time.Hour)
}

// ReadAuditCutoff returns the inclusive fixed lifecycle-audit boundary.
func (request CleanupRequest) ReadAuditCutoff() time.Time {
	return request.Now.UTC().Add(-ReadAuditRetentionDays * 24 * time.Hour)
}

// WriteAuditCutoff returns the inclusive fixed future-write audit boundary.
func (request CleanupRequest) WriteAuditCutoff() time.Time {
	return request.Now.UTC().Add(-WriteAuditRetentionDays * 24 * time.Hour)
}

// CleanupResult reports only rows committed by the completed batch.
type CleanupResult struct {
	EvidenceItems    int64
	ToolInvocations  int64
	ModelRequests    int64
	ReadAuditEvents  int64
	WriteAuditEvents int64
	ApprovalRecords  int64
	MinimalSessions  int64
	More             bool
}
