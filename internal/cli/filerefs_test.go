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
	result, images, warnings := resolveFileRefs(context.Background(), "no refs here", rt)
	assert.Equal(t, "no refs here", result)
	assert.Empty(t, images)
	assert.Empty(t, warnings)
}

func TestResolveFileRefs_SingleFile(t *testing.T) {
	rt := &fileRefRuntime{
		files: map[string][]byte{
			"src/main.go": []byte("package main\n"),
		},
	}
	result, images, warnings := resolveFileRefs(context.Background(), "Look at @src/main.go please", rt)
	assert.Empty(t, warnings)
	assert.Empty(t, images)
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
	result, _, warnings := resolveFileRefs(context.Background(), "Compare @a.go and @b.py", rt)
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
	result, _, warnings := resolveFileRefs(context.Background(), "List @src", rt)
	assert.Empty(t, warnings)
	assert.Contains(t, result, "Directory listing of src:")
	assert.Contains(t, result, "main.go")
	assert.Contains(t, result, "lib/")
}

func TestResolveFileRefs_MissingFile(t *testing.T) {
	rt := &fileRefRuntime{}
	result, _, warnings := resolveFileRefs(context.Background(), "Read @nonexistent.go", rt)
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
	result, _, warnings := resolveFileRefs(context.Background(), "@x.go and @x.go", rt)
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
	result, _, warnings := resolveFileRefs(context.Background(), "@real.go and @fake.js", rt)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "@fake.js")
	assert.Contains(t, result, "Contents of real.go:")
}

func TestResolveFileRefs_ImageFile(t *testing.T) {
	// PNG magic bytes.
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00}
	rt := &fileRefRuntime{
		files: map[string][]byte{
			"screenshot.png": pngData,
		},
	}
	result, images, warnings := resolveFileRefs(context.Background(), "What's in @screenshot.png?", rt)
	assert.Empty(t, warnings)
	require.Len(t, images, 1)
	assert.Equal(t, "image/png", images[0].MimeType)
	assert.Equal(t, "screenshot.png", images[0].Path)
	assert.Contains(t, result, "[Image: screenshot.png]")
	assert.NotContains(t, result, "@screenshot.png")
}

func TestResolveFileRefs_MultipleImages(t *testing.T) {
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	rt := &fileRefRuntime{
		files: map[string][]byte{
			"v1.png": pngData,
			"v2.jpg": jpegData,
		},
	}
	result, images, warnings := resolveFileRefs(context.Background(), "Compare @v1.png and @v2.jpg", rt)
	assert.Empty(t, warnings)
	require.Len(t, images, 2)
	assert.Equal(t, "image/png", images[0].MimeType)
	assert.Equal(t, "image/jpeg", images[1].MimeType)
	assert.Contains(t, result, "[Image: v1.png]")
	assert.Contains(t, result, "[Image: v2.jpg]")
}

func TestResolveFileRefs_MixedTextAndImage(t *testing.T) {
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	rt := &fileRefRuntime{
		files: map[string][]byte{
			"main.go":    []byte("package main"),
			"design.png": pngData,
		},
	}
	result, images, warnings := resolveFileRefs(context.Background(), "Implement @main.go based on @design.png", rt)
	assert.Empty(t, warnings)
	require.Len(t, images, 1)
	assert.Equal(t, "design.png", images[0].Path)
	assert.Contains(t, result, "Contents of main.go:")
	assert.Contains(t, result, "[Image: design.png]")
}

func TestResolveFileRefs_ImageNotFound(t *testing.T) {
	rt := &fileRefRuntime{}
	result, images, warnings := resolveFileRefs(context.Background(), "Look at @missing.png", rt)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "@missing.png")
	assert.Empty(t, images)
	assert.Contains(t, result, "@missing.png")
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
