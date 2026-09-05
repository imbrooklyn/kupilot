// Package executor implements the restricted local-action process boundary.
// It provides exact direct argv and the separately tagged shell operation; it
// is not an OS sandbox and does not grant filesystem or network isolation.
package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const (
	maximumExecutableBytes = 256 * 1024 * 1024
	defaultWaitDelay       = time.Second
)

var ErrLocalProcess = errors.New("local process operation failed safely")

type safeError struct {
	class domain.SafeErrorClass
	code  string
}

func (err safeError) Error() string {
	return "The local process operation failed safely. (" + err.code + ")"
}
func (err safeError) Unwrap() error { return ErrLocalProcess }
func (err safeError) Class() domain.SafeErrorClass {
	if !err.class.Valid() {
		return domain.SafeErrorClassInternal
	}
	return err.class
}

// TextProcessor is the one narrow sanitation dependency permitted to receive
// bounded raw process text. Execute returns only its safe projection.
type TextProcessor interface {
	ProcessLines(string, int) (security.TextResult, error)
}

// Adapter owns executable inspection, process groups, cancellation, output
// pipes, and bounded Wait. Context is never retained.
type Adapter struct {
	text      TextProcessor
	now       func() time.Time
	waitDelay time.Duration
}

func NewAdapter(text TextProcessor, now func() time.Time) (*Adapter, error) {
	if text == nil || now == nil || !validTime(now()) {
		return nil, safeError{class: domain.SafeErrorClassConfigurationInvalid, code: "local_executor_configuration_invalid"}
	}
	return &Adapter{text: text, now: now, waitDelay: defaultWaitDelay}, nil
}

func validTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.UnixMilli() >= 0 &&
		value.Equal(time.UnixMilli(value.UnixMilli()).UTC())
}

// Inspect derives bounded identities for one exact regular executable and one
// exact directory. Every path component must resolve without a symlink.
func (adapter *Adapter) Inspect(ctx context.Context, executable, workingDirectory string) (domain.LocalExecutionObservation, error) {
	if adapter == nil || ctx == nil || ctx.Err() != nil || !domain.ValidLocalExecutionPath(executable) ||
		!domain.ValidLocalExecutionPath(workingDirectory) {
		return domain.LocalExecutionObservation{}, contextOrSafeError(ctx, domain.SafeErrorClassInvalidInput, "local_path_invalid")
	}
	executableID, err := inspectExecutable(ctx, executable)
	if err != nil {
		return domain.LocalExecutionObservation{}, err
	}
	directoryID, err := inspectDirectory(ctx, workingDirectory)
	if err != nil {
		return domain.LocalExecutionObservation{}, err
	}
	observation := domain.LocalExecutionObservation{ExecutableID: executableID, WorkingDirectoryID: directoryID}
	if observation.Validate() != nil {
		return domain.LocalExecutionObservation{}, safeError{class: domain.SafeErrorClassInternal, code: "local_path_identity_invalid"}
	}
	return observation, nil
}

