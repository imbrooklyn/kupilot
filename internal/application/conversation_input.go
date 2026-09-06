package application

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	MaxConversationInputItems          = 8
	MaxConversationInputItemBytes      = MaxQuestionBytes
	MaxConversationInputAggregateBytes = 256 * 1024
	MaxConversationInputPreviewItems   = 4
)

var (
	ErrConversationInputInvalid     = errors.New("conversation input is invalid")
	ErrConversationInputUnavailable = errors.New("conversation input is unavailable")
	ErrConversationInputLimit       = errors.New("conversation input limit was reached")
	ErrConversationInputConflict    = errors.New("conversation input state changed")
)

// ConversationInputState distinguishes local acceptance from the durable
// model-input commit barrier and from conservative recovery outcomes.
type ConversationInputState string

const (
	ConversationInputPending    ConversationInputState = "pending"
	ConversationInputCommitting ConversationInputState = "committing"
	ConversationInputCommitted  ConversationInputState = "committed"
	ConversationInputQueued     ConversationInputState = "queued"
	ConversationInputRejected   ConversationInputState = "rejected"
	ConversationInputRecovered  ConversationInputState = "recovered"
	ConversationInputUnknown    ConversationInputState = "unknown"
	conversationInputDraining   ConversationInputState = "draining"
)

func (state ConversationInputState) valid() bool {
	switch state {
	case ConversationInputPending, ConversationInputCommitting, ConversationInputCommitted,
		ConversationInputQueued, ConversationInputRejected, ConversationInputRecovered,
		ConversationInputUnknown, conversationInputDraining:
		return true
	default:
		return false
	}
}

func (state ConversationInputState) editable() bool {
	return state == ConversationInputQueued || state == ConversationInputRejected || state == ConversationInputRecovered
}

// SubmitSteerCommand binds ordinary input to the exact active run and both
// generations observed by delivery.
type SubmitSteerCommand struct {
	RunID                    domain.AgentRunID
	SessionID                domain.SessionID
	ExpectedScopeGeneration  int64
	ExpectedPolicyGeneration domain.PolicyGeneration
	Text                     string
}

func (command SubmitSteerCommand) Validate() error {
	if !command.RunID.Valid() || !command.SessionID.Valid() || command.ExpectedScopeGeneration < 1 ||
		!command.ExpectedPolicyGeneration.Valid() || !validUICommandText(command.Text, MaxConversationInputItemBytes) {
		return ErrConversationInputInvalid
	}
	return nil
}

// EnqueueFollowUpCommand has the same frozen authority binding as a steer, but
// its input is eligible only as a successor run after a clean commit.
type EnqueueFollowUpCommand = SubmitSteerCommand

// PopConversationInputCommand atomically removes the newest editable ordinary
// input from the current Session queue.
type PopConversationInputCommand struct {
	SessionID                domain.SessionID
	RunID                    domain.AgentRunID
	ExpectedScopeGeneration  int64
	ExpectedPolicyGeneration domain.PolicyGeneration
}

func (command PopConversationInputCommand) Validate() error {
	if !command.SessionID.Valid() || !command.RunID.Valid() || command.ExpectedScopeGeneration < 1 ||
		!command.ExpectedPolicyGeneration.Valid() {
		return ErrConversationInputInvalid
	}
	return nil
}

// ConversationInputProjection is the only content-bearing draft projection.
// It may be displayed in the bounded working area but is never status,
// history, export, audit, or persistence content.
type ConversationInputProjection struct {
	ItemID      domain.MessageID
	RunID       domain.AgentRunID
	State       ConversationInputState
	Text        string
	ContentHash string
	CreatedAt   time.Time
	Revision    int64
}

func (projection ConversationInputProjection) validate() bool {
	if !projection.ItemID.Valid() || !projection.RunID.Valid() || !projection.State.valid() ||
		!validCoordinatorTime(projection.CreatedAt) || projection.Revision < 1 {
		return false
	}
	return validUICommandText(projection.Text, MaxConversationInputItemBytes) &&
		projection.ContentHash == domain.MessageContentHash(projection.Text)
}

