package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/application"
)

const exportTemporaryPrefix = ".kupilot-export-"

var (
	// ErrInvalidExportTarget reports a target that is not an explicit clean absolute Markdown path.
	ErrInvalidExportTarget = errors.New("the export target is invalid")
	// ErrExportPathUnsafe reports a symlink, directory, or unsafe permission boundary.
	ErrExportPathUnsafe = errors.New("the export path is unsafe")
	// ErrExportTargetExists reports the default no-overwrite outcome.
	ErrExportTargetExists = errors.New("the export target already exists")
	// ErrExportWriteFailed reports a content-free local publication failure.
	ErrExportWriteFailed = errors.New("the Session summary could not be written")
)

// ExportWriter publishes one bounded Application projection to a private local file.
type ExportWriter struct {
	syncFile func(*os.File) error
}

// NewExportWriter constructs the stateless narrow filesystem adapter.
func NewExportWriter() *ExportWriter {
	return &ExportWriter{}
}

// WriteSummary validates the target and atomically publishes a same-directory temporary file.
func (writer *ExportWriter) WriteSummary(ctx context.Context, request application.ExportFile) (returnErr error) {
	if ctx == nil || request.Validate() != nil || !validExportTarget(request.TargetPath) {
		return ErrInvalidExportTarget
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parent := filepath.Dir(request.TargetPath)
	if err := validateExportParent(parent); err != nil {
		return err
	}
	if err := validateAbsentExportTarget(request.TargetPath); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(parent, exportTemporaryPrefix)
	if err != nil {
		return ErrExportWriteFailed
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		if !published {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return ErrExportWriteFailed
	}
	if err := validateOpenedExportFile(temporaryPath, temporary); err != nil {
		return err
	}
	for offset := 0; offset < len(request.Content); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+64*1024, len(request.Content))
		written, writeErr := temporary.Write(request.Content[offset:end])
		if writeErr != nil || written != end-offset {
			return ErrExportWriteFailed
		}
		offset = end
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	syncFile := func(file *os.File) error { return file.Sync() }
	if writer != nil && writer.syncFile != nil {
		syncFile = writer.syncFile
	}
	if err := syncFile(temporary); err != nil {
		return ErrExportWriteFailed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		temporary = nil
		return ErrExportWriteFailed
	}
	temporary = nil
	if err := validateAbsentExportTarget(request.TargetPath); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := renameExportNoReplace(temporaryPath, request.TargetPath); err != nil {
		if isExportTargetExists(err) {
			return ErrExportTargetExists
		}
		return ErrExportWriteFailed
	}
	published = true
	if err := validatePublishedExportFile(request.TargetPath); err != nil {
		return err
	}
	if err := syncExportDirectory(parent); err != nil {
		return ErrExportWriteFailed
	}
	return nil
}

func validExportTarget(target string) bool {
	if target == "" || !utf8.ValidString(target) || strings.ContainsRune(target, 0) || !filepath.IsAbs(target) ||
		filepath.Clean(target) != target || filepath.Ext(target) != ".md" {
		return false
	}
	for _, character := range target {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			return false
		}
	}
	root := filepath.VolumeName(target) + string(os.PathSeparator)
	return target != root && filepath.Dir(target) != root && filepath.Base(target) != "." && filepath.Base(target) != string(os.PathSeparator)
}

func validateExportParent(parent string) error {
	if err := rejectExportSymlinkComponents(parent); err != nil {
		return err
	}
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrExportPathUnsafe
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return ErrExportPathUnsafe
	}
	return nil
}

func rejectExportSymlinkComponents(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	current := volume + string(os.PathSeparator)
	relative := strings.TrimPrefix(clean, current)
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		writableByOthers := runtime.GOOS != "windows" && info != nil && info.Mode().Perm()&0o022 != 0 &&
			info.Mode()&os.ModeSticky == 0
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || writableByOthers {
			return ErrExportPathUnsafe
		}
	}
	return nil
}

func validateAbsentExportTarget(target string) error {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrExportPathUnsafe
	}
	return ErrExportTargetExists
}

func validateOpenedExportFile(path string, file *os.File) error {
	pathInfo, pathErr := os.Lstat(path)
	openedInfo, openedErr := file.Stat()
	if pathErr != nil || openedErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !openedInfo.Mode().IsRegular() ||
		runtime.GOOS != "windows" && openedInfo.Mode().Perm() != 0o600 || !os.SameFile(pathInfo, openedInfo) {
		return ErrExportPathUnsafe
	}
	return nil
}

func validatePublishedExportFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%w", ErrExportPathUnsafe)
	}
	return nil
}
