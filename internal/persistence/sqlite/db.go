// Package sqlite provides KuPilot's local SQLite persistence adapter.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const (
	driverName          = "sqlite"
	databaseFilename    = "kupilot.db"
	maxOpenConnections  = 1
	maxPathBytes        = 4096
	maxVersionBytes     = 64
	maxCorrelationBytes = 128
)

var knownSidecarSuffixes = [...]string{"-journal", "-wal", "-shm"}

// ErrorClass is a stable, non-sensitive storage failure category.
type ErrorClass string

const (
	ClassConfigurationInvalid   ErrorClass = "configuration_invalid"
	ClassPolicyDenied           ErrorClass = "policy_denied"
	ClassPersistenceUnavailable ErrorClass = "persistence_unavailable"
	ClassCancelled              ErrorClass = "cancelled"
	ClassTimeout                ErrorClass = "timeout"
)

type errorCause uint8

const (
	causeNone errorCause = iota
	causeCancelled
	causeTimeout
)

// Error is a bounded storage error that never includes SQL, bind values,
// driver text, or local paths.
type Error struct {
	class         ErrorClass
	code          string
	operation     string
	message       string
	correlationID string
	retryable     bool
	cause         errorCause
}

// Error returns only code-defined safe text and a validated correlation ID.
func (err *Error) Error() string {
	if err == nil {
		return "KuPilot local storage failed. (storage_internal)"
	}
	return err.message + " (" + err.code + "; correlation=" + err.correlationID + ")"
}

// Is preserves cancellation and deadline semantics without exposing a raw
// filesystem, SQL, or driver error.
func (err *Error) Is(target error) bool {
	if err == nil {
		return false
	}
	return err.cause == causeCancelled && target == context.Canceled ||
		err.cause == causeTimeout && target == context.DeadlineExceeded
}

// Class returns the stable error class.
func (err *Error) Class() ErrorClass {
	if err == nil {
		return ClassPersistenceUnavailable
	}
	return err.class
}

// Code returns the stable storage error code.
func (err *Error) Code() string {
	if err == nil {
		return "storage_internal"
	}
	return err.code
}

// Operation returns the code-defined operation name.
func (err *Error) Operation() string {
	if err == nil {
		return "local_storage"
	}
	return err.operation
}

// CorrelationID returns the validated caller-owned correlation identifier.
func (err *Error) CorrelationID() string {
	if err == nil {
		return "storage"
	}
	return err.correlationID
}

// Retryable reports the conservative retry classification.
func (err *Error) Retryable() bool {
	return err != nil && err.retryable
}

// SafeMessage returns bounded text approved for delivery and logging sinks.
func (err *Error) SafeMessage() string {
	return err.Error()
}

// OpenOptions contains the complete project-owned input for opening storage.
type OpenOptions struct {
	StateDir           string
	ApplicationVersion string
	CorrelationID      string
}

// DB owns the private sqlx handle for the SQLite adapter.
type DB struct {
	mu            sync.Mutex
	handle        *sqlx.DB
	correlationID string
	stateDir      string
	databasePath  string
	closed        bool
}