// ConversationInputStatus is content-free and safe for /status.
type ConversationInputStatus struct {
	Revision         int64
	Pending          int
	Committing       int
	Committed        int
	Queued           int
	Rejected         int
	Recovered        int
	Unknown          int
	Editable         int
	Items            int
	Bytes            int
	MaximumItems     int
	MaximumBytes     int
	MaximumItemBytes int
}

func (status ConversationInputStatus) valid() bool {
	if status.Revision < 0 || status.Pending < 0 || status.Committing < 0 || status.Committed < 0 || status.Queued < 0 ||
		status.Rejected < 0 || status.Recovered < 0 || status.Unknown < 0 || status.Editable < 0 ||
		status.Items < 0 || status.Bytes < 0 || status.MaximumItems != MaxConversationInputItems ||
		status.MaximumBytes != MaxConversationInputAggregateBytes || status.MaximumItemBytes != MaxConversationInputItemBytes {
		return false
	}
	visible := status.Pending + status.Committing + status.Committed + status.Queued + status.Rejected + status.Recovered + status.Unknown
	return status.Items == visible && status.Items <= status.MaximumItems && status.Bytes <= status.MaximumBytes &&
		status.Editable == status.Queued+status.Rejected+status.Recovered
}

type conversationInput struct {
	id              domain.MessageID
	sessionID       domain.SessionID
	runID           domain.AgentRunID
	scope           domain.ScopeSnapshot
	policy          domain.PolicyGeneration
	modelRole       domain.ModelRole
	modelProfile    string
	originHash      string
	consentRevision uint64
	budgetLimits    agent.RunBudgetLimits
	resource        *domain.ResourceRef
	text            string
	hash            string
	createdAt       time.Time
	revision        int64
	runSequence     int
	state           ConversationInputState
}

type conversationInputQueue struct {
	items      map[domain.MessageID]*conversationInput
	order      []domain.MessageID
	revision   int64
	totalBytes int
}

func (queue *conversationInputQueue) initialize() {
	if queue.items == nil {
		queue.items = make(map[domain.MessageID]*conversationInput)
	}
}

func (queue *conversationInputQueue) nextRevision() int64 {
	queue.revision++
	return queue.revision
}

func (queue *conversationInputQueue) remove(id domain.MessageID) {
	item := queue.items[id]
	if item == nil {
		return
	}
	queue.totalBytes -= len(item.text)
	delete(queue.items, id)
	for index, ordered := range queue.order {
		if ordered == id {
			queue.order = append(queue.order[:index], queue.order[index+1:]...)
			break
		}
	}
}

func (queue *conversationInputQueue) reset() {
	queue.items = make(map[domain.MessageID]*conversationInput)
	queue.order = nil
	queue.totalBytes = 0
	queue.nextRevision()
}

func (coordinator *Coordinator) currentConversationBindingLocked() (string, string, PrivacyBindingSnapshot, bool) {
	if coordinator.modelRuntime == nil || coordinator.privacy == nil {
		return "", "", PrivacyBindingSnapshot{}, false
	}
	profile, originHash := coordinator.currentAgentBindingLocked()
	privacy := coordinator.privacy.Snapshot()
	return profile, originHash, privacy, domain.ValidModelToken(profile, 128) &&
		validPrivacyDigest(originHash) && privacy.Role == domain.ModelRoleAgent && privacy.OriginHash == originHash &&
		privacy.Accepted && privacy.Revision > 0
}

