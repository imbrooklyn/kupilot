//go:build integration

package config

import "testing"

// TestNamedModelRolesIntegrationContract exercises same-origin/different-model,
// different-origin/different-credential, and absent-credential configuration
// from one strict versioned load contract.
func TestNamedModelRolesIntegrationContract(t *testing.T) {
	TestLoadVersion2NamedProfilesAndIndependentCredentials(t)
}
