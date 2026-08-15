package tui

import (
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	performanceRunID        domain.AgentRunID       = "00000000-0000-7000-8000-000000009701"
	performanceInvocationID domain.ToolInvocationID = "00000000-0000-7000-8000-000000009702"
	performanceSessionID    domain.SessionID        = "00000000-0000-7000-8000-000000009703"
)

var (
	streamRenderBenchmarkText  string
	streamRenderBenchmarkModel Model
)

// BenchmarkStreamRenderV1 measures the normal ordered Bubble Tea reduction and
// render path for a maximum-size stream and the maximum retained message count.
func BenchmarkStreamRenderV1(b *testing.B) {
	b.Run("BoundedStream64KiB", benchmarkBoundedStreamRender)
	b.Run("RetainedHistory100x1KiB", benchmarkRetainedHistory)
}

func benchmarkBoundedStreamRender(b *testing.B) {
	const deltaCount = 16
	delta := strings.Repeat("x", application.MaxQuestionBytes/deltaCount)
	terminal := strings.Repeat("f", application.MaxQuestionBytes-8) + "\x1b[31m" + "\u202e"
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		model := newPerformanceModel()
		view := applyPerformanceEvent(b, &model, application.UIEvent{
			Kind: application.UIEventRunStarted, RunID: performanceRunID,
			ScopeGeneration: 7, Sequence: 1,
		})
		for index := range deltaCount {
			view = applyPerformanceEvent(b, &model, application.UIEvent{
				Kind: application.UIEventTextDelta, RunID: performanceRunID,
				ScopeGeneration: 7, Sequence: int64(index + 2), Text: delta,
			})
		}
		view = applyPerformanceEvent(b, &model, application.UIEvent{
			Kind: application.UIEventTextDelta, RunID: performanceRunID,
			ScopeGeneration: 8, Sequence: 18, Text: "stale",
		})
		view = applyPerformanceEvent(b, &model, application.UIEvent{
			Kind: application.UIEventToolStep, RunID: performanceRunID,
			ScopeGeneration: 7, Sequence: 18,
			ToolStep: &application.ToolStep{
				InvocationID: performanceInvocationID, Name: domain.ToolNameGetResource,
				Purpose: "Inspect one bounded synthetic projection.", Status: application.ToolStepRunning,
			},
		})
		view = applyPerformanceEvent(b, &model, application.UIEvent{
			Kind: application.UIEventToolStep, RunID: performanceRunID,
			ScopeGeneration: 7, Sequence: 19,
			ToolStep: &application.ToolStep{
				InvocationID: performanceInvocationID, Name: domain.ToolNameGetResource,
				Purpose: "Inspect one bounded synthetic projection.", Status: application.ToolStepSucceeded,
				Summary: "The synthetic projection was collected.", EvidenceCount: 2,
			},
		})
		view = applyPerformanceEvent(b, &model, application.UIEvent{
			Kind: application.UIEventRunCompleted, RunID: performanceRunID,
			ScopeGeneration: 7, Sequence: 20, Text: terminal,
		})
		view = applyPerformanceEvent(b, &model, application.UIEvent{
			Kind: application.UIEventTextDelta, RunID: performanceRunID,
			ScopeGeneration: 7, Sequence: 21, Text: "late",
		})
		if !model.run.Terminal || model.run.LastSequence != 20 || len(model.run.StreamedText) > application.MaxQuestionBytes {
			b.Fatalf("terminal stream state is invalid: sequence=%d bytes=%d", model.run.LastSequence, len(model.run.StreamedText))
		}
		if strings.ContainsAny(model.run.StreamedText, "\x1b\u202e") || strings.Contains(view, "\x1b[31m") ||
			strings.ContainsRune(view, '\u202e') || len(view) > 16*1024 {
			b.Fatalf("rendered output is unsafe or unbounded: bytes=%d", len(view))
		}
		streamRenderBenchmarkText = view
		streamRenderBenchmarkModel = model
	}
}

func benchmarkRetainedHistory(b *testing.B) {
	content := strings.Repeat("h", 1024)
	history := make([]application.UIHistoryMessage, 100)
	for index := range history {
		history[index] = application.UIHistoryMessage{
			Role: domain.MessageRoleUser, Format: domain.MessageFormatPlain, Content: content,
		}
		if index%2 == 1 {
			history[index].Role = domain.MessageRoleAssistant
			history[index].Format = domain.MessageFormatMarkdown
		}
	}
	resumed := application.UIResumedSession{
		ResumeRequestID: 1,
		Session: application.UISessionCandidate{
			ID: performanceSessionID, Title: "Synthetic performance history",
			UpdatedAtUnixMillis: 1_700_000_000_000, Context: "example-context",
			Namespace: "example-namespace", PrivacyMode: domain.PrivacyModeStandard,
		},
		History: history,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		model := newPerformanceModel()
		model.applyAcceptedResume(resumed)
		model.reflow()
		view := model.render()
		if len(model.transcript.Entries()) != len(history)+1 || len(view) > 16*1024 || strings.ContainsRune(view, '\u202e') {
			b.Fatalf("retained history render is invalid: entries=%d bytes=%d", len(model.transcript.Entries()), len(view))
		}
		streamRenderBenchmarkText = view
		streamRenderBenchmarkModel = model
	}
}

func newPerformanceModel() Model {
	return NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor, NoColor: true,
		Scope: ScopeView{
			Context: "example-context", Namespace: "example-namespace",
			Generation: 7, ReadOnly: true,
		},
	})
}

func applyPerformanceEvent(tb testing.TB, model *Model, event application.UIEvent) string {
	tb.Helper()
	next, _ := model.Update(ApplicationEventMsg{Event: event})
	updated, ok := next.(Model)
	if !ok {
		tb.Fatalf("Update returned %T, want tui.Model", next)
	}
	*model = updated
	return model.View().Content
}
