package application

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrInvalidSessionSearch reports an invalid or expanding local discovery request.
	ErrInvalidSessionSearch = errors.New("the Session search request is invalid")
)

// SessionSearchRequest is one bounded literal match over safe display metadata.
type SessionSearchRequest struct {
	Filter string
	Limit  int
}

// Validate checks the normalized literal filter and hard result bound.
func (request SessionSearchRequest) Validate() error {
	if request.Limit < 1 || request.Limit > MaxUIQueryCandidates || len(request.Filter) > maxUIFilterBytes ||
		!utf8.ValidString(request.Filter) || request.Filter != strings.TrimSpace(request.Filter) {
		return ErrInvalidSessionSearch
	}
	for _, current := range request.Filter {
		if unicode.IsControl(current) || unicode.In(current, unicode.Cf) {
			return ErrInvalidSessionSearch
		}
	}
	return nil
}

// SessionSearchReader returns already-ranked safe resume metadata and no content preview.
type SessionSearchReader interface {
	SearchResumable(context.Context, SessionSearchRequest) ([]ResumeSessionRecord, error)
}
