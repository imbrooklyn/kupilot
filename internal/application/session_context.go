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
	CreatedAt time.Time
	ID        domain.MessageID
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
	if request.After != nil && (!request.After.ID.Valid() || !validCoordinatorTime(request.After.CreatedAt)) {
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
		(message.Role != domain.MessageRoleUser && message.Role != domain.MessageRoleAssistant) {
		return agent.ConversationTurn{}, ErrModelContextUnavailable
	}
	return agent.ConversationTurn{
		MessageID: message.ID, Role: message.Role, Content: message.Content, ContentHash: message.Hash,
	}, nil
}

func validateEligibleTurns(messages []domain.Message, sessionID domain.SessionID) error {
	if len(messages)%2 != 0 || len(messages) > domain.MaxSessionContextMessages {
		return ErrModelContextUnavailable
	}
	for index, message := range messages {
		if message.SessionID != sessionID || message.RunID == nil {
			return ErrModelContextUnavailable
		}
		wantRole := domain.MessageRoleUser
		if index%2 == 1 {
			wantRole = domain.MessageRoleAssistant
			if messages[index-1].RunID == nil || *messages[index-1].RunID != *message.RunID {
				return ErrModelContextUnavailable
			}
		}
		if message.Role != wantRole {
			return ErrModelContextUnavailable
		}
		if index > 0 {
			previous := messages[index-1]
			if message.CreatedAt.Before(previous.CreatedAt) ||
				message.CreatedAt.Equal(previous.CreatedAt) && message.ID <= previous.ID {
				return ErrModelContextUnavailable
			}
		}
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
		if len(page.Messages) != modelContextPageSize || page.Next.ID != page.Messages[len(page.Messages)-1].ID ||
			!page.Next.CreatedAt.Equal(page.Messages[len(page.Messages)-1].CreatedAt) {
			return agent.ConversationContext{}, ErrModelContextUnavailable
		}
		cursor = &ModelContextCursor{CreatedAt: page.Next.CreatedAt, ID: page.Next.ID}
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
			MessageID: message.ID, Role: message.Role, ContentHash: message.Hash, ContentBytes: len(message.Content),
		}
	}
	covered := 0
	var selectedSummary *domain.SessionContextSummary
	if found && summary.AgentProfile == profile && summary.AgentOriginHash == originHash &&
		summary.PolicyVersion == domain.SafeConversationContextPolicyVersion {
		if summary.CoveredCount > len(messages) || summary.CoveredCount%2 != 0 {
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
	if summary.CoveredCount <= previousCovered || summary.CoveredCount > len(coverage) || summary.CoveredCount%2 != 0 {
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
		MessageID: state.input.RequestMessageID(), Role: domain.MessageRoleUser,
		Content: state.input.Question(), ContentHash: domain.MessageContentHash(state.input.Question()),
	}
	assistant := agent.ConversationTurn{
		MessageID: assistantID, Role: domain.MessageRoleAssistant,
		Content: answer, ContentHash: domain.MessageContentHash(answer),
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.modelContext.sessionID != state.run.SessionID || !coordinator.modelContext.loaded {
		return ErrModelContextUnavailable
	}
	candidate := coordinator.modelContext
	candidate.turns = append(append([]agent.ConversationTurn(nil), coordinator.modelContext.turns...), user, assistant)
	candidate.coverage = append(append([]domain.SessionContextCoverageItem(nil), coordinator.modelContext.coverage...),
		domain.SessionContextCoverageItem{MessageID: user.MessageID, Role: user.Role, ContentHash: user.ContentHash, ContentBytes: len(user.Content)},
		domain.SessionContextCoverageItem{MessageID: assistant.MessageID, Role: assistant.Role, ContentHash: assistant.ContentHash, ContentBytes: len(assistant.Content)},
	)
	candidate.eligibleBytes += len(user.Content) + len(assistant.Content)
	candidate.recentTailCount = len(candidate.turns)
	if _, err := candidate.conversation(); err != nil {
		return err
	}
	coordinator.modelContext = candidate
	return nil
}