// Open validates the fixed storage path, opens the selected SQLite driver,
// applies connection policy, and migrates the schema.
func Open(ctx context.Context, options OpenOptions) (_ *DB, returnErr error) {
	if err := contextFailure(ctx, "open_storage", options.CorrelationID); err != nil {
		return nil, err
	}
	if !validMetadata(options.ApplicationVersion, maxVersionBytes) ||
		!validMetadata(options.CorrelationID, maxCorrelationBytes) {
		return nil, newError(
			ClassConfigurationInvalid,
			"storage_metadata_invalid",
			"open_storage",
			"KuPilot storage metadata is invalid.",
			"storage",
			nil,
		)
	}
	if !validStateDirectory(options.StateDir) {
		return nil, newError(
			ClassConfigurationInvalid,
			"storage_path_invalid",
			"validate_storage_path",
			"The KuPilot state directory is invalid.",
			options.CorrelationID,
			nil,
		)
	}

	databasePath, existingFiles, err := prepareStoragePath(ctx, options.StateDir, options.CorrelationID)
	if err != nil {
		return nil, err
	}
	registerSQLXBindType()
	raw, err := sql.Open(driverName, databaseURI(databasePath))
	if err != nil {
		return nil, newError(
			ClassPersistenceUnavailable,
			"storage_open_failed",
			"open_storage",
			"KuPilot local storage is unavailable.",
			options.CorrelationID,
			err,
		)
	}
	handle := sqlx.NewDb(raw, driverName)
	handle.SetMaxOpenConns(maxOpenConnections)
	handle.SetMaxIdleConns(maxOpenConnections)
	db := &DB{
		handle:        handle,
		correlationID: options.CorrelationID,
		stateDir:      options.StateDir,
		databasePath:  databasePath,
	}
	defer func() {
		if returnErr != nil {
			_ = handle.Close()
		}
	}()

	if err := handle.PingContext(ctx); err != nil {
		return nil, newError(
			ClassPersistenceUnavailable,
			"storage_open_failed",
			"open_storage",
			"KuPilot local storage is unavailable.",
			options.CorrelationID,
			err,
		)
	}
	if err := verifyIntegrity(ctx, handle, options.CorrelationID); err != nil {
		return nil, err
	}
	if err := migrate(ctx, handle, options.ApplicationVersion, options.CorrelationID); err != nil {
		return nil, err
	}
	if err := activateJournalMode(ctx, handle, options.CorrelationID); err != nil {
		return nil, err
	}
	if err := verifyConnectionPolicy(ctx, handle, options.CorrelationID); err != nil {
		return nil, err
	}
	if err := verifyIntegrity(ctx, handle, options.CorrelationID); err != nil {
		return nil, err
	}
	if err := secureKnownStorageFiles(options.StateDir, databasePath, existingFiles, options.CorrelationID); err != nil {
		return nil, err
	}
	return db, nil
}

// Close releases the private database handle.
func (db *DB) Close() error {
	if db == nil || db.handle == nil {
		return nil
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.closeLocked()
}

func (db *DB) closeLocked() error {
	if db.closed {
		return nil
	}
	db.closed = true
	if err := db.handle.Close(); err != nil {
		return newError(
			ClassPersistenceUnavailable,
			"storage_close_failed",
			"close_storage",
			"KuPilot could not close local storage cleanly.",
			db.correlationID,
			err,
		)
	}
	return nil
}

// DeleteAllLocalState closes storage and removes only the validated database
// and known SQLite sidecars. It never removes a directory or follows a link.
func (db *DB) DeleteAllLocalState(ctx context.Context) (application.LocalStateDeletionResult, error) {
	if db == nil || db.handle == nil {
		return application.LocalStateDeletionResult{}, newError(
			ClassConfigurationInvalid,
			"storage_delete_invalid",
			"delete_all_local_state",
			"KuPilot local storage cannot be deleted safely.",
			"storage",
			nil,
		)
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return application.LocalStateDeletionResult{StorageClosed: true}, newError(
			ClassPersistenceUnavailable,
			"storage_already_closed",
			"delete_all_local_state",
			"KuPilot local storage is already closed.",
			db.correlationID,
			nil,
		)
	}
	if err := contextFailure(ctx, "delete_all_local_state", db.correlationID); err != nil {
		return application.LocalStateDeletionResult{}, err
	}
	if err := db.validateDeletionPaths(); err != nil {
		return application.LocalStateDeletionResult{}, pathError(db.correlationID, err)
	}
	if err := db.closeLocked(); err != nil {
		return application.LocalStateDeletionResult{StorageClosed: true}, err
	}

	var removalErrors []error
	for _, path := range knownStoragePaths(db.databasePath) {
		if err := validateKnownStorageFile(path); err != nil {
			removalErrors = append(removalErrors, err)
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			removalErrors = append(removalErrors, err)
		}
	}
	if len(removalErrors) != 0 {
		return application.LocalStateDeletionResult{StorageClosed: true}, newError(
			ClassPersistenceUnavailable,
			"storage_delete_incomplete",
			"delete_all_local_state",
			"KuPilot closed local storage but could not remove every database file.",
			db.correlationID,
			errors.Join(removalErrors...),
		)
	}
	return application.LocalStateDeletionResult{StorageClosed: true, Complete: true}, nil
}

func (db *DB) validateDeletionPaths() error {
	if !validStateDirectory(db.stateDir) || db.databasePath != filepath.Join(db.stateDir, databaseFilename) {
		return os.ErrInvalid
	}
	if err := rejectSymlinkedPath(db.stateDir); err != nil {
		return err
	}
	info, err := os.Lstat(db.stateDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err != nil {
			return err
		}
		return os.ErrInvalid
	}
	for _, path := range knownStoragePaths(db.databasePath) {
		if err := validateKnownStorageFile(path); err != nil {
			return err
		}
	}
	return nil
}

func registerSQLXBindType() {
	sqlx.BindDriver(driverName, sqlx.QUESTION)
}

func databaseURI(databasePath string) string {
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := uri.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "secure_delete(FAST)")
	query.Add("_pragma", "synchronous(NORMAL)")
	query.Set("_txlock", "immediate")
	uri.RawQuery = query.Encode()
	return uri.String()
}

