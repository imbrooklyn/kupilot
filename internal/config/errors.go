package config

import "context"

// ErrorClass is a stable, non-sensitive configuration failure category.
type ErrorClass string

const (
	ClassConfigurationInvalid ErrorClass = "configuration_invalid"
	ClassUnsupported          ErrorClass = "unsupported"
	ClassCancelled            ErrorClass = "cancelled"
	ClassInternal             ErrorClass = "internal"
)

// SafeError contains only code-defined fields suitable for terminal output and
// structured logs. Raw parser, filesystem, and environment errors are not
// retained.
type SafeError struct {
	class     ErrorClass
	code      string
	operation string
	message   string
	retryable bool
}

func newSafeError(class ErrorClass, code, operation, message string) *SafeError {
	return &SafeError{
		class:     class,
		code:      code,
		operation: operation,
		message:   message,
	}
}

func (err *SafeError) Error() string {
	if err == nil {
		return "KuPilot configuration failed."
	}
	return err.message + " (" + err.code + ")"
}

// Is preserves cancellation semantics without exposing a raw adapter cause.
func (err *SafeError) Is(target error) bool {
	return err != nil && err.class == ClassCancelled && target == context.Canceled
}

// Class returns the stable error class.
func (err *SafeError) Class() ErrorClass {
	if err == nil {
		return ClassInternal
	}
	return err.class
}

// Code returns the stable error identifier.
func (err *SafeError) Code() string {
	if err == nil {
		return "config_internal"
	}
	return err.code
}

// ErrorCode makes SafeError consumable by delivery adapters without coupling
// those adapters to this package.
func (err *SafeError) ErrorCode() string {
	return err.Code()
}

// Operation returns the code-defined operation that failed.
func (err *SafeError) Operation() string {
	if err == nil {
		return "configuration"
	}
	return err.operation
}

// Retryable reports the conservative retry classification.
func (err *SafeError) Retryable() bool {
	return err != nil && err.retryable
}

// SafeMessage returns bounded text approved for terminal output.
func (err *SafeError) SafeMessage() string {
	return err.Error()
}
