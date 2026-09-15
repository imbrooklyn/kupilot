package agent

import (
	"errors"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// InteractionError preserves an internal cause while exporting only a fixed
// classification through its formatting and Failure method.
type InteractionError struct {
	failure domain.InteractionFailure
	cause   error
}

func (err *InteractionError) Error() string                      { return err.failure.SafeMessage() }
func (err *InteractionError) Unwrap() error                      { return err.cause }
func (err *InteractionError) Failure() domain.InteractionFailure { return err.failure }

func interactionError(failure domain.InteractionFailure, cause error) error {
	return &InteractionError{failure: failure, cause: cause}
}

// InteractionFailureOf extracts only project-owned diagnostic metadata.
func InteractionFailureOf(err error, fallback domain.InteractionFailure) domain.InteractionFailure {
	var failure *InteractionError
	if errors.As(err, &failure) {
		return failure.Failure()
	}
	return fallback
}
