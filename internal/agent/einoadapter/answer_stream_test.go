package einoadapter

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestAnswerMarkdownExtractorDecodesFragmentedFirstFieldOnly(t *testing.T) {
	t.Parallel()

	fragments := []string{
		" \n{\"ans", "wer_markdown\"", ":\"First\\n", "second \\u4f60",
		"\\u597d \\uD83D", "\\uDE00 and \\\"quoted\\\".\"",
		",\"evidence_citations\":[{\"claim\":\"must not render\"}]",
		",\"proposed_actions\":[]}",
	}
	var extractor answerMarkdownExtractor
	var got strings.Builder
	for _, fragment := range fragments {
		got.WriteString(extractor.push(fragment))
	}
	want := "First\nsecond " + string([]rune{0x4f60, 0x597d}) + " " + string(rune(0x1f600)) + " and \"quoted\"."
	if !extractor.complete() || got.String() != want || strings.Contains(got.String(), "must not render") {
		t.Fatalf("extracted answer complete=%t value=%q", extractor.complete(), got.String())
	}
}

func TestAnswerMarkdownExtractorDoesNotProjectOtherResponseShapes(t *testing.T) {
	t.Parallel()

	inputs := []struct {
		value             string
		provisionalPrefix string
	}{
		{value: `{"evidence_citations":[],"answer_markdown":"late","proposed_actions":[]}`},
		{value: `I will inspect another resource before answering.`},
		{value: `{"answer_markdown":42,"evidence_citations":[],"proposed_actions":[]}`},
		{value: `{"answer_markdown":"invalid\q escape","evidence_citations":[],"proposed_actions":[]}`, provisionalPrefix: "invalid"},
	}
	for _, input := range inputs {
		var extractor answerMarkdownExtractor
		if got := extractor.push(input.value); got != input.provisionalPrefix || extractor.complete() {
			t.Fatalf("unsupported response projected %q from %q", got, input.value)
		}
	}
}

func TestCredentialStreamGuardNeverEmitsSplitCredentialPrefix(t *testing.T) {
	t.Parallel()

	secret := strings.Join([]string{"generated", "model", "credential", "9201"}, "-")
	credential, err := config.NewSecretValue(secret)
	if err != nil {
		t.Fatalf("NewSecretValue() error = %v", err)
	}
	defer credential.Destroy()
	guard := credentialStreamGuard{credential: &credential}
	var emitted strings.Builder
	parts := []string{"safe ", secret[:9], secret[9:18], secret[18:] + " tail"}
	for index, part := range parts {
		value, found := guard.push(part, false)
		if found {
			if index != len(parts)-1 {
				t.Fatalf("credential detected at fragment %d", index)
			}
			if strings.Contains(emitted.String(), secret) || emitted.String() != "safe " {
				t.Fatalf("credential prefix reached output: %q", emitted.String())
			}
			return
		}
		emitted.WriteString(value)
	}
	t.Fatal("split credential was not detected")
}

func TestProvisionalEventBudgetIsSharedByTheEntireRun(t *testing.T) {
	t.Parallel()

	state := new(runState)
	for index := 0; index < maxProvisionalAnswerEvents; index++ {
		if !state.reserveProvisionalEvent() {
			t.Fatalf("provisional event %d was rejected before the run-wide ceiling", index+1)
		}
	}
	if state.reserveProvisionalEvent() {
		t.Fatal("the run-wide provisional event ceiling expanded")
	}

	limits, err := agent.RunBudgetLimitsForProfile(agent.BudgetProfileExtended)
	if err != nil {
		t.Fatalf("RunBudgetLimitsForProfile() error = %v", err)
	}
	worstCaseEvents := maxProvisionalAnswerEvents + 1 + limits.ModelCalls +
		limits.ToolCalls*(3+domain.MaxEvidenceItemsPerResult) + 2
	if worstCaseEvents > agent.MaxRunEvents {
		t.Fatalf("provisional budget leaves insufficient structural event capacity: %d > %d", worstCaseEvents, agent.MaxRunEvents)
	}
}

