package application

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	modelContextPageSize = 100
	maxModelContextPages = (domain.MaxSessionContextMessages + modelContextPageSize - 1) / modelContextPageSize
)

var ErrModelContextUnavailable = errors.New("safe Session model context is unavailable")

// ModelContextCursor is an exclusive ascending keyset boundary.
type ModelContextCursor struct {
	RunStartedAt time.Time
	RunID        domain.AgentRunID
	RunSequence  int
	ID           domain.MessageID
}

// ModelContextPageRequest selects only messages from successfully completed
// turns. It cannot select roles, raw runs, scope, Evidence, or arbitrary SQL.
type ModelContextPageRequest struct {
	SessionID domain.SessionID
	Limit     int
	After     *ModelContextCursor
}

// Validate checks the complete bounded page selection.
func (request ModelContextPageRequest) Validate() error {
	if !request.SessionID.Valid() || request.Limit < 1 || request.Limit > modelContextPageSize {
		return ErrModelContextUnavailable
	}
	if request.After != nil && (!request.After.ID.Valid() || !request.After.RunID.Valid() ||
		request.After.RunSequence < 0 || request.After.RunSequence > domain.MaxRunConversationSequence ||
		!validCoordinatorTime(request.After.RunStartedAt)) {
		return ErrModelContextUnavailable
	}
	return nil
}

// ModelContextPage is one bounded committed-order page.
type ModelContextPage struct {
	Messages []domain.Message
	Next     *ModelContextCursor
}

// ModelContextPersistence owns exactly the durable reads and summary write
// needed by Session model memory.
type ModelContextPersistence interface {
	ListEligibleModelContext(context.Context, ModelContextPageRequest) (ModelContextPage, error)
	LoadSessionContextSummary(context.Context, domain.SessionID) (domain.SessionContextSummary, bool, error)
	SaveSessionContextSummary(context.Context, domain.SessionContextSummary) error
}

type sessionModelContext struct {
	sessionID       domain.SessionID
	mode            domain.PrivacyMode
	loaded          bool
	turns           []agent.ConversationTurn
	coverage        []domain.SessionContextCoverageItem
	summary         *domain.SessionContextSummary
	eligibleBytes   int
	compressedAt    *time.Time
	recentTailCount int
	storageHealthy  bool
}

func newSessionModelContext(session domain.Session, loaded bool) sessionModelContext {
	return sessionModelContext{
		sessionID: session.ID, mode: session.PrivacyMode, loaded: loaded,
		storageHealthy: true,
	}
}

func (state sessionModelContext) conversation() (agent.ConversationContext, error) {
	return agent.NewConversationContextWithCoverage(state.sessionID, state.turns, state.summary, state.coverage)
}

func (state *sessionModelContext) clear() {
	if state == nil {
		return
	}
	*state = sessionModelContext{}
}

func eligibleConversationTurn(message domain.Message) (agent.ConversationTurn, error) {
	if message.Validate() != nil || message.Status != domain.MessageStatusCommitted ||
		message.RunID == nil || message.RunSequence == nil ||
		(message.Role != domain.MessageRoleUser && message.Role != domain.MessageRoleAssistant) {
		return agent.ConversationTurn{}, ErrModelContextUnavailable
	}
	return agent.ConversationTurn{
		MessageID: message.ID, RunID: *message.RunID, RunSequence: *message.RunSequence,
		Role: message.Role, Content: message.Content, ContentHash: message.Hash,
	}, nil
}

func validateEligibleTurns(messages []domain.Message, sessionID domain.SessionID) error {
	if len(messages) > domain.MaxSessionContextMessages {
		return ErrModelContextUnavailable
	}
	coverage := make([]domain.SessionContextCoverageItem, len(messages))
	for index, message := range messages {
		if message.SessionID != sessionID || message.RunID == nil || message.RunSequence == nil {
			return ErrModelContextUnavailable
		}
		if index > 0 {
			previous := messages[index-1]
			if *message.RunID == *previous.RunID && *message.RunSequence != *previous.RunSequence+1 {
				return ErrModelContextUnavailable
			}
		}
		coverage[index] = domain.SessionContextCoverageItem{
			MessageID: message.ID, RunID: *message.RunID, RunSequence: *message.RunSequence,
			Role: message.Role, ContentHash: message.Hash, ContentBytes: len(message.Content),
		}
	}
	if len(coverage) > 0 && domain.ValidateSessionContextCoverage(coverage) != nil {
		return ErrModelContextUnavailable
	}
	return nil
}

