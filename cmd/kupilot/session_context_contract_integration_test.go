//go:build integration

package main

import "testing"

// TestSessionContextSQLiteIntegrationContract keeps real temporary SQLite
// replay and accepted-resume zero-I/O behavior in the tagged evidence layer.
// It persists project-owned safe rows only; no Eino object enters storage.
func TestSessionContextSQLiteIntegrationContract(t *testing.T) {
	t.Run("safe resume rows", TestSessionApplicationAdapterUsesRealSQLiteResumeEligibility)
	t.Run("accepted resume is zero external IO", TestResumeIntegrationSeparatesExplicitScopeActivationFromZeroIOAcceptance)
}