func activateJournalMode(ctx context.Context, db *sqlx.DB, correlationID string) error {
	var mode string
	if err := db.GetContext(ctx, &mode, `PRAGMA journal_mode = WAL`); err != nil {
		return newError(
			ClassPersistenceUnavailable,
			"storage_pragma_failed",
			"configure_storage",
			"KuPilot could not apply its local storage policy.",
			correlationID,
			err,
		)
	}
	if !strings.EqualFold(mode, "wal") {
		return newError(
			ClassPersistenceUnavailable,
			"storage_pragma_failed",
			"configure_storage",
			"KuPilot could not verify its local storage policy.",
			correlationID,
			nil,
		)
	}
	return nil
}

func verifyConnectionPolicy(ctx context.Context, db *sqlx.DB, correlationID string) error {
	checks := []struct {
		query string
		want  string
	}{
		{query: `PRAGMA foreign_keys`, want: "1"},
		{query: `PRAGMA busy_timeout`, want: "5000"},
		{query: `PRAGMA journal_mode`, want: "wal"},
		{query: `PRAGMA synchronous`, want: "1"},
		{query: `PRAGMA secure_delete`, want: "2"},
	}
	for _, check := range checks {
		var got string
		if err := db.GetContext(ctx, &got, check.query); err != nil {
			return newError(
				ClassPersistenceUnavailable,
				"storage_pragma_failed",
				"configure_storage",
				"KuPilot could not apply its local storage policy.",
				correlationID,
				err,
			)
		}
		if !strings.EqualFold(got, check.want) {
			return newError(
				ClassPersistenceUnavailable,
				"storage_pragma_failed",
				"configure_storage",
				"KuPilot could not verify its local storage policy.",
				correlationID,
				nil,
			)
		}
	}
	return nil
}

func verifyIntegrity(ctx context.Context, db *sqlx.DB, correlationID string) error {
	var result string
	if err := db.GetContext(ctx, &result, `PRAGMA quick_check`); err != nil {
		return newError(
			ClassPersistenceUnavailable,
			"storage_integrity_failed",
			"verify_storage_integrity",
			"KuPilot could not verify local storage integrity.",
			correlationID,
			err,
		)
	}
	if result != "ok" {
		return newError(
			ClassPersistenceUnavailable,
			"storage_integrity_failed",
			"verify_storage_integrity",
			"KuPilot local storage failed its integrity check.",
			correlationID,
			nil,
		)
	}
	return nil
}