func (coordinator *Coordinator) conversationForRun(
	ctx context.Context,
	sessionID domain.SessionID,
) (agent.ConversationContext, error) {
	coordinator.mu.Lock()
	if coordinator.currentSession == nil {
		coordinator.mu.Unlock()
		return agent.NewConversationContext(sessionID, nil, nil)
	}
	if coordinator.currentSession.ID != sessionID || coordinator.modelContext.sessionID != sessionID {
		coordinator.mu.Unlock()
		return agent.ConversationContext{}, ErrModelContextUnavailable
	}
	if coordinator.modelContext.loaded {
		if !coordinator.modelContext.storageHealthy {
			coordinator.mu.Unlock()
			return agent.ConversationContext{}, ErrModelContextUnavailable
		}
		value, err := coordinator.modelContext.conversation()
		coordinator.mu.Unlock()
		return value, err
	}
	mode := coordinator.modelContext.mode
	store := coordinator.modelContextStore
	coordinator.mu.Unlock()
	if mode == domain.PrivacyModeMinimal {
		return agent.ConversationContext{}, ErrModelContextUnavailable
	}
	if store == nil {
		return agent.ConversationContext{}, ErrModelContextUnavailable
	}

	loadContext, cancel := context.WithTimeout(ctx, coordinator.persistenceLimit)
	defer cancel()
	messages := make([]domain.Message, 0, modelContextPageSize)
	var cursor *ModelContextCursor
	for pageNumber := 0; pageNumber < maxModelContextPages; pageNumber++ {
		page, err := store.ListEligibleModelContext(loadContext, ModelContextPageRequest{
			SessionID: sessionID, Limit: modelContextPageSize, After: cursor,
		})
		if err != nil {
			return agent.ConversationContext{}, ErrModelContextUnavailable
		}
		messages = append(messages, page.Messages...)
		if len(messages) > domain.MaxSessionContextMessages {
			return agent.ConversationContext{}, ErrModelContextUnavailable
		}
		if page.Next == nil {
			cursor = nil
			break
		}
		last := page.Messages[len(page.Messages)-1]
		if len(page.Messages) != modelContextPageSize || last.RunID == nil || last.RunSequence == nil ||
			page.Next.ID != last.ID || page.Next.RunID != *last.RunID || page.Next.RunSequence != *last.RunSequence {
			return agent.ConversationContext{}, ErrModelContextUnavailable
		}
		value := *page.Next
		cursor = &value
	}
	if cursor != nil || validateEligibleTurns(messages, sessionID) != nil {
		return agent.ConversationContext{}, ErrModelContextUnavailable
	}

	summary, found, err := store.LoadSessionContextSummary(loadContext, sessionID)
	if err != nil {
		return agent.ConversationContext{}, ErrModelContextUnavailable
	}
	profile, originHash := coordinator.currentAgentBinding()
	coverage := make([]domain.SessionContextCoverageItem, len(messages))
	totalBytes := 0
	for index, message := range messages {
		totalBytes += len(message.Content)
		if totalBytes > domain.MaxSessionHistoryBytes {
			return agent.ConversationContext{}, ErrModelContextUnavailable
		}
		coverage[index] = domain.SessionContextCoverageItem{
			MessageID: message.ID, RunID: *message.RunID, RunSequence: *message.RunSequence,
			Role: message.Role, ContentHash: message.Hash, ContentBytes: len(message.Content),
		}
	}
	covered := 0
	var selectedSummary *domain.SessionContextSummary
	if found && summary.AgentProfile == profile && summary.AgentOriginHash == originHash &&
		summary.PolicyVersion == domain.SafeConversationContextPolicyVersion {
		if summary.CoveredCount > len(messages) ||
			domain.ValidateSessionContextCoverage(coverage[:summary.CoveredCount]) != nil {
			return agent.ConversationContext{}, ErrModelContextUnavailable
		}
		digest, coveredBytes, digestErr := domain.SessionContextCoverageDigestItems(coverage[:summary.CoveredCount])
		if digestErr != nil || digest != summary.CoverageDigest || coveredBytes != summary.CoveredBytes ||
			coverage[0].MessageID != summary.CoveredFirstID || coverage[summary.CoveredCount-1].MessageID != summary.CoveredThroughID {
			return agent.ConversationContext{}, ErrModelContextUnavailable
		}
		covered = summary.CoveredCount
		value := summary
		selectedSummary = &value
	}
	turns := make([]agent.ConversationTurn, 0, len(messages)-covered)
	for _, message := range messages[covered:] {
		turn, turnErr := eligibleConversationTurn(message)
		if turnErr != nil {
			return agent.ConversationContext{}, ErrModelContextUnavailable
		}
		turns = append(turns, turn)
	}
	conversation, err := agent.NewConversationContextWithCoverage(sessionID, turns, selectedSummary, coverage)
	if err != nil {
		return agent.ConversationContext{}, ErrModelContextUnavailable
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.closed || coordinator.currentSession == nil || coordinator.currentSession.ID != sessionID ||
		coordinator.modelContext.sessionID != sessionID || coordinator.modelContext.loaded {
		if coordinator.modelContext.loaded && coordinator.modelContext.sessionID == sessionID {
			return coordinator.modelContext.conversation()
		}
		return agent.ConversationContext{}, ErrModelContextUnavailable
	}
	coordinator.modelContext.turns = turns
	coordinator.modelContext.coverage = coverage
	coordinator.modelContext.summary = selectedSummary
	coordinator.modelContext.eligibleBytes = totalBytes
	coordinator.modelContext.recentTailCount = len(turns)
	coordinator.modelContext.loaded = true
	coordinator.modelContext.storageHealthy = true
	if selectedSummary != nil {
		value := selectedSummary.GeneratedAt
		coordinator.modelContext.compressedAt = &value
	}
	return conversation, nil
}

func (coordinator *Coordinator) currentAgentBinding() (string, string) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.currentAgentBindingLocked()
}

