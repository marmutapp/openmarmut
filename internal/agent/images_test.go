package agent

import (
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/marmutapp/openmarmut/internal/localrt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newImageTestRuntime(t *testing.T) (*localrt.LocalRuntime, string) {
	t.Helper()
	dir := t.TempDir()
	rt := localrt.New(dir, 10*time.Second, slog.Default())
	require.NoError(t, rt.Init(context.Background()))
	return rt, dir
}

func TestDetectMIME_PNG(t *testing.T) {
	data := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	assert.Equal(t, "image/png", detectMIME(data))
}

func TestDetectMIME_JPEG(t *testing.T) {
	data := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}
	assert.Equal(t, "image/jpeg", detectMIME(data))
}

func TestDetectMIME_GIF(t *testing.T) {
	data := []byte("GIF89a" + "dummy")
	assert.Equal(t, "image/gif", detectMIME(data))
}

func TestDetectMIME_WebP(t *testing.T) {
	data := []byte("RIFF\x00\x00\x00\x00WEBP")
	assert.Equal(t, "image/webp", detectMIME(data))
}

func TestDetectMIME_Unknown(t *testing.T) {
	assert.Equal(t, "", detectMIME([]byte{0x00, 0x01, 0x02, 0x03}))
}

func TestDetectMIME_TooShort(t *testing.T) {
	assert.Equal(t, "", detectMIME([]byte{0x89}))
	assert.Equal(t, "", detectMIME(nil))
}

func TestLoadImage_PNG(t *testing.T) {
	rt, dir := newImageTestRuntime(t)
	defer rt.Close(context.Background()) //nolint:errcheck

	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.png"), pngData, 0644))

	img, err := LoadImage(context.Background(), rt, "test.png")
	require.NoError(t, err)
	assert.Equal(t, "image/png", img.MimeType)
	assert.Equal(t, "test.png", img.Path)

	decoded, err := base64.StdEncoding.DecodeString(img.Data)
	require.NoError(t, err)
	assert.Equal(t, pngData, decoded)
}

func TestLoadImage_JPEG(t *testing.T) {
	rt, dir := newImageTestRuntime(t)
	defer rt.Close(context.Background()) //nolint:errcheck

	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "photo.jpg"), jpegData, 0644))

	img, err := LoadImage(context.Background(), rt, "photo.jpg")
	require.NoError(t, err)
	assert.Equal(t, "image/jpeg", img.MimeType)
}

func TestLoadImage_NotFound(t *testing.T) {
	rt, _ := newImageTestRuntime(t)
	defer rt.Close(context.Background()) //nolint:errcheck

	_, err := LoadImage(context.Background(), rt, "missing.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent.LoadImage")
}

func TestLoadImage_UnsupportedFormat(t *testing.T) {
	rt, dir := newImageTestRuntime(t)
	defer rt.Close(context.Background()) //nolint:errcheck

	require.NoError(t, os.WriteFile(filepath.Join(dir, "data.bin"), []byte("not an image file"), 0644))

	_, err := LoadImage(context.Background(), rt, "data.bin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported image format")
}

func TestLoadImage_TooLarge(t *testing.T) {
	rt, dir := newImageTestRuntime(t)
	defer rt.Close(context.Background()) //nolint:errcheck

	data := make([]byte, MaxImageSize+1)
	data[0] = 0x89
	data[1] = 0x50
	data[2] = 0x4E
	data[3] = 0x47
	require.NoError(t, os.WriteFile(filepath.Join(dir, "huge.png"), data, 0644))

	_, err := LoadImage(context.Background(), rt, "huge.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "file too large")
}

func TestIsImageExtension(t *testing.T) {
	tests := []struct {
		ext  string
		want bool
	}{
		{".png", true},
		{".jpg", true},
		{".jpeg", true},
		{".gif", true},
		{".webp", true},
		{".go", false},
		{".txt", false},
		{".pdf", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			assert.Equal(t, tt.want, IsImageExtension(tt.ext))
		})
	}
}