func (coordinator *Coordinator) acceptConversationInput(
	ctx context.Context,
	command SubmitSteerCommand,
	state ConversationInputState,
) (ConversationInputProjection, error) {
	if coordinator == nil || ctx == nil || command.Validate() != nil ||
		(state != ConversationInputPending && state != ConversationInputQueued) {
		return ConversationInputProjection{}, ErrConversationInputInvalid
	}
	if err := ctx.Err(); err != nil {
		return ConversationInputProjection{}, err
	}
	processed, err := coordinator.questions.Process(command.Text, MaxConversationInputItemBytes)
	if err != nil || processed.Value == "" || len(processed.Value) > MaxConversationInputItemBytes {
		return ConversationInputProjection{}, ErrQuestionRejected
	}
	id, idErr := coordinator.identifiers.NewMessageID()
	createdAt := coordinator.now()
	if idErr != nil || !id.Valid() || !validCoordinatorTime(createdAt) {
		return ConversationInputProjection{}, ErrConversationInputUnavailable
	}

	coordinator.mu.Lock()
	active := coordinator.active
	if coordinator.closed || coordinator.persistenceDegraded || active == nil || active.terminal ||
		active.run.ID != command.RunID || active.run.SessionID != command.SessionID ||
		active.run.Scope.Generation != command.ExpectedScopeGeneration ||
		active.input.PolicyGeneration() != command.ExpectedPolicyGeneration ||
		!coordinator.runScopeCurrentLocked(active) ||
		!coordinator.runResourcePolicies.CurrentPolicyGeneration(ctx, active.input.PolicyGeneration()) {
		coordinator.mu.Unlock()
		return ConversationInputProjection{}, ErrConversationInputUnavailable
	}
	profile, originHash, privacy, bound := coordinator.currentConversationBindingLocked()
	if !bound {
		coordinator.mu.Unlock()
		return ConversationInputProjection{}, ErrConsentRequired
	}
	queue := &coordinator.conversationInputs
	queue.initialize()
	if len(queue.items) >= MaxConversationInputItems ||
		len(processed.Value) > MaxConversationInputAggregateBytes-queue.totalBytes {
		coordinator.mu.Unlock()
		return ConversationInputProjection{}, ErrConversationInputLimit
	}
	item := &conversationInput{
		id: id, sessionID: command.SessionID, runID: command.RunID, scope: active.run.Scope,
		policy: command.ExpectedPolicyGeneration, modelRole: privacy.Role, modelProfile: profile,
		originHash: originHash, consentRevision: privacy.Revision, budgetLimits: active.input.BudgetLimits(),
		resource: cloneResource(active.run.Resource),
		text:     processed.Value, hash: domain.MessageContentHash(processed.Value), createdAt: createdAt, state: state,
	}
	item.revision = queue.nextRevision()
	queue.items[item.id] = item
	queue.order = append(queue.order, item.id)
	queue.totalBytes += len(item.text)
	projection := projectConversationInput(item)
	event := coordinator.conversationInputEventLocked(item)
	coordinator.mu.Unlock()
	// The typed command outcome is the acceptance acknowledgement. This
	// best-effort working-area projection cannot revoke an accepted item or race
	// a simultaneous Eino boundary into a false rejection.
	_ = coordinator.uiEvents.PublishUIEvent(ctx, event)
	return projection, nil
}

// SubmitSteer accepts ordinary chat for the next model boundary of one exact
// active run. Acceptance does not persist or commit the input.
func (coordinator *Coordinator) SubmitSteer(ctx context.Context, command SubmitSteerCommand) (ConversationInputProjection, error) {
	return coordinator.acceptConversationInput(ctx, command, ConversationInputPending)
}

// EnqueueFollowUp appends one ordinary successor input to the process-local
// FIFO queue. It is not model history until a successor run durably starts.
func (coordinator *Coordinator) EnqueueFollowUp(ctx context.Context, command EnqueueFollowUpCommand) (ConversationInputProjection, error) {
	return coordinator.acceptConversationInput(ctx, command, ConversationInputQueued)
}

// PopLastConversationInput atomically wins against auto-drain or returns a
// conflict. Pending, committing, committed, draining, and unknown input is not
// editable.
func (coordinator *Coordinator) PopLastConversationInput(
	ctx context.Context,
	command PopConversationInputCommand,
) (ConversationInputProjection, error) {
	if coordinator == nil || ctx == nil || command.Validate() != nil {
		return ConversationInputProjection{}, ErrConversationInputInvalid
	}
	if err := ctx.Err(); err != nil {
		return ConversationInputProjection{}, err
	}
	coordinator.mu.Lock()
	queue := &coordinator.conversationInputs
	active := coordinator.active
	if active == nil || active.terminal || active.run.ID != command.RunID ||
		active.run.SessionID != command.SessionID || active.run.Scope.Generation != command.ExpectedScopeGeneration ||
		active.input.PolicyGeneration() != command.ExpectedPolicyGeneration {
		coordinator.mu.Unlock()
		return ConversationInputProjection{}, ErrConversationInputUnavailable
	}
	for index := len(queue.order) - 1; index >= 0; index-- {
		item := queue.items[queue.order[index]]
		if item == nil || item.sessionID != command.SessionID || !item.state.editable() {
			continue
		}
		projection := projectConversationInput(item)
		queue.remove(item.id)
		queue.nextRevision()
		event := coordinator.conversationInputEventLocked(nil)
		coordinator.mu.Unlock()
		// The typed command result returns the atomically removed content. A
		// best-effort status projection cannot undo the edit winner after the
		// queue lock is released.
		_ = coordinator.uiEvents.PublishUIEvent(ctx, event)
		return projection, nil
	}
	coordinator.mu.Unlock()
	return ConversationInputProjection{}, ErrConversationInputConflict
}