func TestAdapterStreamsFragmentedAnswerWithoutEnvelopeMetadata(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	const answer = "Streaming answer arrives in several visible fragments."
	const diagnosis = `{"answer_markdown":"` + answer + `","evidence_citations":[],"proposed_actions":[],"response_schema_version":2,"outcome":"answer","stop_reason":"completed","limitations":[],"questions":[]}`
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: `{"answer_markdown":"Streaming `},
		{Role: schema.Assistant, Content: "answer arrives "},
		{Role: schema.Assistant, Content: "in several "},
		{Role: schema.Assistant, Content: `visible fragments.","evidence_citations":[]`},
		{Role: schema.Assistant, Content: `,"proposed_actions":[],"response_schema_version":2,"outcome":"answer","stop_reason":"completed","limitations":[],"questions":[]}`},
		{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	model := &recordingModel{scripts: []modelScript{scriptedChunks(chunks...)}}
	recorder := newEventRecorder()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	outcome := testAdapter(t, clock, model, new(recordingTool), guard).Run(context.Background(), input, recorder)
	if outcome.Status != domain.AgentRunStatusCompleted || outcome.Diagnosis == nil ||
		outcome.Diagnosis.AnswerMarkdown != answer {
		t.Fatalf("streamed outcome = %#v", outcome)
	}
	var streamed strings.Builder
	deltas := 0
	for _, event := range recorder.Events() {
		if event.Kind != agent.RunEventTextDelta {
			continue
		}
		deltas++
		streamed.WriteString(event.TextDelta)
	}
	if deltas < 2 || streamed.String() != answer || strings.Contains(streamed.String(), "evidence_citations") ||
		strings.Contains(streamed.String(), "proposed_actions") || strings.Contains(streamed.String(), diagnosis) {
		t.Fatalf("streamed deltas=%d value=%q", deltas, streamed.String())
	}
}

func TestAdapterStopsStreamingWhenScopeBecomesStale(t *testing.T) {
	clock := newTestClock()
	guard := newTestScopeGuard()
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: `{"answer_markdown":"first `},
		{Role: schema.Assistant, Content: `second third","evidence_citations":[],"proposed_actions":[]}`},
		{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}},
	}
	model := &recordingModel{scripts: []modelScript{scriptedChunks(chunks...)}}
	var events []agent.RunEvent
	sink := agent.EventSinkFunc(func(_ context.Context, event agent.RunEvent) agent.EventSinkResult {
		events = append(events, event)
		if event.Kind == agent.RunEventTextDelta {
			guard.SetCurrent(false)
		}
		return agent.EventSinkAccepted
	})
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	outcome := testAdapter(t, clock, model, new(recordingTool), guard).Run(context.Background(), input, sink)
	if outcome.Status != domain.AgentRunStatusStaleScope || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassStaleScope {
		t.Fatalf("stale streaming outcome = %#v", outcome)
	}
	deltas := 0
	for _, event := range events {
		if event.Kind == agent.RunEventTextDelta {
			deltas++
		}
	}
	if deltas != 1 || len(model.Requests()) != 1 {
		t.Fatalf("stale stream deltas=%d model calls=%d events=%#v", deltas, len(model.Requests()), events)
	}
}

func TestAdapterBlocksJSONEscapedCredentialFromEveryDiagnosisSink(t *testing.T) {
	t.Parallel()

	const credential = "test-model-credential-8100"
	escaped := jsonUnicodeEscapeASCII(credential)
	tests := []struct {
		name      string
		diagnosis string
	}{
		{
			name: "answer",
			diagnosis: `{"answer_markdown":"` + escaped +
				`","evidence_citations":[],"proposed_actions":[]}`,
		},
		{
			name: "citation metadata",
			diagnosis: `{"answer_markdown":"Safe provisional answer.","evidence_citations":[{"sequence":1,"claim":"` + escaped +
				`","claim_type":"unsupported_observation","claim_hash":"661a251f9fde14b9e426d1bbb3d8cad0786d9a0129aa806810d0880df1de0426",` +
				`"evidence_ids":[],"coverage_state":"unsupported"}],"proposed_actions":[]}`,
		},
		{
			name: "proposed action metadata",
			diagnosis: `{"answer_markdown":"Safe provisional answer.","evidence_citations":[],` +
				`"proposed_actions":[{"operation":"restart_deployment","reason":"Restart the workload.",` +
				`"risk":"Pods will be replaced.","prerequisites":["` + escaped + `"],` +
				`"target":{"api_version":"apps/v1","kind":"Deployment",` +
				`"namespace":"test-namespace","name":"sample-deployment"},"parameters":null}]}`,
		},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			clock := newTestClock()
			model := &recordingModel{scripts: []modelScript{scriptedChunks(diagnosisChunks(current.diagnosis)...)}}
			recorder := newEventRecorder()
			input := testInput(t, clock, agent.DefaultRunBudgetLimits())
			outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
				context.Background(), input, recorder,
			)
			if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
				*outcome.ErrorClass != domain.SafeErrorClassSensitiveOutputBlocked || outcome.Diagnosis != nil {
				t.Fatalf("escaped-credential outcome = %#v", outcome)
			}
			encodedEvents := fmt.Sprintf("%#v", recorder.Events())
			if strings.Contains(encodedEvents, credential) || strings.Contains(encodedEvents, escaped) {
				t.Fatalf("escaped credential reached run events: %s", encodedEvents)
			}
			for _, event := range recorder.Events() {
				if event.Kind == agent.RunEventDiagnosisReady {
					t.Fatalf("escaped credential produced a Diagnosis event: %#v", event)
				}
			}
		})
	}
}

