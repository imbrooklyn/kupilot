package einoadapter

import (
	"sort"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type safeReadCacheEntry struct {
	metadata     agent.ToolReuseMetadata
	subjectFacts map[string]string
}

func safeReadReusable(name domain.ToolName) bool {
	return planSafeTool(name)
}

func (state *runState) reusableSafeRead(call agent.BoundToolCall, now time.Time) (agent.ToolReuseMetadata, bool) {
	if !safeReadReusable(call.Name()) || !validRuntimeTime(now) {
		return agent.ToolReuseMetadata{}, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	entry, found := state.safeReadReuse[call.Identity()]
	if !found || entry.metadata.PolicyGeneration != call.PolicyGeneration() ||
		now.Before(entry.metadata.ObservedAt) || now.Sub(entry.metadata.ObservedAt) >= agent.SafeReadReuseWindow {
		return agent.ToolReuseMetadata{}, false
	}
	for subject := range entry.subjectFacts {
		if _, conflicting := state.conflictSubjects[subject]; conflicting {
			return agent.ToolReuseMetadata{}, false
		}
	}
	metadata := entry.metadata
	metadata.EvidenceIDs = append([]domain.EvidenceID(nil), metadata.EvidenceIDs...)
	return metadata, true
}

func (state *runState) retainReusableSafeRead(call agent.BoundToolCall, result domain.ToolResult, content string) {
	if !safeReadReusable(call.Name()) || result.Status != domain.ToolResultStatusSuccess ||
		result.Truncation.Truncated || content == "" {
		return
	}
	evidenceIDs := make([]domain.EvidenceID, 0, len(result.Evidence))
	subjectFacts := make(map[string]string, len(result.Evidence))
	for _, evidence := range result.Evidence {
		if evidence.Partial || evidence.Truncated {
			return
		}
		evidenceIDs = append(evidenceIDs, evidence.ID)
		subject := safeReadSubject(evidence)
		if previous, exists := subjectFacts[subject]; exists && previous != evidence.Fingerprint {
			return
		}
		subjectFacts[subject] = evidence.Fingerprint
	}
	sort.Slice(evidenceIDs, func(left, right int) bool { return evidenceIDs[left] < evidenceIDs[right] })
	metadata := agent.ToolReuseMetadata{
		SourceInvocationID: call.InvocationID(), EvidenceIDs: evidenceIDs,
		ObservedAt: result.ObservedAt, ResultDigest: domain.SHA256Hex(content),
		PolicyGeneration: call.PolicyGeneration(), FreshnessWindow: agent.SafeReadReuseWindow,
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for subject, fingerprint := range subjectFacts {
		if previous, exists := state.safeReadSubjects[subject]; exists && previous != fingerprint {
			state.conflictSubjects[subject] = struct{}{}
			for identity, entry := range state.safeReadReuse {
				if _, affected := entry.subjectFacts[subject]; affected {
					delete(state.safeReadReuse, identity)
				}
			}
		}
		state.safeReadSubjects[subject] = fingerprint
	}
	for subject := range subjectFacts {
		if _, conflicting := state.conflictSubjects[subject]; conflicting {
			return
		}
	}
	state.safeReadReuse[call.Identity()] = safeReadCacheEntry{metadata: metadata, subjectFacts: subjectFacts}
}

func (state *runState) reusableSafeReadStillCurrent(call agent.BoundToolCall, expected agent.ToolReuseMetadata, now time.Time) bool {
	actual, ok := state.reusableSafeRead(call, now)
	if !ok || actual.SourceInvocationID != expected.SourceInvocationID || actual.ObservedAt != expected.ObservedAt ||
		actual.ResultDigest != expected.ResultDigest || actual.PolicyGeneration != expected.PolicyGeneration ||
		actual.FreshnessWindow != expected.FreshnessWindow || len(actual.EvidenceIDs) != len(expected.EvidenceIDs) {
		return false
	}
	for index := range actual.EvidenceIDs {
		if actual.EvidenceIDs[index] != expected.EvidenceIDs[index] {
			return false
		}
	}
	return true
}

func safeReadSubject(evidence domain.Evidence) string {
	sourcePath := ""
	if evidence.SourcePath != nil {
		sourcePath = *evidence.SourcePath
	}
	return domain.SHA256Hex(string(evidence.Category) + "\x00" + evidence.Resource.APIVersion + "\x00" +
		evidence.Resource.Kind + "\x00" + evidence.Resource.Namespace + "\x00" + evidence.Resource.Name + "\x00" +
		evidence.Resource.UID + "\x00" + sourcePath + "\x00" + evidence.Series)
}