func prepareStoragePath(ctx context.Context, stateDir, correlationID string) (string, map[string]os.FileInfo, error) {
	if err := contextFailure(ctx, "prepare_storage_path", correlationID); err != nil {
		return "", nil, err
	}
	if err := rejectSymlinkedPath(stateDir); err != nil {
		return "", nil, pathError(correlationID, err)
	}
	_, beforeErr := os.Lstat(stateDir)
	createdStateDir := errors.Is(beforeErr, os.ErrNotExist)
	if beforeErr != nil && !createdStateDir {
		return "", nil, pathError(correlationID, beforeErr)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", nil, permissionError(correlationID, err)
	}
	if err := rejectSymlinkedPath(stateDir); err != nil {
		return "", nil, pathError(correlationID, err)
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() {
		return "", nil, pathError(correlationID, err)
	}
	if createdStateDir {
		if err := os.Chmod(stateDir, 0o700); err != nil {
			return "", nil, permissionError(correlationID, err)
		}
	}

	databasePath := filepath.Join(stateDir, databaseFilename)
	existingFiles := make(map[string]os.FileInfo, len(knownSidecarSuffixes)+1)
	for _, path := range knownStoragePaths(databasePath) {
		if err := validateKnownStorageFile(path); err != nil {
			return "", nil, pathError(correlationID, err)
		}
		if existing, statErr := os.Lstat(path); statErr == nil {
			existingFiles[path] = existing
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", nil, pathError(correlationID, statErr)
		}
	}
	file, err := os.OpenFile(databasePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	createdDatabase := err == nil
	if createdDatabase {
		if closeErr := file.Close(); closeErr != nil {
			return "", nil, permissionError(correlationID, closeErr)
		}
	} else if !errors.Is(err, os.ErrExist) {
		return "", nil, permissionError(correlationID, err)
	}
	if createdDatabase {
		if err := os.Chmod(databasePath, 0o600); err != nil {
			return "", nil, permissionError(correlationID, err)
		}
	}
	if err := contextFailure(ctx, "prepare_storage_path", correlationID); err != nil {
		return "", nil, err
	}
	return databasePath, existingFiles, nil
}

func secureKnownStorageFiles(
	stateDir,
	databasePath string,
	existingFiles map[string]os.FileInfo,
	correlationID string,
) error {
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return pathError(correlationID, err)
	}
	for _, path := range knownStoragePaths(databasePath) {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return pathError(correlationID, err)
		}
		if existing, ok := existingFiles[path]; !ok || !os.SameFile(existing, info) {
			if err := os.Chmod(path, 0o600); err != nil {
				return permissionError(correlationID, err)
			}
		}
	}
	return nil
}

func knownStoragePaths(databasePath string) []string {
	paths := make([]string, 0, 1+len(knownSidecarSuffixes))
	paths = append(paths, databasePath)
	for _, suffix := range knownSidecarSuffixes {
		paths = append(paths, databasePath+suffix)
	}
	return paths
}

func validateKnownStorageFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return os.ErrInvalid
	}
	return nil
}

func rejectSymlinkedPath(path string) error {
	current := filepath.Clean(path)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return os.ErrInvalid
			}
			if !info.IsDir() {
				return os.ErrInvalid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func validStateDirectory(value string) bool {
	if value == "" || len(value) > maxPathBytes || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return false
	}
	return filepath.Dir(value) != value
}

func validMetadata(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes {
		return false
	}
	for _, current := range value {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' || current >= '0' && current <= '9' {
			continue
		}
		switch current {
		case '.', '_', '-', ':', '+':
			continue
		default:
			return false
		}
	}
	return true
}

func contextFailure(ctx context.Context, operation, correlationID string) error {
	if ctx == nil {
		return newError(
			ClassConfigurationInvalid,
			"storage_context_invalid",
			operation,
			"KuPilot requires a valid storage context.",
			correlationID,
			nil,
		)
	}
	if err := ctx.Err(); err != nil {
		return newError(
			ClassCancelled,
			"storage_cancelled",
			operation,
			"The local storage operation was cancelled.",
			correlationID,
			err,
		)
	}
	return nil
}

func pathError(correlationID string, cause error) error {
	return newError(
		ClassPolicyDenied,
		"storage_path_unsafe",
		"validate_storage_path",
		"KuPilot refused an unsafe local storage path.",
		correlationID,
		cause,
	)
}

func permissionError(correlationID string, cause error) error {
	return newError(
		ClassPersistenceUnavailable,
		"storage_permissions_failed",
		"secure_storage_files",
		"KuPilot could not secure its local storage files.",
		correlationID,
		cause,
	)
}

func newError(class ErrorClass, code, operation, message, correlationID string, cause error) *Error {
	if !validMetadata(correlationID, maxCorrelationBytes) {
		correlationID = "storage"
	}
	result := &Error{
		class:         class,
		code:          code,
		operation:     operation,
		message:       message,
		correlationID: correlationID,
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		result.class = ClassTimeout
		result.code = "storage_timeout"
		result.message = "The local storage operation timed out."
		result.cause = causeTimeout
	} else if errors.Is(cause, context.Canceled) {
		result.class = ClassCancelled
		result.code = "storage_cancelled"
		result.message = "The local storage operation was cancelled."
		result.cause = causeCancelled
	}
	return result
}
