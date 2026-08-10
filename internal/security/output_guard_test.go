package security

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestOutputGuardProducesCanonicalBoundedJSON(t *testing.T) {
	t.Parallel()

	guard, err := NewOutputGuard(64)
	if err != nil {
		t.Fatalf("NewOutputGuard() error = %v", err)
	}
	raw, err := json.Marshal(struct {
		Second string `json:"second"`
		First  string `json:"first"`
	}{Second: "two", First: "one"})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	encoded, err := guard.CanonicalizeJSONObject(raw)
	if err != nil {
		t.Fatalf("CanonicalizeJSONObject() error = %v", err)
	}
	if encoded != `{"first":"one","second":"two"}` {
		t.Fatalf("CanonicalizeJSONObject() = %s", encoded)
	}

	raw, err = json.Marshal(struct {
		Value string `json:"value"`
	}{Value: strings.Repeat("x", 64)})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	_, err = guard.CanonicalizeJSONObject(raw)
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("oversized CanonicalizeJSONObject() error = %v, want ErrOutputLimit", err)
	}
}