// Execute performs one process attempt only after validating the complete
// envelope and re-deriving both path identities. It never retries.
func (adapter *Adapter) Execute(ctx context.Context, envelope domain.ActionEnvelope) (domain.LocalCommandResult, error) {
	if adapter == nil || ctx == nil || ctx.Err() != nil || envelope.Validate() != nil ||
		envelope.Intent.ValidateLocalCommand() != nil && envelope.Intent.ValidateShellCommand() != nil {
		return notAttempted(contextOrSafeClass(ctx, domain.SafeErrorClassInvalidInput)),
			contextOrSafeError(ctx, domain.SafeErrorClassInvalidInput, "local_execution_invalid")
	}
	if !adapter.now().Before(envelope.ExpiresAt) {
		return notAttempted(domain.SafeErrorClassPolicyDenied), safeError{class: domain.SafeErrorClassPolicyDenied, code: "local_execution_expired"}
	}
	parameters := envelope.Intent.Parameters
	observation, err := adapter.Inspect(ctx, parameters.Executable, parameters.WorkingDirectory)
	if err != nil {
		return notAttempted(classOf(err)), err
	}
	if observation.ExecutableID != parameters.ExecutableID || observation.WorkingDirectoryID != parameters.WorkingDirectoryID {
		return notAttempted(domain.SafeErrorClassConflict), safeError{class: domain.SafeErrorClassConflict, code: "local_path_changed"}
	}

	arguments := parameters.Arguments.Values()
	if envelope.Intent.Operation == domain.ActionOperationShell {
		arguments = []string{"-c", parameters.ShellCommand}
	}
	operationContext, cancel := context.WithTimeout(ctx, envelope.Intent.Limits.Timeout)
	defer cancel()
	command := exec.CommandContext(operationContext, parameters.Executable, arguments...)
	command.Dir = parameters.WorkingDirectory
	command.Env = parameters.Environment.Values()
	command.Stdin = nil
	collector := newOrderedCollector(envelope.Intent.Limits.MaximumOutput, envelope.Intent.Limits.MaximumLines)
	command.Stdout = streamWriter{collector: collector, stream: "stdout"}
	command.Stderr = streamWriter{collector: collector, stream: "stderr"}
	command.WaitDelay = adapter.waitDelay
	configureCommand(command)
	if err := operationContext.Err(); err != nil {
		return notAttempted(contextClass(err)), safeError{class: contextClass(err), code: "local_execution_cancelled_before_start"}
	}
	// Close the validation-to-start window as much as the portable os/exec API
	// permits. A later replacement race remains an explicit documented risk.
	finalObservation, err := adapter.Inspect(operationContext, parameters.Executable, parameters.WorkingDirectory)
	if err != nil {
		return notAttempted(classOf(err)), err
	}
	if finalObservation != observation {
		return notAttempted(domain.SafeErrorClassConflict), safeError{class: domain.SafeErrorClassConflict, code: "local_path_changed"}
	}
	if err := command.Start(); err != nil {
		class := contextOrSafeClass(operationContext, domain.SafeErrorClassUnavailable)
		return notAttempted(class), safeError{class: class, code: "local_process_start_failed"}
	}
	waitErr := command.Wait()
	state, class, exitCode := classifyWait(operationContext, command, waitErr)
	raw, rawTruncated := collector.result()
	processed, processErr := adapter.text.ProcessLines(string(raw), envelope.Intent.Limits.MaximumOutput)
	zeroBytes(raw)
	if processErr != nil {
		result := domain.LocalCommandResult{
			State: state, OutputDigest: domain.LocalSafeOutputDigest(""), ExitCode: exitCode, Started: true,
			Truncated: rawTruncated, ErrorClass: class,
		}
		if state == domain.LocalProcessExited {
			result.State = domain.LocalProcessOutputBlocked
			result.ErrorClass = domain.SafeErrorClassSensitiveOutputBlocked
		}
		if result.Validate(envelope.Intent.Limits) != nil {
			return notAttempted(domain.SafeErrorClassInternal), safeError{class: domain.SafeErrorClassInternal, code: "local_output_projection_invalid"}
		}
		if state != domain.LocalProcessExited {
			return result, safeError{class: class, code: waitErrorCode(state, class)}
		}
		return result, safeError{class: domain.SafeErrorClassSensitiveOutputBlocked, code: "local_output_blocked"}
	}
	lineCount := safeLineCount(processed.Value)
	result := domain.LocalCommandResult{
		State: state, SafeOutput: processed.Value, OutputDigest: domain.LocalSafeOutputDigest(processed.Value),
		ExitCode: exitCode, Started: true, Truncated: rawTruncated || processed.Truncated,
		LineCount: lineCount, ByteCount: len(processed.Value), RedactionCount: processed.RedactionCount,
		ErrorClass: class,
	}
	if result.Validate(envelope.Intent.Limits) != nil {
		return notAttempted(domain.SafeErrorClassInternal), safeError{class: domain.SafeErrorClassInternal, code: "local_output_projection_invalid"}
	}
	if waitErr != nil {
		return result, safeError{class: class, code: waitErrorCode(state, class)}
	}
	return result, nil
}