func TestAdapterCancellationAfterProvisionalAnswerStopsOwnedStream(t *testing.T) {
	clock := newTestClock()
	model := &cancellablePartialModel{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adapter := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard())
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	var (
		mu     sync.Mutex
		events []agent.RunEvent
		once   sync.Once
	)
	deltaSeen := make(chan struct{})
	sink := agent.EventSinkFunc(func(_ context.Context, event agent.RunEvent) agent.EventSinkResult {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		if event.Kind == agent.RunEventTextDelta {
			once.Do(func() { close(deltaSeen) })
		}
		return agent.EventSinkAccepted
	})
	result := make(chan agent.RunOutcome, 1)
	go func() {
		result <- adapter.Run(ctx, input, sink)
	}()
	select {
	case <-deltaSeen:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("provisional answer did not arrive")
	}
	var outcome agent.RunOutcome
	select {
	case outcome = <-result:
	case <-time.After(time.Second):
		t.Fatal("cancelled provisional stream did not stop")
	}
	if outcome.Status != domain.AgentRunStatusCancelled || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassCancelled || outcome.Diagnosis != nil {
		t.Fatalf("cancelled provisional outcome = %#v", outcome)
	}
	mu.Lock()
	defer mu.Unlock()
	deltaCount, terminalCount := 0, 0
	for _, event := range events {
		if event.Kind == agent.RunEventTextDelta {
			deltaCount++
		}
		if event.Terminal() {
			terminalCount++
		}
		if event.Kind == agent.RunEventDiagnosisReady {
			t.Fatalf("cancelled provisional stream produced a Diagnosis: %#v", event)
		}
	}
	if deltaCount == 0 || terminalCount != 1 {
		t.Fatalf("cancelled provisional events = %#v", events)
	}
}

func TestAdapterTimeoutAfterProvisionalAnswerPublishesNoDiagnosis(t *testing.T) {
	t.Parallel()

	clock := newTestClock()
	model := &partialErrorModel{failure: domain.NewModelError(
		domain.ModelErrorCodeTimeout,
		domain.ModelOperationStream,
		"00000000-0000-7000-8000-000000008201",
	)}
	recorder := newEventRecorder()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), input, recorder,
	)
	if outcome.Status != domain.AgentRunStatusTimedOut || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassTimeout || outcome.Diagnosis != nil {
		t.Fatalf("timed-out provisional outcome = %#v", outcome)
	}
	deltaCount, terminalCount := 0, 0
	for _, event := range recorder.Events() {
		if event.Kind == agent.RunEventTextDelta {
			deltaCount++
		}
		if event.Terminal() {
			terminalCount++
		}
		if event.Kind == agent.RunEventDiagnosisReady {
			t.Fatalf("timed-out provisional stream produced a Diagnosis: %#v", event)
		}
	}
	if deltaCount == 0 || terminalCount != 1 {
		t.Fatalf("timed-out provisional events = %#v", recorder.Events())
	}
}

func TestAdapterMalformedStreamAfterProvisionalAnswerPublishesNoDiagnosis(t *testing.T) {
	t.Parallel()

	clock := newTestClock()
	model := &partialErrorModel{failure: domain.NewModelError(
		domain.ModelErrorCodeMalformedStream,
		domain.ModelOperationStream,
		"00000000-0000-7000-8000-000000008202",
	)}
	recorder := newEventRecorder()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), input, recorder,
	)
	if outcome.Status != domain.AgentRunStatusFailed || outcome.ErrorClass == nil ||
		*outcome.ErrorClass != domain.SafeErrorClassInvalidExternalResponse || outcome.Diagnosis != nil {
		t.Fatalf("malformed provisional outcome = %#v", outcome)
	}
	assertProvisionalFailureEvents(t, recorder.Events())
}

