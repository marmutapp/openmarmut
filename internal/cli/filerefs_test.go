package cli

import (
	"context"
	"os"
	"testing"

	"github.com/gajaai/openmarmut-go/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileRefRuntime is a stub runtime that returns canned file contents and listings.
type fileRefRuntime struct {
	stubRuntime
	files map[string][]byte
	dirs  map[string][]runtime.FileEntry
}

func (r *fileRefRuntime) ReadFile(_ context.Context, path string) ([]byte, error) {
	if data, ok := r.files[path]; ok {
		return data, nil
	}
	return nil, os.ErrNotExist
}

func (r *fileRefRuntime) ListDir(_ context.Context, path string) ([]runtime.FileEntry, error) {
	if entries, ok := r.dirs[path]; ok {
		return entries, nil
	}
	return nil, os.ErrNotExist
}

func TestResolveFileRefs_NoRefs(t *testing.T) {
	rt := &fileRefRuntime{}
	result, warnings := resolveFileRefs(context.Background(), "no refs here", rt)
	assert.Equal(t, "no refs here", result)
	assert.Empty(t, warnings)
}

func TestResolveFileRefs_SingleFile(t *testing.T) {
	rt := &fileRefRuntime{
		files: map[string][]byte{
			"src/main.go": []byte("package main\n"),
		},
	}
	result, warnings := resolveFileRefs(context.Background(), "Look at @src/main.go please", rt)
	assert.Empty(t, warnings)
	assert.Contains(t, result, "Contents of src/main.go:")
	assert.Contains(t, result, "```go")
	assert.Contains(t, result, "package main")
	assert.Contains(t, result, "please")
}

func TestResolveFileRefs_MultipleFiles(t *testing.T) {
	rt := &fileRefRuntime{
		files: map[string][]byte{
			"a.go": []byte("package a"),
			"b.py": []byte("print('hello')"),
		},
	}
	result, warnings := resolveFileRefs(context.Background(), "Compare @a.go and @b.py", rt)
	assert.Empty(t, warnings)
	assert.Contains(t, result, "Contents of a.go:")
	assert.Contains(t, result, "```go")
	assert.Contains(t, result, "Contents of b.py:")
	assert.Contains(t, result, "```python")
}

func TestResolveFileRefs_Directory(t *testing.T) {
	rt := &fileRefRuntime{
		dirs: map[string][]runtime.FileEntry{
			"src": {
				{Name: "main.go", IsDir: false},
				{Name: "lib", IsDir: true},
			},
		},
	}
	result, warnings := resolveFileRefs(context.Background(), "List @src", rt)
	assert.Empty(t, warnings)
	assert.Contains(t, result, "Directory listing of src:")
	assert.Contains(t, result, "main.go")
	assert.Contains(t, result, "lib/")
}

func TestResolveFileRefs_MissingFile(t *testing.T) {
	rt := &fileRefRuntime{}
	result, warnings := resolveFileRefs(context.Background(), "Read @nonexistent.go", rt)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "file not found")
	assert.Contains(t, result, "@nonexistent.go")
}

func TestResolveFileRefs_DuplicateRef(t *testing.T) {
	rt := &fileRefRuntime{
		files: map[string][]byte{
			"x.go": []byte("code"),
		},
	}
	result, warnings := resolveFileRefs(context.Background(), "@x.go and @x.go", rt)
	assert.Empty(t, warnings)
	// First occurrence should be expanded, second kept as-is.
	assert.Contains(t, result, "Contents of x.go:")
	assert.Contains(t, result, "@x.go")
}

func TestResolveFileRefs_MixedExistAndMissing(t *testing.T) {
	rt := &fileRefRuntime{
		files: map[string][]byte{
			"real.go": []byte("package real"),
		},
	}
	result, warnings := resolveFileRefs(context.Background(), "@real.go and @fake.js", rt)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "@fake.js")
	assert.Contains(t, result, "Contents of real.go:")
}

func TestFileRefPattern_Matches(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"@src/main.go", []string{"src/main.go"}},
		{"@file.py @other.js", []string{"file.py", "other.js"}},
		{"no refs", nil},
		{"email@example.com", []string{"example.com"}},
		{"@path/to/deep/file.ts", []string{"path/to/deep/file.ts"}},
		{"@config.yaml", []string{"config.yaml"}},
		{"@_underscore.go", []string{"_underscore.go"}},
		{"@file-with-dash.rs", []string{"file-with-dash.rs"}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			matches := fileRefPattern.FindAllStringSubmatch(tt.input, -1)
			var paths []string
			for _, m := range matches {
				paths = append(paths, m[1])
			}
			assert.Equal(t, tt.expected, paths)
		})
	}
}

func TestLookupLang(t *testing.T) {
	tests := []struct {
		ext  string
		lang string
	}{
		{".go", "go"},
		{".py", "python"},
		{".js", "javascript"},
		{".ts", "typescript"},
		{".tsx", "tsx"},
		{".rs", "rust"},
		{".unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			assert.Equal(t, tt.lang, lookupLang(tt.ext))
		})
	}
}
