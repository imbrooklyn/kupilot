package security

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

var (
	// ErrOutputLimit reports a serialized value that exceeds its fixed ceiling.
	ErrOutputLimit = errors.New("serialized output exceeds its fixed limit")
	// ErrInvalidOutputGuard reports an invalid byte ceiling or JSON value.
	ErrInvalidOutputGuard = errors.New("output guard policy is invalid")
)

// OutputGuard canonicalizes one project-owned JSON object and enforces an
// exact serialized byte ceiling.
type OutputGuard struct {
	maximumBytes int
}

// NewOutputGuard validates one non-expanding caller-owned ceiling.
func NewOutputGuard(maximumBytes int) (OutputGuard, error) {
	if maximumBytes < 2 {
		return OutputGuard{}, ErrInvalidOutputGuard
	}
	return OutputGuard{maximumBytes: maximumBytes}, nil
}

// CanonicalizeJSONObject validates caller-serialized project data and returns
// compact JSON with recursively sorted object keys.
func (guard OutputGuard) CanonicalizeJSONObject(encoded []byte) (string, error) {
	if guard.maximumBytes < 2 {
		return "", ErrInvalidOutputGuard
	}
	if len(encoded) < 2 || encoded[0] != '{' || encoded[len(encoded)-1] != '}' {
		return "", ErrInvalidOutputGuard
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return "", ErrInvalidOutputGuard
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", ErrInvalidOutputGuard
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || len(canonical) < 2 || canonical[0] != '{' || canonical[len(canonical)-1] != '}' ||
		strings.TrimSpace(string(canonical)) != string(canonical) {
		return "", ErrInvalidOutputGuard
	}
	if len(canonical) > guard.maximumBytes {
		return "", ErrOutputLimit
	}
	return string(canonical), nil
}

// Allows reports whether an already-measured complete envelope fits.
func (guard OutputGuard) Allows(serializedBytes int) bool {
	return guard.maximumBytes >= 2 && serializedBytes >= 0 && serializedBytes <= guard.maximumBytes
}