func TestAdapterLengthLimitReplacesProvisionalAnswerWithLocalDiagnosis(t *testing.T) {
	t.Parallel()

	clock := newTestClock()
	model := &recordingModel{scripts: []modelScript{scriptedChunks(
		&schema.Message{Role: schema.Assistant, Content: `{"answer_markdown":"Visible provisional answer before length limit`},
		&schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{FinishReason: "length"}},
	)}}
	recorder := newEventRecorder()
	input := testInput(t, clock, agent.DefaultRunBudgetLimits())
	outcome := testAdapter(t, clock, model, new(recordingTool), newTestScopeGuard()).Run(
		context.Background(), input, recorder,
	)
	if outcome.Status != domain.AgentRunStatusCompleted || outcome.ErrorClass != nil || outcome.Diagnosis == nil ||
		outcome.Diagnosis.AnswerMarkdown != "The model response reached its fixed output limit." {
		t.Fatalf("length-limited provisional outcome = %#v", outcome)
	}
	deltaCount, diagnosisCount, terminalCount := 0, 0, 0
	for _, event := range recorder.Events() {
		if event.Kind == agent.RunEventTextDelta {
			deltaCount++
		}
		if event.Kind == agent.RunEventDiagnosisReady {
			diagnosisCount++
		}
		if event.Terminal() {
			terminalCount++
		}
	}
	if deltaCount == 0 || diagnosisCount != 1 || terminalCount != 1 {
		t.Fatalf("length-limited provisional events = %#v", recorder.Events())
	}
}

func assertProvisionalFailureEvents(t *testing.T, events []agent.RunEvent) {
	t.Helper()
	deltaCount, terminalCount := 0, 0
	for _, event := range events {
		if event.Kind == agent.RunEventTextDelta {
			deltaCount++
		}
		if event.Terminal() {
			terminalCount++
		}
		if event.Kind == agent.RunEventDiagnosisReady {
			t.Fatalf("failed provisional stream produced a Diagnosis: %#v", event)
		}
	}
	if deltaCount == 0 || terminalCount != 1 {
		t.Fatalf("failed provisional events = %#v", events)
	}
}

func jsonUnicodeEscapeASCII(value string) string {
	var result strings.Builder
	for _, current := range []byte(value) {
		_, _ = fmt.Fprintf(&result, `\u%04x`, current)
	}
	return result.String()
}

type cancellablePartialModel struct{}

func (*cancellablePartialModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if validateBoundToolInfos(tools) != nil {
		return nil, agent.ErrToolPolicyDenied
	}
	return &cancellablePartialModel{}, nil
}

func (model *cancellablePartialModel) Generate(
	ctx context.Context,
	messages []*schema.Message,
	options ...einomodel.Option,
) (*schema.Message, error) {
	stream, err := model.Stream(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.ConcatMessageStream(stream)
}

func (*cancellablePartialModel) Stream(
	ctx context.Context,
	_ []*schema.Message,
	_ ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		if writer.Send(&schema.Message{
			Role: schema.Assistant, Content: `{"answer_markdown":"Visible provisional answer before cancellation`,
		}, nil) {
			return
		}
		<-ctx.Done()
		writer.Send(nil, ctx.Err())
	}()
	return reader, nil
}

type partialErrorModel struct {
	failure error
}

func (model *partialErrorModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if model == nil || model.failure == nil || validateBoundToolInfos(tools) != nil {
		return nil, agent.ErrToolPolicyDenied
	}
	return model, nil
}

func (model *partialErrorModel) Generate(
	ctx context.Context,
	messages []*schema.Message,
	options ...einomodel.Option,
) (*schema.Message, error) {
	stream, err := model.Stream(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.ConcatMessageStream(stream)
}

func (model *partialErrorModel) Stream(
	_ context.Context,
	_ []*schema.Message,
	_ ...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	reader, writer := schema.Pipe[*schema.Message](2)
	writer.Send(&schema.Message{
		Role: schema.Assistant, Content: `{"answer_markdown":"Visible provisional answer before timeout`,
	}, nil)
	writer.Send(nil, model.failure)
	writer.Close()
	return reader, nil
}