func (coordinator *Coordinator) conversationInputStatusLocked() ConversationInputStatus {
	queue := &coordinator.conversationInputs
	status := ConversationInputStatus{
		Revision: queue.revision, Bytes: queue.totalBytes,
		MaximumItems: MaxConversationInputItems, MaximumBytes: MaxConversationInputAggregateBytes,
		MaximumItemBytes: MaxConversationInputItemBytes,
	}
	for _, item := range queue.items {
		if item == nil {
			continue
		}
		status.Items++
		switch item.state {
		case ConversationInputPending:
			status.Pending++
		case ConversationInputCommitting:
			status.Committing++
		case ConversationInputCommitted:
			status.Committed++
		case ConversationInputQueued:
			status.Queued++
		case conversationInputDraining:
			// Delivery continues to see the item as queued until the successor
			// RunStarted event commits it as that run's initial input.
			status.Queued++
		case ConversationInputRejected:
			status.Rejected++
		case ConversationInputRecovered:
			status.Recovered++
		case ConversationInputUnknown:
			status.Unknown++
		}
		if item.state.editable() {
			status.Editable++
		}
	}
	return status
}

func projectConversationInput(item *conversationInput) ConversationInputProjection {
	if item == nil {
		return ConversationInputProjection{}
	}
	state := item.state
	if state == conversationInputDraining {
		state = ConversationInputQueued
	}
	return ConversationInputProjection{
		ItemID: item.id, RunID: item.runID, State: state,
		Text: item.text, ContentHash: item.hash, CreatedAt: item.createdAt, Revision: item.revision,
	}
}

func (coordinator *Coordinator) conversationInputEventLocked(changed *conversationInput) UIEvent {
	queue := &coordinator.conversationInputs
	previews := make([]ConversationInputProjection, 0, MaxConversationInputPreviewItems)
	for _, id := range queue.order {
		item := queue.items[id]
		if item == nil || item.state == ConversationInputCommitted {
			continue
		}
		previews = append(previews, projectConversationInput(item))
		if len(previews) == MaxConversationInputPreviewItems {
			break
		}
	}
	status := coordinator.conversationInputStatusLocked()
	input := UIConversationInputEvent{Revision: queue.revision, Status: status, Preview: previews}
	if changed != nil {
		value := projectConversationInput(changed)
		input.Changed = &value
	}
	runID := domain.AgentRunID("")
	scopeGeneration := int64(0)
	policyGeneration := domain.PolicyGeneration(0)
	if changed != nil {
		runID, scopeGeneration, policyGeneration = changed.runID, changed.scope.Generation, changed.policy
	} else if coordinator.active != nil {
		runID, scopeGeneration, policyGeneration = coordinator.active.run.ID,
			coordinator.active.run.Scope.Generation, coordinator.active.input.PolicyGeneration()
	}
	return UIEvent{
		Kind: UIEventConversationInput, RunID: runID, ScopeGeneration: scopeGeneration,
		PolicyGeneration: policyGeneration, ConversationInput: &input,
	}
}

func (coordinator *Coordinator) conversationInputBindingCurrentLocked(item *conversationInput, state *activeRun) bool {
	if item == nil || state == nil || coordinator.active != state || state.terminal ||
		item.sessionID != state.run.SessionID || item.runID != state.run.ID || item.scope != state.run.Scope ||
		item.policy != state.input.PolicyGeneration() || item.budgetLimits != state.input.BudgetLimits() ||
		!coordinator.runScopeCurrentLocked(state) ||
		!coordinator.runResourcePolicies.CurrentPolicyGeneration(context.Background(), item.policy) {
		return false
	}
	profile, originHash, privacy, ok := coordinator.currentConversationBindingLocked()
	return ok && item.modelRole == privacy.Role && item.modelProfile == profile && item.originHash == originHash &&
		item.consentRevision == privacy.Revision
}

