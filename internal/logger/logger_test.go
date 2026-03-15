package logger

import (
	"bytes"
	"strings"
	"testing"

	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithWriter(config.LogConfig{Level: "info", Format: "text"}, &buf)
	require.NotNil(t, log)

	log.Info("hello", "key", "value")
	output := buf.String()
	assert.Contains(t, output, "hello")
	assert.Contains(t, output, "key=value")
}

func TestNew_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithWriter(config.LogConfig{Level: "info", Format: "json"}, &buf)
	require.NotNil(t, log)

	log.Info("hello")
	output := buf.String()
	assert.Contains(t, output, `"msg":"hello"`)
}

func TestNew_LevelFiltering(t *testing.T) {
	tests := []struct {
		name     string
		cfgLevel string
		logLevel string
		logFn    func(l *testing.T, buf *bytes.Buffer) string
		visible  bool
	}{
		{"debug visible at debug", "debug", "debug", logDebug, true},
		{"debug hidden at info", "info", "debug", logDebug, false},
		{"info visible at info", "info", "info", logInfo, true},
		{"warn visible at warn", "warn", "warn", logWarn, true},
		{"info hidden at warn", "warn", "info", logInfo, false},
		{"error visible at error", "error", "error", logError, true},
		{"warn hidden at error", "error", "warn", logWarn, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := NewWithWriter(config.LogConfig{Level: tt.cfgLevel, Format: "text"}, &buf)

			switch tt.logLevel {
			case "debug":
				log.Debug("test-msg")
			case "info":
				log.Info("test-msg")
			case "warn":
				log.Warn("test-msg")
			case "error":
				log.Error("test-msg")
			}

			if tt.visible {
				assert.True(t, strings.Contains(buf.String(), "test-msg"),
					"expected message to be visible")
			} else {
				assert.False(t, strings.Contains(buf.String(), "test-msg"),
					"expected message to be filtered")
			}
		})
	}
}

// Helpers to satisfy the test table interface — not actually used in the refactored version,
// but kept for potential future use of function pointer approach.
func logDebug(t *testing.T, buf *bytes.Buffer) string { return buf.String() }
func logInfo(t *testing.T, buf *bytes.Buffer) string  { return buf.String() }
func logWarn(t *testing.T, buf *bytes.Buffer) string   { return buf.String() }
func logError(t *testing.T, buf *bytes.Buffer) string  { return buf.String() }

func TestNew_DefaultLevel(t *testing.T) {
	var buf bytes.Buffer
	log := NewWithWriter(config.LogConfig{Level: "unknown", Format: "text"}, &buf)

	log.Info("should-show")
	assert.Contains(t, buf.String(), "should-show")

	buf.Reset()
	log.Debug("should-hide")
	assert.NotContains(t, buf.String(), "should-hide")
}
