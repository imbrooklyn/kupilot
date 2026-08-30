package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

var ErrIdentifierUnavailable = errors.New("an application identifier is unavailable")

// SessionPersistence owns Session creation plus the current lifecycle controls.
type SessionPersistence interface {
	CreateWithAudit(context.Context, domain.Session, domain.AuditEvent) error
	SessionLifecyclePersistence
}

// RunPersistence owns the three atomic lifecycle transaction intents. It
// exposes no resume query or generic repository method.
type RunPersistence interface {
	BeginWithAudit(context.Context, domain.Message, domain.AgentRun, domain.AuditEvent) error
	FinishWithAudit(context.Context, domain.AgentRun, domain.AuditEvent) error
	CompleteWithAudit(context.Context, domain.Diagnosis, domain.Message, domain.AgentRun, domain.AuditEvent) error
}

// ToolEvidencePersistence atomically stores one terminal ToolInvocation and
// its accepted same-run Evidence and terminal audit record.
type ToolEvidencePersistence interface {
	SaveWithAudit(context.Context, domain.ToolInvocation, []domain.Evidence, domain.AuditEvent) error
}

// AuditPersistence stores fixed structured audit metadata without payloads.
type AuditPersistence interface {
	Append(context.Context, domain.AuditEvent) error
}

// ActiveScope owns the exact scope snapshot and cancellation binding for the
// sole active AgentRun.
type ActiveScope interface {
	CurrentScope() (domain.ClusterScope, bool)
	BindRun(domain.ClusterScope, domain.AgentRunID, context.CancelFunc) error
	UnbindRun(domain.AgentRunID)
}

// ApplicationIdentifierSource supplies the three durable IDs created directly
// by Application use cases.
type ApplicationIdentifierSource interface {
	NewSessionID() (domain.SessionID, error)
	NewMessageID() (domain.MessageID, error)
	NewAgentRunID() (domain.AgentRunID, error)
}

// AuditIdentifierSource supplies IDs for structured audit records.
type AuditIdentifierSource interface {
	NewAuditEventID() (domain.AuditEventID, error)
}

// QuestionProcessor applies the local normalization, sensitive-value policy,
// and byte ceiling before a question reaches persistence or the model.
type QuestionProcessor interface {
	Process(string, int) (security.TextResult, error)
}

// UIEventSink synchronously accepts one neutral ordered UI projection.
type UIEventSink interface {
	PublishUIEvent(context.Context, UIEvent) error
}

// UIEventSinkFunc adapts one function to the delivery-neutral UI sink.
type UIEventSinkFunc func(context.Context, UIEvent) error

func (function UIEventSinkFunc) PublishUIEvent(ctx context.Context, event UIEvent) error {
	return function(ctx, event)
}

// RunObserver receives only fixed lifecycle metadata suitable for a bounded
// logger. It never receives question, model, Tool, Evidence, or Diagnosis text.
type RunObserver interface {
	ObserveRun(context.Context, RunObservation)
}

// RunObserverFunc adapts one function to the safe lifecycle observer.
type RunObserverFunc func(context.Context, RunObservation)

func (function RunObserverFunc) ObserveRun(ctx context.Context, observation RunObservation) {
	function(ctx, observation)
}

// IdentifierGenerator creates application-owned UUIDv7 identifiers from the
// current UTC Unix millisecond and cryptographic randomness.
type IdentifierGenerator struct {
	mu  sync.Mutex
	now func() time.Time
}

// NewIdentifierGenerator constructs the process-local durable ID source.
func NewIdentifierGenerator(now func() time.Time) (*IdentifierGenerator, error) {
	if now == nil || !validCoordinatorTime(now()) {
		return nil, ErrIdentifierUnavailable
	}
	return &IdentifierGenerator{now: now}, nil
}

func (generator *IdentifierGenerator) NewSessionID() (domain.SessionID, error) {
	value, err := generator.next()
	return domain.SessionID(value), err
}

func (generator *IdentifierGenerator) NewMessageID() (domain.MessageID, error) {
	value, err := generator.next()
	return domain.MessageID(value), err
}

func (generator *IdentifierGenerator) NewAgentRunID() (domain.AgentRunID, error) {
	value, err := generator.next()
	return domain.AgentRunID(value), err
}

func (generator *IdentifierGenerator) NewAuditEventID() (domain.AuditEventID, error) {
	value, err := generator.next()
	return domain.AuditEventID(value), err
}

func (generator *IdentifierGenerator) NewApprovalID() (domain.ApprovalID, error) {
	value, err := generator.next()
	return domain.ApprovalID(value), err
}

// NewNonce creates one opaque approval proof from cryptographic randomness.
// It is never formatted, logged, persisted in plaintext, or reused as an ID.
func (generator *IdentifierGenerator) NewNonce(ctx context.Context) (domain.ApprovalNonce, error) {
	if generator == nil || ctx == nil || ctx.Err() != nil {
		return domain.ApprovalNonce{}, ErrIdentifierUnavailable
	}
	value := make([]byte, domain.ApprovalNonceBytes)
	if _, err := rand.Read(value); err != nil || ctx.Err() != nil {
		return domain.ApprovalNonce{}, ErrIdentifierUnavailable
	}
	nonce, err := domain.NewApprovalNonce(value)
	for index := range value {
		value[index] = 0
	}
	if err != nil {
		return domain.ApprovalNonce{}, ErrIdentifierUnavailable
	}
	return nonce, nil
}

func (generator *IdentifierGenerator) NewModelRequestID() (domain.ModelRequestID, error) {
	value, err := generator.next()
	return domain.ModelRequestID(value), err
}

func (generator *IdentifierGenerator) NewToolInvocationID() (domain.ToolInvocationID, error) {
	value, err := generator.next()
	return domain.ToolInvocationID(value), err
}

func (generator *IdentifierGenerator) NewDiagnosisID() (domain.DiagnosisID, error) {
	value, err := generator.next()
	return domain.DiagnosisID(value), err
}

func (generator *IdentifierGenerator) NewEvidenceID() (domain.EvidenceID, error) {
	value, err := generator.next()
	return domain.EvidenceID(value), err
}

func (generator *IdentifierGenerator) next() (string, error) {
	if generator == nil || generator.now == nil {
		return "", ErrIdentifierUnavailable
	}
	generator.mu.Lock()
	defer generator.mu.Unlock()
	now := generator.now()
	if !validCoordinatorTime(now) {
		return "", ErrIdentifierUnavailable
	}
	milliseconds := now.UnixMilli()
	if milliseconds < 0 || uint64(milliseconds) > 0xffffffffffff {
		return "", ErrIdentifierUnavailable
	}
	var value [16]byte
	if _, err := rand.Read(value[6:]); err != nil {
		return "", ErrIdentifierUnavailable
	}
	timestamp := uint64(milliseconds)
	for index := 5; index >= 0; index-- {
		value[index] = byte(timestamp)
		timestamp >>= 8
	}
	value[6] = value[6]&0x0f | 0x70
	value[8] = value[8]&0x3f | 0x80
	var encoded [36]byte
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded[:]), nil
}

// Current implements the exact freshness port used independently by the Agent
// runtime and fixed Tool handlers.
func (manager *ScopeManager) Current(ctx context.Context, scope domain.ClusterScope) bool {
	if manager == nil || ctx == nil || ctx.Err() != nil || scope.Validate() != nil {
		return false
	}
	if !manager.scopeCurrent(scope) {
		return false
	}
	return ctx.Err() == nil
}