func notAttempted(class domain.SafeErrorClass) domain.LocalCommandResult {
	if !class.Valid() {
		class = domain.SafeErrorClassInternal
	}
	return domain.LocalCommandResult{
		State: domain.LocalProcessNotAttempted, OutputDigest: domain.LocalSafeOutputDigest(""), ErrorClass: class,
	}
}

func classifyWait(ctx context.Context, command *exec.Cmd, err error) (domain.LocalProcessState, domain.SafeErrorClass, int) {
	exitCode := 0
	if command != nil && command.ProcessState != nil && command.ProcessState.ExitCode() >= 0 {
		exitCode = command.ProcessState.ExitCode()
	}
	if err == nil {
		return domain.LocalProcessExited, "", exitCode
	}
	if ctx != nil && ctx.Err() != nil {
		return domain.LocalProcessUnknown, contextClass(ctx.Err()), exitCode
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ProcessState != nil && exitError.ProcessState.Exited() {
		return domain.LocalProcessFailed, domain.SafeErrorClassInvalidExternalResponse, exitCode
	}
	return domain.LocalProcessUnknown, domain.SafeErrorClassUnavailable, exitCode
}

func waitErrorCode(state domain.LocalProcessState, class domain.SafeErrorClass) string {
	if state == domain.LocalProcessFailed {
		return "local_process_exit_nonzero"
	}
	switch class {
	case domain.SafeErrorClassCancelled:
		return "local_process_cancelled"
	case domain.SafeErrorClassTimeout:
		return "local_process_timed_out"
	default:
		return "local_process_outcome_unknown"
	}
}

func contextOrSafeError(ctx context.Context, fallback domain.SafeErrorClass, code string) error {
	return safeError{class: contextOrSafeClass(ctx, fallback), code: code}
}

func contextOrSafeClass(ctx context.Context, fallback domain.SafeErrorClass) domain.SafeErrorClass {
	if ctx != nil && ctx.Err() != nil {
		return contextClass(ctx.Err())
	}
	return fallback
}

func contextClass(err error) domain.SafeErrorClass {
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.SafeErrorClassTimeout
	}
	return domain.SafeErrorClassCancelled
}

func classOf(err error) domain.SafeErrorClass {
	var classified interface{ Class() domain.SafeErrorClass }
	if errors.As(err, &classified) && classified.Class().Valid() {
		return classified.Class()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return domain.SafeErrorClassTimeout
	}
	if errors.Is(err, context.Canceled) {
		return domain.SafeErrorClassCancelled
	}
	return domain.SafeErrorClassInternal
}

func inspectExecutable(ctx context.Context, name string) (domain.ActionDigest, error) {
	file, info, err := openVerifiedPath(ctx, name, false)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Size() <= 0 || info.Size() > maximumExecutableBytes {
		return "", safeError{class: domain.SafeErrorClassPolicyDenied, code: "local_executable_denied"}
	}
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var header [2]byte
	headerBytes := 0
	for {
		if err := ctx.Err(); err != nil {
			zeroBytes(buffer)
			return "", safeError{class: contextClass(err), code: "local_executable_inspection_cancelled"}
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			if headerBytes < len(header) {
				headerBytes += copy(header[headerBytes:], buffer[:count])
			}
			_, _ = hash.Write(buffer[:count])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			zeroBytes(buffer)
			return "", safeError{class: domain.SafeErrorClassUnavailable, code: "local_executable_read_failed"}
		}
	}
	zeroBytes(buffer)
	if headerBytes == len(header) && header == [2]byte{'#', '!'} {
		return "", safeError{class: domain.SafeErrorClassPolicyDenied, code: "local_executable_script_denied"}
	}
	current, err := os.Lstat(name)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) {
		return "", safeError{class: domain.SafeErrorClassConflict, code: "local_executable_changed"}
	}
	contentHash := hex.EncodeToString(hash.Sum(nil))
	return pathIdentity("executable", name, info, contentHash), nil
}

