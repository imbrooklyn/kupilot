package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const streamBenchmarkRunID domain.AgentRunID = "00000000-0000-7000-8000-000000009601"

var streamMergeBenchmarkSink []UIEvent

// BenchmarkStreamDeltaMergeV1 measures one maximum-size stream assembled from
// fixed small deltas at the normal Application-to-TUI coalescing boundary.
func BenchmarkStreamDeltaMergeV1(b *testing.B) {
	const (
		deltaBytes = 1024
		deltaCount = MaxQuestionBytes / deltaBytes
	)
	delta := strings.Repeat("x", deltaBytes)
	now := time.UnixMilli(1_700_000_000_000).UTC()
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		events := make([]UIEvent, 0, deltaCount/4+2)
		bridge, err := newEventBridge(streamBenchmarkRunID, 7, UIEventSinkFunc(func(_ context.Context, event UIEvent) error {
			events = append(events, event)
			return nil
		}))
		if err != nil {
			b.Fatalf("newEventBridge() error = %v", err)
		}
		if err := bridge.accept(ctx, agent.RunEvent{
			RunID: streamBenchmarkRunID, ScopeGeneration: 7, Sequence: 1,
			OccurredAt: now, Kind: agent.RunEventRunStarted,
		}); err != nil {
			b.Fatalf("accept(started) error = %v", err)
		}
		for index := range deltaCount {
			if err := bridge.accept(ctx, agent.RunEvent{
				RunID: streamBenchmarkRunID, ScopeGeneration: 7, Sequence: int64(index + 2),
				OccurredAt: now, Kind: agent.RunEventTextDelta, TextDelta: delta,
			}); err != nil {
				b.Fatalf("accept(delta) error = %v", err)
			}
		}
		if err := bridge.accept(ctx, agent.RunEvent{
			RunID: streamBenchmarkRunID, ScopeGeneration: 7, Sequence: deltaCount + 2,
			OccurredAt: now, Kind: agent.RunEventRunFailed,
			Failure: &agent.RunEventFailure{
				Class: domain.SafeErrorClassInternal, SafeMessage: "The synthetic AgentRun failed safely.",
			},
		}); err != nil {
			b.Fatalf("accept(terminal) error = %v", err)
		}
		if len(events) != deltaCount/4+2 || !events[len(events)-1].Terminal() {
			b.Fatalf("coalesced event shape is invalid: count=%d", len(events))
		}
		if err := bridge.accept(ctx, agent.RunEvent{
			RunID: streamBenchmarkRunID, ScopeGeneration: 7, Sequence: deltaCount + 3,
			OccurredAt: now, Kind: agent.RunEventTextDelta, TextDelta: "late",
		}); !errors.Is(err, ErrInvalidUIEvent) {
			b.Fatalf("post-terminal event error = %v, want ErrInvalidUIEvent", err)
		}
		streamMergeBenchmarkSink = events
	}
}