// ClaimSteer implements agent.RunSteeringBridge at an exact Eino model
// boundary. At most one FIFO pending steer is claimed per boundary.
func (coordinator *Coordinator) ClaimSteer(
	ctx context.Context,
	boundary agent.SteerBoundary,
) (agent.SteerClaim, bool, error) {
	if coordinator == nil || ctx == nil || boundary.Validate() != nil {
		return agent.SteerClaim{}, false, ErrConversationInputInvalid
	}
	if err := ctx.Err(); err != nil {
		return agent.SteerClaim{}, false, err
	}
	coordinator.mu.Lock()
	state := coordinator.active
	if state == nil || state.run.ID != boundary.RunID || state.run.SessionID != boundary.SessionID ||
		state.run.Scope.Generation != boundary.ScopeGeneration || state.input.PolicyGeneration() != boundary.PolicyGeneration ||
		state.terminal {
		coordinator.mu.Unlock()
		return agent.SteerClaim{}, false, ErrConversationInputUnavailable
	}
	item := queueFirstPending(&coordinator.conversationInputs, boundary.RunID)
	if item == nil {
		coordinator.mu.Unlock()
		return agent.SteerClaim{}, false, nil
	}
	if !coordinator.conversationInputBindingCurrentLocked(item, state) {
		item.state = ConversationInputRecovered
		item.revision = coordinator.conversationInputs.nextRevision()
		event := coordinator.conversationInputEventLocked(item)
		coordinator.mu.Unlock()
		_ = coordinator.uiEvents.PublishUIEvent(context.WithoutCancel(ctx), event)
		return agent.SteerClaim{}, false, ErrConversationInputUnavailable
	}
	sequence := 1 + len(state.committedInputs)
	if sequence > domain.MaxCommittedSteerInputs {
		item.state = ConversationInputRecovered
		item.revision = coordinator.conversationInputs.nextRevision()
		event := coordinator.conversationInputEventLocked(item)
		coordinator.mu.Unlock()
		_ = coordinator.uiEvents.PublishUIEvent(context.WithoutCancel(ctx), event)
		return agent.SteerClaim{}, false, ErrConversationInputLimit
	}
	item.state = ConversationInputCommitting
	item.runSequence = sequence
	item.revision = coordinator.conversationInputs.nextRevision()
	claim := agent.SteerClaim{
		ItemID: item.id, RunID: item.runID, SessionID: item.sessionID,
		ScopeGeneration: item.scope.Generation, PolicyGeneration: item.policy,
		RunSequence: sequence, Content: item.text, ContentHash: item.hash,
	}
	event := coordinator.conversationInputEventLocked(item)
	coordinator.mu.Unlock()
	if claim.Validate() != nil || coordinator.uiEvents.PublishUIEvent(ctx, event) != nil {
		coordinator.ResolveSteer(context.WithoutCancel(ctx), claim, agent.SteerResolutionRejected)
		return agent.SteerClaim{}, false, ErrConversationInputUnavailable
	}
	return claim, true, nil
}

func queueFirstPending(queue *conversationInputQueue, runID domain.AgentRunID) *conversationInput {
	if queue == nil {
		return nil
	}
	for _, id := range queue.order {
		item := queue.items[id]
		if item != nil && item.runID == runID && item.state == ConversationInputPending {
			return item
		}
	}
	return nil
}

