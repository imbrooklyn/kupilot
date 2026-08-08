package logging

import "context"

// ErrorClass is a stable, non-sensitive logging failure category.
type ErrorClass string

const (
	ClassConfigurationInvalid ErrorClass = "configuration_invalid"
	ClassCancelled            ErrorClass = "cancelled"
	ClassInternal             ErrorClass = "internal"
)

// SafeError contains only code-defined logging failure information. Filesystem
// paths and raw operating-system errors are deliberately discarded.
type SafeError struct {
	class   ErrorClass
	code    string
	message string
}

func newSafeError(class ErrorClass, code, message string) *SafeError {
	return &SafeError{class: class, code: code, message: message}
}

func (err *SafeError) Error() string {
	if err == nil {
		return "KuPilot logging failed."
	}
	return err.message + " (" + err.code + ")"
}

// Is preserves cancellation semantics without exposing a raw filesystem cause.
func (err *SafeError) Is(target error) bool {
	return err != nil && err.class == ClassCancelled && target == context.Canceled
}

// Class returns the stable failure class.
func (err *SafeError) Class() ErrorClass {
	if err == nil {
		return ClassInternal
	}
	return err.class
}

// Code returns the stable failure identifier.
func (err *SafeError) Code() string {
	if err == nil {
		return "logging_internal"
	}
	return err.code
}

// ErrorCode makes SafeError consumable by delivery adapters without coupling.
func (err *SafeError) ErrorCode() string {
	return err.Code()
}

// SafeMessage returns bounded text approved for terminal output.
func (err *SafeError) SafeMessage() string {
	return err.Error()
}