func (coordinator *Coordinator) currentAgentBindingLocked() (string, string) {
	profile := "agent"
	if runtime, ok := coordinator.modelRuntime.(namedModelRuntime); ok && domain.ValidModelToken(runtime.ProfileName(), 128) {
		profile = runtime.ProfileName()
	}
	return profile, coordinator.privacy.OriginHash()
}

func (coordinator *Coordinator) validateSummaryForRunLocked(state *activeRun, summary domain.SessionContextSummary) error {
	if state == nil || summary.Validate() != nil || summary.SessionID != state.run.SessionID {
		return ErrInvalidAgentEvent
	}
	context := state.input.Conversation()
	coverage := context.Coverage()
	previousCovered := 0
	if previous := context.Summary(); previous != nil {
		previousCovered = previous.CoveredCount
	}
	if summary.CoveredCount <= previousCovered || summary.CoveredCount > len(coverage) ||
		domain.ValidateSessionContextCoverage(coverage[:summary.CoveredCount]) != nil {
		return ErrInvalidAgentEvent
	}
	digest, bytes, err := domain.SessionContextCoverageDigestItems(coverage[:summary.CoveredCount])
	if err != nil || digest != summary.CoverageDigest || bytes != summary.CoveredBytes ||
		coverage[0].MessageID != summary.CoveredFirstID || coverage[summary.CoveredCount-1].MessageID != summary.CoveredThroughID {
		return ErrInvalidAgentEvent
	}
	profile, originHash := coordinator.currentAgentBindingLocked()
	if summary.AgentProfile != profile || summary.AgentOriginHash != originHash {
		return ErrInvalidAgentEvent
	}
	return nil
}

func (coordinator *Coordinator) acceptStoredSummary(state *activeRun, summary domain.SessionContextSummary) error {
	context := state.input.Conversation()
	previousCovered := 0
	if previous := context.Summary(); previous != nil {
		previousCovered = previous.CoveredCount
	}
	drop := summary.CoveredCount - previousCovered
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.active != state || coordinator.modelContext.sessionID != summary.SessionID ||
		!coordinator.modelContext.loaded || drop < 0 || drop > len(coordinator.modelContext.turns) {
		return ErrInvalidAgentEvent
	}
	value := summary
	coordinator.modelContext.summary = &value
	coordinator.modelContext.turns = append([]agent.ConversationTurn(nil), coordinator.modelContext.turns[drop:]...)
	coordinator.modelContext.recentTailCount = len(coordinator.modelContext.turns)
	generated := summary.GeneratedAt
	coordinator.modelContext.compressedAt = &generated
	return nil
}

func (coordinator *Coordinator) recordCompletedConversation(state *activeRun, assistantID domain.MessageID, answer string) error {
	if state == nil || !assistantID.Valid() {
		return ErrModelContextUnavailable
	}
	user := agent.ConversationTurn{
		MessageID: state.input.RequestMessageID(), RunID: state.run.ID, RunSequence: 0,
		Role:    domain.MessageRoleUser,
		Content: state.input.Question(), ContentHash: domain.MessageContentHash(state.input.Question()),
	}
	assistant := agent.ConversationTurn{
		MessageID: assistantID, RunID: state.run.ID, RunSequence: 1 + len(state.committedInputs),
		Role:    domain.MessageRoleAssistant,
		Content: answer, ContentHash: domain.MessageContentHash(answer),
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.modelContext.sessionID != state.run.SessionID || !coordinator.modelContext.loaded {
		return ErrModelContextUnavailable
	}
	candidate := coordinator.modelContext
	runTurns := make([]agent.ConversationTurn, 0, 2+len(state.committedInputs))
	runTurns = append(runTurns, user)
	runTurns = append(runTurns, state.committedInputs...)
	runTurns = append(runTurns, assistant)
	candidate.turns = append(append([]agent.ConversationTurn(nil), coordinator.modelContext.turns...), runTurns...)
	candidate.coverage = append([]domain.SessionContextCoverageItem(nil), coordinator.modelContext.coverage...)
	for _, turn := range runTurns {
		candidate.coverage = append(candidate.coverage, domain.SessionContextCoverageItem{
			MessageID: turn.MessageID, RunID: turn.RunID, RunSequence: turn.RunSequence,
			Role: turn.Role, ContentHash: turn.ContentHash, ContentBytes: len(turn.Content),
		})
	}
	candidate.eligibleBytes += len(user.Content) + len(assistant.Content)
	for _, turn := range state.committedInputs {
		candidate.eligibleBytes += len(turn.Content)
	}
	candidate.recentTailCount = len(candidate.turns)
	if _, err := candidate.conversation(); err != nil {
		return err
	}
	coordinator.modelContext = candidate
	return nil
}
