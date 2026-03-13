package llm

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// --- Factory Tests ---

func TestNewProvider_UnknownType(t *testing.T) {
	_, err := NewProvider(ProviderEntry{Name: "test", Type: "unknown"}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider type")
}

func TestNewProvider_AppliesDefaultEndpoint(t *testing.T) {
	var captured ProviderEntry
	RegisterType("openai", func(entry ProviderEntry, logger *slog.Logger) (Provider, error) {
		captured = entry
		return nil, nil
	})
	defer delete(constructors, "openai")

	_, _ = NewProvider(ProviderEntry{
		Name:      "test",
		Type:      "openai",
		ModelName: "gpt-4o",
	}, testLogger)
	assert.Equal(t, "https://api.openai.com", captured.EndpointURL)
}

func TestNewProvider_AppliesDefaultAuth(t *testing.T) {
	var captured ProviderEntry
	RegisterType("_test_auth", func(entry ProviderEntry, logger *slog.Logger) (Provider, error) {
		captured = entry
		return nil, nil
	})
	defer delete(constructors, "_test_auth")

	// Provide endpoint so factory doesn't fail on missing default.
	_, _ = NewProvider(ProviderEntry{
		Name:        "test",
		Type:        "_test_auth",
		EndpointURL: "http://localhost:9999",
		ModelName:   "m",
	}, testLogger)
	assert.Equal(t, "none", captured.Auth.Type)
}

func TestNewProvider_ResolvesCredential(t *testing.T) {
	t.Setenv("TEST_FACTORY_KEY", "resolved-secret")

	var captured ProviderEntry
	RegisterType("_test_cred", func(entry ProviderEntry, logger *slog.Logger) (Provider, error) {
		captured = entry
		return nil, nil
	})
	defer delete(constructors, "_test_cred")

	_, _ = NewProvider(ProviderEntry{
		Name:        "test",
		Type:        "_test_cred",
		EndpointURL: "http://localhost:9999",
		APIKey:      "$TEST_FACTORY_KEY",
	}, testLogger)
	assert.Equal(t, "resolved-secret", captured.APIKey)
}

func TestNewProvider_CredentialResolutionError(t *testing.T) {
	os.Unsetenv("NONEXISTENT_FACTORY_KEY")

	RegisterType("_test_cred_err", func(entry ProviderEntry, logger *slog.Logger) (Provider, error) {
		return nil, nil
	})
	defer delete(constructors, "_test_cred_err")

	_, err := NewProvider(ProviderEntry{
		Name:        "test",
		Type:        "_test_cred_err",
		EndpointURL: "http://localhost:9999",
		APIKey:      "$NONEXISTENT_FACTORY_KEY",
	}, testLogger)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthFailed)
}

func TestNewProvider_MissingEndpointForCustomType(t *testing.T) {
	RegisterType("_test_no_ep", func(entry ProviderEntry, logger *slog.Logger) (Provider, error) {
		return nil, nil
	})
	defer delete(constructors, "_test_no_ep")

	_, err := NewProvider(ProviderEntry{
		Name: "test",
		Type: "_test_no_ep",
	}, testLogger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint_url is required")
}

// --- ResolveCredential Tests ---

func TestResolveCredential_Empty(t *testing.T) {
	val, err := ResolveCredential("")
	require.NoError(t, err)
	assert.Empty(t, val)
}

func TestResolveCredential_DollarSyntax(t *testing.T) {
	t.Setenv("MY_API_KEY", "secret-123")
	val, err := ResolveCredential("$MY_API_KEY")
	require.NoError(t, err)
	assert.Equal(t, "secret-123", val)
}

func TestResolveCredential_EnvPrefixSyntax(t *testing.T) {
	t.Setenv("MY_API_KEY", "secret-456")
	val, err := ResolveCredential("env:MY_API_KEY")
	require.NoError(t, err)
	assert.Equal(t, "secret-456", val)
}

func TestResolveCredential_Literal(t *testing.T) {
	val, err := ResolveCredential("literal-key-value")
	require.NoError(t, err)
	assert.Equal(t, "literal-key-value", val)
}

func TestResolveCredential_MissingEnvVar(t *testing.T) {
	os.Unsetenv("NONEXISTENT_KEY")
	_, err := ResolveCredential("$NONEXISTENT_KEY")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthFailed)
	assert.Contains(t, err.Error(), "NONEXISTENT_KEY")
}

func TestResolveCredential_EnvPrefixMissing(t *testing.T) {
	os.Unsetenv("NONEXISTENT_KEY")
	_, err := ResolveCredential("env:NONEXISTENT_KEY")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthFailed)
}

// --- ApplyAuth Tests ---

func TestApplyAuth_Bearer(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.example.com/v1/test", nil)
	ApplyAuth(req, AuthConfig{Type: "bearer", TokenPrefix: "Bearer "}, "my-key")
	assert.Equal(t, "Bearer my-key", req.Header.Get("Authorization"))
}

func TestApplyAuth_BearerDefaultPrefix(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.example.com/v1/test", nil)
	ApplyAuth(req, AuthConfig{Type: "bearer"}, "my-key")
	assert.Equal(t, "Bearer my-key", req.Header.Get("Authorization"))
}

func TestApplyAuth_Header(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.example.com/v1/test", nil)
	ApplyAuth(req, AuthConfig{Type: "header", HeaderName: "x-api-key"}, "my-key")
	assert.Equal(t, "my-key", req.Header.Get("x-api-key"))
}

func TestApplyAuth_HeaderWithPrefix(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.example.com/v1/test", nil)
	ApplyAuth(req, AuthConfig{Type: "header", HeaderName: "x-api-key", TokenPrefix: "Key "}, "my-key")
	assert.Equal(t, "Key my-key", req.Header.Get("x-api-key"))
}

func TestApplyAuth_Query(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.example.com/v1/test", nil)
	ApplyAuth(req, AuthConfig{Type: "query", QueryParam: "key"}, "my-key")
	assert.Equal(t, "my-key", req.URL.Query().Get("key"))
}

func TestApplyAuth_None(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.example.com/v1/test", nil)
	ApplyAuth(req, AuthConfig{Type: "none"}, "")
	assert.Empty(t, req.Header.Get("Authorization"))
}

func TestApplyAuth_EmptyKey(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.example.com/v1/test", nil)
	ApplyAuth(req, AuthConfig{Type: "bearer"}, "")
	assert.Empty(t, req.Header.Get("Authorization"))
}

// --- Default Tests ---

func TestDefaultAuthForType(t *testing.T) {
	tests := []struct {
		typeName string
		expected AuthConfig
	}{
		{"openai", AuthConfig{Type: "bearer", TokenPrefix: "Bearer "}},
		{"anthropic", AuthConfig{Type: "header", HeaderName: "x-api-key"}},
		{"gemini", AuthConfig{Type: "query", QueryParam: "key"}},
		{"ollama", AuthConfig{Type: "none"}},
		{"unknown", AuthConfig{Type: "none"}},
	}
	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			assert.Equal(t, tt.expected, DefaultAuthForType(tt.typeName))
		})
	}
}

func TestDefaultEndpointURL(t *testing.T) {
	assert.Equal(t, "https://api.openai.com", DefaultEndpointURL("openai"))
	assert.Equal(t, "https://api.anthropic.com", DefaultEndpointURL("anthropic"))
	assert.Equal(t, "https://generativelanguage.googleapis.com", DefaultEndpointURL("gemini"))
	assert.Equal(t, "http://localhost:11434", DefaultEndpointURL("ollama"))
	assert.Empty(t, DefaultEndpointURL("custom"))
}