// CommitSteer persists and publishes one claimed steer before the adapter may
// enter real model I/O.
func (coordinator *Coordinator) CommitSteer(ctx context.Context, claim agent.SteerClaim) error {
	if coordinator == nil || ctx == nil || claim.Validate() != nil {
		return ErrConversationInputInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	authorized, privacyErr := coordinator.privacy.AuthorizeModel(ctx)
	if privacyErr != nil || !authorized {
		if privacyErr != nil {
			coordinator.privacy.FailClosed()
		}
		coordinator.ResolveSteer(context.WithoutCancel(ctx), claim, agent.SteerResolutionRecovered)
		if privacyErr != nil {
			return ErrPersistenceUnavailable
		}
		return ErrConsentRequired
	}
	createdAt := coordinator.now()
	coordinator.mu.Lock()
	state := coordinator.active
	item := coordinator.conversationInputs.items[claim.ItemID]
	if !validCoordinatorTime(createdAt) || item == nil || item.state != ConversationInputCommitting ||
		item.runSequence != claim.RunSequence || item.text != claim.Content || item.hash != claim.ContentHash ||
		state == nil || !coordinator.conversationInputBindingCurrentLocked(item, state) ||
		claim.RunSequence != 1+len(state.committedInputs) {
		coordinator.mu.Unlock()
		coordinator.ResolveSteer(context.WithoutCancel(ctx), claim, agent.SteerResolutionRecovered)
		return ErrConversationInputUnavailable
	}
	message := domain.Message{
		ID: item.id, SessionID: item.sessionID, RunID: &item.runID, RunSequence: intPointer(item.runSequence),
		Role: domain.MessageRoleUser, Content: item.text, Format: domain.MessageFormatPlain,
		Status: domain.MessageStatusCommitted, Scope: scopeSnapshotPointer(item.scope), Resource: cloneResource(item.resource),
		Hash: item.hash, CreatedAt: createdAt,
	}
	minimal := state.minimalPersistence
	coordinator.mu.Unlock()
	if message.Validate() != nil {
		coordinator.ResolveSteer(context.WithoutCancel(ctx), claim, agent.SteerResolutionRejected)
		return ErrConversationInputInvalid
	}
	if !minimal {
		if coordinator.runInputs == nil || coordinator.persist(ctx, func(operationContext context.Context) error {
			return coordinator.runInputs.AppendRunInput(operationContext, message)
		}) != nil {
			coordinator.ResolveSteer(context.WithoutCancel(ctx), claim, agent.SteerResolutionRecovered)
			if state != nil {
				_ = coordinator.markRunPersistenceDegraded(context.WithoutCancel(ctx), state)
			}
			return ErrPersistenceUnavailable
		}
	}

	coordinator.mu.Lock()
	state = coordinator.active
	item = coordinator.conversationInputs.items[claim.ItemID]
	current := item != nil && item.state == ConversationInputCommitting && state != nil &&
		coordinator.conversationInputBindingCurrentLocked(item, state) && claim.RunSequence == 1+len(state.committedInputs)
	if !current && minimal {
		coordinator.mu.Unlock()
		coordinator.ResolveSteer(context.WithoutCancel(ctx), claim, agent.SteerResolutionRecovered)
		return ErrConversationInputUnavailable
	}
	// In standard mode the Message append above is irreversible even when a
	// concurrent invalidation wins before this lock is reacquired. Preserve
	// that known committed fact and block model I/O; `unknown` is reserved for
	// a failure after the wrapped endpoint was actually entered.
	if !current && (item == nil || state == nil || state.run.ID != claim.RunID ||
		state.run.SessionID != claim.SessionID || item.runID != claim.RunID ||
		item.sessionID != claim.SessionID || item.runSequence != claim.RunSequence ||
		item.text != claim.Content || item.hash != claim.ContentHash ||
		(item.state != ConversationInputCommitting && item.state != ConversationInputRecovered) ||
		claim.RunSequence != 1+len(state.committedInputs)) {
		coordinator.mu.Unlock()
		return ErrConversationInputUnavailable
	}
	state.committedInputs = append(state.committedInputs, agent.ConversationTurn{
		MessageID: item.id, RunID: item.runID, RunSequence: item.runSequence,
		Role: domain.MessageRoleUser, Content: item.text, ContentHash: item.hash,
	})
	item.state = ConversationInputCommitted
	item.revision = coordinator.conversationInputs.nextRevision()
	event := coordinator.conversationInputEventLocked(item)
	coordinator.mu.Unlock()
	if err := coordinator.uiEvents.PublishUIEvent(ctx, event); err != nil {
		// Persistence already committed the user Message, so this known
		// pre-model delivery failure cannot be called rejected or unknown. The
		// adapter receives the error and must not enter model I/O.
		return ErrConversationInputUnavailable
	}
	if !current {
		return ErrConversationInputUnavailable
	}
	return nil
}

// ResolveSteer conservatively closes a claim without retrying or selecting a
// new run. Rejected and recovered inputs are editable; unknown inputs are not.
func (coordinator *Coordinator) ResolveSteer(
	ctx context.Context,
	claim agent.SteerClaim,
	resolution agent.SteerResolution,
) {
	if coordinator == nil || ctx == nil || claim.Validate() != nil || !resolution.Valid() {
		return
	}
	coordinator.mu.Lock()
	item := coordinator.conversationInputs.items[claim.ItemID]
	if item == nil || item.runID != claim.RunID || item.sessionID != claim.SessionID ||
		item.runSequence != claim.RunSequence {
		coordinator.mu.Unlock()
		return
	}
	next := ConversationInputState(resolution)
	if item.state == ConversationInputCommitting || resolution == agent.SteerResolutionUnknown && item.state == ConversationInputCommitted {
		item.state = next
		item.revision = coordinator.conversationInputs.nextRevision()
	} else {
		coordinator.mu.Unlock()
		return
	}
	event := coordinator.conversationInputEventLocked(item)
	coordinator.mu.Unlock()
	_ = coordinator.uiEvents.PublishUIEvent(ctx, event)
}

func (coordinator *Coordinator) finishConversationInputRunLocked(
	state *activeRun,
	clean bool,
) (*UIEvent, *conversationInput) {
	if coordinator == nil || state == nil {
		return nil, nil
	}
	queue := &coordinator.conversationInputs
	queue.initialize()
	safeToDrain := clean
	if safeToDrain {
		for _, item := range queue.items {
			if item == nil || item.sessionID != state.run.SessionID || item.runID != state.run.ID {
				continue
			}
			switch item.state {
			case ConversationInputCommitting, ConversationInputRejected, ConversationInputRecovered, ConversationInputUnknown:
				safeToDrain = false
			}
		}
	}
	changed := false
	for _, id := range append([]domain.MessageID(nil), queue.order...) {
		item := queue.items[id]
		if item == nil || item.sessionID != state.run.SessionID {
			continue
		}
		if !safeToDrain && item.state == ConversationInputQueued {
			// A failed link stops the entire successor chain. Older queued
			// items must not become eligible again after some unrelated later
			// success in the same process.
			item.state = ConversationInputRecovered
			item.revision = queue.nextRevision()
			changed = true
			continue
		}
		if item.runID != state.run.ID {
			continue
		}
		switch item.state {
		case ConversationInputPending:
			if safeToDrain {
				item.state = ConversationInputQueued
			} else {
				item.state = ConversationInputRecovered
			}
			item.revision = queue.nextRevision()
			changed = true
		case ConversationInputCommitting:
			item.state = ConversationInputRecovered
			item.revision = queue.nextRevision()
			changed = true
		case ConversationInputQueued:
			if !safeToDrain {
				item.state = ConversationInputRecovered
				item.revision = queue.nextRevision()
				changed = true
			}
		case ConversationInputCommitted:
			queue.remove(item.id)
			queue.nextRevision()
			changed = true
		}
	}
	var successor *conversationInput
	if safeToDrain {
		for _, id := range queue.order {
			item := queue.items[id]
			if item == nil || item.state != ConversationInputQueued || item.sessionID != state.run.SessionID {
				continue
			}
			if !coordinator.successorBindingCurrentLocked(item) {
				item.state = ConversationInputRecovered
				item.revision = queue.nextRevision()
				changed = true
				continue
			}
			item.state = conversationInputDraining
			item.revision = queue.nextRevision()
			changed = true
			successor = item
			break
		}
	}
	if !changed {
		return nil, successor
	}
	event := coordinator.conversationInputEventLocked(nil)
	return &event, successor
}

func (coordinator *Coordinator) successorBindingCurrentLocked(item *conversationInput) bool {
	if item == nil || coordinator.currentSession == nil || coordinator.currentSession.ID != item.sessionID ||
		!coordinator.runResourcePolicies.CurrentPolicyGeneration(context.Background(), item.policy) {
		return false
	}
	current, ok := coordinator.scope.CurrentScope()
	if !ok || current.Snapshot() != item.scope {
		return false
	}
	profile, originHash, privacy, ok := coordinator.currentConversationBindingLocked()
	return ok && item.modelRole == privacy.Role && item.modelProfile == profile && item.originHash == originHash &&
		item.consentRevision == privacy.Revision && item.budgetLimits == coordinator.budgetLimits
}

func (coordinator *Coordinator) startQueuedSuccessor(ctx context.Context, item *conversationInput) {
	if coordinator == nil || ctx == nil || item == nil {
		return
	}
	coordinator.mu.Lock()
	current := coordinator.conversationInputs.items[item.id]
	if current != item || current.state != conversationInputDraining {
		coordinator.mu.Unlock()
		return
	}
	index := coordinator.conversationInputs.index(item.id)
	coordinator.conversationInputs.remove(item.id)
	item.revision = coordinator.conversationInputs.nextRevision()
	event := coordinator.conversationInputEventLocked(item)
	coordinator.mu.Unlock()
	if coordinator.uiEvents.PublishUIEvent(context.WithoutCancel(ctx), event) != nil {
		coordinator.recoverRemovedSuccessor(ctx, item, index)
		return
	}
	_, err := coordinator.startRun(ctx, StartRunCommand{
		SessionID: item.sessionID, Question: item.text, Resource: cloneResource(item.resource),
	}, item.id)
	if err != nil {
		coordinator.recoverRemovedSuccessor(ctx, item, index)
	}
}

func (queue *conversationInputQueue) index(id domain.MessageID) int {
	for index, ordered := range queue.order {
		if ordered == id {
			return index
		}
	}
	return len(queue.order)
}

func (queue *conversationInputQueue) insert(index int, item *conversationInput) {
	if item == nil {
		return
	}
	queue.initialize()
	index = max(0, min(index, len(queue.order)))
	queue.order = append(queue.order, "")
	copy(queue.order[index+1:], queue.order[index:])
	queue.order[index] = item.id
	queue.items[item.id] = item
	queue.totalBytes += len(item.text)
}

func (coordinator *Coordinator) recoverRemovedSuccessor(ctx context.Context, item *conversationInput, index int) {
	coordinator.mu.Lock()
	queue := &coordinator.conversationInputs
	if coordinator.closed || coordinator.currentSession == nil || coordinator.currentSession.ID != item.sessionID ||
		queue.items[item.id] != nil {
		coordinator.mu.Unlock()
		return
	}
	item.state = ConversationInputRecovered
	item.revision = queue.nextRevision()
	queue.insert(index, item)
	event := coordinator.conversationInputEventLocked(item)
	coordinator.mu.Unlock()
	_ = coordinator.uiEvents.PublishUIEvent(context.WithoutCancel(ctx), event)
}

// InvalidateScope implements the local conversation-input invalidation hook.
// ScopeManager has already advanced its generation and removed current
// authority; it owns cancellation immediately after this returns.
func (coordinator *Coordinator) InvalidateScope(_ int64) error {
	if coordinator == nil {
		return ErrConversationInputUnavailable
	}
	coordinator.mu.Lock()
	changed := coordinator.invalidateConversationInputsLocked()
	var event *UIEvent
	if changed && coordinator.active != nil {
		value := coordinator.conversationInputEventLocked(nil)
		event = &value
	}
	coordinator.mu.Unlock()
	if event != nil {
		_ = coordinator.uiEvents.PublishUIEvent(context.Background(), *event)
	}
	return nil
}

func (coordinator *Coordinator) invalidateConversationInputs() *UIEvent {
	coordinator.mu.Lock()
	changed := coordinator.invalidateConversationInputsLocked()
	if !changed || coordinator.active == nil {
		coordinator.mu.Unlock()
		return nil
	}
	event := coordinator.conversationInputEventLocked(nil)
	coordinator.mu.Unlock()
	return &event
}

func (coordinator *Coordinator) invalidateConversationInputsLocked() bool {
	queue := &coordinator.conversationInputs
	changed := false
	for _, item := range queue.items {
		if item == nil {
			continue
		}
		switch item.state {
		case ConversationInputPending, ConversationInputCommitting, ConversationInputQueued, conversationInputDraining:
			item.state = ConversationInputRecovered
			item.revision = queue.nextRevision()
			changed = true
		}
	}
	return changed
}

func (coordinator *Coordinator) publishConversationInvalidation(ctx context.Context) {
	event := coordinator.invalidateConversationInputs()
	if event != nil {
		_ = coordinator.uiEvents.PublishUIEvent(context.WithoutCancel(ctx), *event)
	}
}
