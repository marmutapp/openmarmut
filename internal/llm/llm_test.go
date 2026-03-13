package llm

import (
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestNewProvider_Unknown(t *testing.T) {
	_, err := NewProvider(ProviderConfig{Name: "unknown"}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

func TestResolveAPIKey_Ollama(t *testing.T) {
	key, err := ResolveAPIKey("ollama", "")
	require.NoError(t, err)
	assert.Empty(t, key)
}

func TestResolveAPIKey_ExplicitEnvVar(t *testing.T) {
	t.Setenv("MY_CUSTOM_KEY", "custom-key-value")

	key, err := ResolveAPIKey("openai", "MY_CUSTOM_KEY")
	require.NoError(t, err)
	assert.Equal(t, "custom-key-value", key)
}

func TestResolveAPIKey_StandardEnvVar(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ant-key-value")

	key, err := ResolveAPIKey("anthropic", "")
	require.NoError(t, err)
	assert.Equal(t, "ant-key-value", key)
}

func TestResolveAPIKey_Missing(t *testing.T) {
	// Ensure the env var is not set.
	os.Unsetenv("OPENAI_API_KEY")

	_, err := ResolveAPIKey("openai", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthFailed)
	assert.Contains(t, err.Error(), "OPENAI_API_KEY")
}

func TestResolveAPIKey_GeminiStandard(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "google-key")

	key, err := ResolveAPIKey("gemini", "")
	require.NoError(t, err)
	assert.Equal(t, "google-key", key)
}
