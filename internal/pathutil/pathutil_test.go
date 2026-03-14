package pathutil

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_ValidPaths(t *testing.T) {
	base := "/home/user/project"

	tests := []struct {
		name    string
		relPath string
		want    string
	}{
		{"simple file", "file.txt", filepath.Join(base, "file.txt")},
		{"nested file", "src/main.go", filepath.Join(base, "src/main.go")},
		{"empty path resolves to base", "", base},
		{"dot resolves to base", ".", base},
		{"internal dotdot", "a/../b", filepath.Join(base, "b")},
		{"deep nesting", "a/b/c/d/e.txt", filepath.Join(base, "a/b/c/d/e.txt")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(base, tt.relPath)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolve_PathEscape(t *testing.T) {
	base := "/home/user/project"

	tests := []struct {
		name    string
		relPath string
	}{
		{"parent directory", ".."},
		{"parent then sibling", "../other"},
		{"double escape", "a/../../etc"},
		{"triple escape", "a/b/../../../etc/passwd"},
		{"sibling directory", "../other/file.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(base, tt.relPath)
			require.Error(t, err)
			assert.True(t, errors.Is(err, runtime.ErrPathEscape),
				"expected ErrPathEscape, got: %v", err)
		})
	}
}

func TestResolve_AbsolutePath(t *testing.T) {
	_, err := Resolve("/home/user/project", "/etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path must be relative")
}

func TestResolve_EmptyBaseDir(t *testing.T) {
	_, err := Resolve("", "file.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baseDir must not be empty")
}

func TestResolve_WithTempDir(t *testing.T) {
	base := t.TempDir()

	got, err := Resolve(base, "subdir/file.txt")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(base, "subdir/file.txt"), got)

	_, err = Resolve(base, "../../etc/passwd")
	require.Error(t, err)
	assert.True(t, errors.Is(err, runtime.ErrPathEscape))
}

func TestMustBeRelative(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"relative simple", "file.txt", false},
		{"relative nested", "a/b/c", false},
		{"relative dotdot", "../file", false},
		{"relative dot", ".", false},
		{"empty", "", false},
		{"absolute unix", "/etc/passwd", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MustBeRelative(tt.path)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
