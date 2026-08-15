package agent_test

import (
	"path/filepath"
	"testing"
)

type diagnosisBenchmarkFixture struct {
	policy  scenarioPolicy
	fixture conversationFixture
}

var diagnosisBenchmarkSink scenarioRun

// BenchmarkDiagnosisFixtureMatrixV1 runs the complete synthetic diagnosis
// matrix through the fixed model and Tool adapters and then applies its rubric.
func BenchmarkDiagnosisFixtureMatrixV1(b *testing.B) {
	fixtures := loadDiagnosisBenchmarkFixtures(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, fixture := range fixtures {
			run := runConversationFixture(b, fixture.fixture)
			if err := evaluateDiagnosisRubric(fixture.policy, fixture.fixture, run); err != nil {
				b.Fatalf("rubric evaluation failed: %v", err)
			}
			diagnosisBenchmarkSink = run
		}
	}
}

func loadDiagnosisBenchmarkFixtures(tb testing.TB) []diagnosisBenchmarkFixture {
	tb.Helper()
	result := make([]diagnosisBenchmarkFixture, 0, len(diagnosisScenarioExpectations())*2)
	for _, expectation := range diagnosisScenarioExpectations() {
		directory := diagnosisFixturePath(expectation.name)
		policy := readStrictJSONFixture[scenarioPolicy](tb, filepath.Join(directory, "rubric.json"))
		assertPolicyExpectation(tb, policy, expectation)
		for _, fixtureName := range []string{"sufficient.json", "limited.json"} {
			result = append(result, diagnosisBenchmarkFixture{
				policy:  policy,
				fixture: readStrictJSONFixture[conversationFixture](tb, filepath.Join(directory, fixtureName)),
			})
		}
	}
	return result
}