func inspectDirectory(ctx context.Context, name string) (domain.ActionDigest, error) {
	file, info, err := openVerifiedPath(ctx, name, true)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if !info.IsDir() {
		return "", safeError{class: domain.SafeErrorClassPolicyDenied, code: "local_working_directory_denied"}
	}
	return pathIdentity("directory", name, info, ""), nil
}

func openVerifiedPath(ctx context.Context, name string, directory bool) (*os.File, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, safeError{class: contextClass(err), code: "local_path_inspection_cancelled"}
	}
	cleaned := filepath.Clean(name)
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return nil, nil, safeError{class: domain.SafeErrorClassNotFound, code: "local_path_unavailable"}
	}
	if cleaned != name || resolved != name || !filepath.IsAbs(name) {
		return nil, nil, safeError{class: domain.SafeErrorClassPolicyDenied, code: "local_path_symlink_denied"}
	}
	before, err := os.Lstat(name)
	if err != nil {
		return nil, nil, safeError{class: domain.SafeErrorClassNotFound, code: "local_path_unavailable"}
	}
	if before.Mode()&os.ModeSymlink != 0 || directory != before.IsDir() {
		return nil, nil, safeError{class: domain.SafeErrorClassPolicyDenied, code: "local_path_type_denied"}
	}
	file, err := openPathNoFollow(name, directory)
	if err != nil {
		return nil, nil, safeError{class: domain.SafeErrorClassUnavailable, code: "local_path_open_failed"}
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || opened.Mode()&os.ModeSymlink != 0 || directory != opened.IsDir() {
		_ = file.Close()
		return nil, nil, safeError{class: domain.SafeErrorClassConflict, code: "local_path_changed"}
	}
	return file, opened, nil
}

func pathIdentity(kind, name string, info os.FileInfo, contentHash string) domain.ActionDigest {
	fields := []string{
		"kupilot.local-path-identity/v1", kind, name, info.Mode().String(), strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10), platformFileIdentity(info), contentHash,
	}
	var builder strings.Builder
	for _, field := range fields {
		builder.WriteString(strconv.Itoa(len(field)))
		builder.WriteByte(':')
		builder.WriteString(field)
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return domain.ActionDigest(hex.EncodeToString(digest[:]))
}

type orderedCollector struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	maximum   int
	maxLines  int
	lines     int
	last      string
	truncated bool
}

func newOrderedCollector(maximum, maxLines int) *orderedCollector {
	return &orderedCollector{maximum: maximum, maxLines: maxLines}
}

type streamWriter struct {
	collector *orderedCollector
	stream    string
}

func (writer streamWriter) Write(value []byte) (int, error) {
	if writer.collector != nil {
		writer.collector.write(writer.stream, value)
	}
	return len(value), nil
}

func (collector *orderedCollector) write(stream string, value []byte) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(value) == 0 {
		return
	}
	prefix := []byte{}
	if collector.last != stream {
		if collector.buffer.Len() > 0 && collector.buffer.Bytes()[collector.buffer.Len()-1] != '\n' {
			prefix = append(prefix, '\n')
		}
		prefix = append(prefix, '[')
		prefix = append(prefix, stream...)
		prefix = append(prefix, ']', ' ')
		collector.last = stream
	}
	collector.append(prefix)
	collector.append(value)
}

func (collector *orderedCollector) append(value []byte) {
	for _, current := range value {
		if collector.buffer.Len() >= collector.maximum || collector.lines >= collector.maxLines && current != '\n' {
			collector.truncated = true
			continue
		}
		if collector.buffer.Len() == 0 {
			collector.lines = 1
		}
		if current == '\n' && collector.lines >= collector.maxLines {
			collector.truncated = true
			continue
		}
		_ = collector.buffer.WriteByte(current)
		if current == '\n' {
			collector.lines++
		}
	}
}

func (collector *orderedCollector) result() ([]byte, bool) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return append([]byte(nil), collector.buffer.Bytes()...), collector.truncated
}

func safeLineCount(value string) int {
	if value == "" {
		return 0
	}
	count := strings.Count(value, "\n") + 1
	if strings.HasSuffix(value, "\n") {
		count--
	}
	return count
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

var _ io.Writer = streamWriter{}
