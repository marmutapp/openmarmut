package logger

import (
	"io"
	"log/slog"
	"os"

	"github.com/marmutapp/openmarmut/internal/config"
)

// New creates a configured *slog.Logger from LogConfig.
func New(cfg config.LogConfig) *slog.Logger {
	return NewWithWriter(cfg, os.Stderr)
}

// NewWithWriter creates a configured *slog.Logger writing to w.
func NewWithWriter(cfg config.LogConfig, w io.Writer) *slog.Logger {
	level := parseLevel(cfg.Level)

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	switch cfg.Format {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		handler = slog.NewTextHandler(w, opts)
	}

	return slog.New(handler)
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
