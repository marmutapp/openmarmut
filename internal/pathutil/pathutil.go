package pathutil

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gajaai/opencode-go/internal/runtime"
)

// Resolve safely resolves a relative path against a base directory.
// It rejects absolute paths and paths that escape the base directory.
func Resolve(baseDir, relPath string) (string, error) {
	if baseDir == "" {
		return "", fmt.Errorf("pathutil.Resolve: baseDir must not be empty")
	}

	if err := MustBeRelative(relPath); err != nil {
		return "", fmt.Errorf("pathutil.Resolve(%s): %w", relPath, err)
	}

	joined := filepath.Join(baseDir, relPath)
	resolved := filepath.Clean(joined)
	absBase := filepath.Clean(baseDir)

	if resolved == absBase {
		return resolved, nil
	}

	if !strings.HasPrefix(resolved, absBase+string(filepath.Separator)) {
		return "", fmt.Errorf("pathutil.Resolve(%s): %w", relPath, runtime.ErrPathEscape)
	}

	return resolved, nil
}

// MustBeRelative returns an error if the given path is absolute.
func MustBeRelative(path string) error {
	if filepath.IsAbs(path) {
		return fmt.Errorf("path must be relative, got absolute: %s", path)
	}
	return nil
}
