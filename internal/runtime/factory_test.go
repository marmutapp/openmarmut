package runtime_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/marmutapp/openmarmut/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestNewRuntime_UnknownMode(t *testing.T) {
	cfg := &config.Config{
		Mode:      "unknown",
		TargetDir: "/tmp",
	}

	_, err := runtime.NewRuntime(cfg, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown mode")
}
