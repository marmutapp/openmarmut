package agent

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gajaai/openmarmut-go/internal/llm"
	"github.com/gajaai/openmarmut-go/internal/runtime"
)

// MaxImageSize is the maximum allowed image file size (20 MB).
const MaxImageSize = 20 * 1024 * 1024

// imageExtensions maps file extensions to MIME types for known image formats.
var imageExtensions = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// IsImageExtension returns true if the file extension is a supported image type.
func IsImageExtension(ext string) bool {
	_, ok := imageExtensions[ext]
	return ok
}

// LoadImage reads an image file via the Runtime and returns an ImageContent.
// MIME type is detected from magic bytes, not the file extension.
func LoadImage(ctx context.Context, rt runtime.Runtime, path string) (*llm.ImageContent, error) {
	data, err := rt.ReadFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("agent.LoadImage(%s): %w", path, err)
	}

	if len(data) > MaxImageSize {
		return nil, fmt.Errorf("agent.LoadImage(%s): file too large (%d bytes, max %d)", path, len(data), MaxImageSize)
	}

	mime := detectMIME(data)
	if mime == "" {
		return nil, fmt.Errorf("agent.LoadImage(%s): unsupported image format", path)
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return &llm.ImageContent{
		Data:     encoded,
		MimeType: mime,
		Path:     path,
	}, nil
}

// LoadImageFromOS reads an image file directly from the OS filesystem.
// Used when no Runtime is available (e.g., --no-tools mode).
func LoadImageFromOS(path string) (*llm.ImageContent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agent.LoadImageFromOS(%s): %w", path, err)
	}

	if len(data) > MaxImageSize {
		return nil, fmt.Errorf("agent.LoadImageFromOS(%s): file too large (%d bytes, max %d)", path, len(data), MaxImageSize)
	}

	mime := detectMIME(data)
	if mime == "" {
		return nil, fmt.Errorf("agent.LoadImageFromOS(%s): unsupported image format", path)
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return &llm.ImageContent{
		Data:     encoded,
		MimeType: mime,
		Path:     filepath.Base(path),
	}, nil
}

// detectMIME identifies the image MIME type from magic bytes.
func detectMIME(data []byte) string {
	if len(data) < 4 {
		return ""
	}

	// PNG: 89 50 4E 47
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "image/png"
	}

	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}

	// GIF: GIF87a or GIF89a
	if len(data) >= 6 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' {
		return "image/gif"
	}

	// WebP: RIFF....WEBP
	if len(data) >= 12 && data[0] == 'R' && data[1] == 'I' && data[2] == 'F' && data[3] == 'F' &&
		data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P' {
		return "image/webp"
	}

	return ""
}
