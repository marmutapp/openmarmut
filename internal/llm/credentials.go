package llm

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// ResolveCredential resolves an env var reference to its value.
// "$VAR" and "env:VAR" both resolve to os.Getenv("VAR").
// Empty string returns empty string (valid for auth type "none").
// A value without a prefix is treated as a literal (returned as-is).
// Returns error if the referenced env var is not set.
func ResolveCredential(ref string) (string, error) {
	if ref == "" {
		return "", nil
	}

	var envVar string
	switch {
	case strings.HasPrefix(ref, "$"):
		envVar = ref[1:]
	case strings.HasPrefix(ref, "env:"):
		envVar = ref[4:]
	default:
		return ref, nil
	}

	val := os.Getenv(envVar)
	if val == "" {
		return "", fmt.Errorf("ResolveCredential: %w: environment variable %s is not set", ErrAuthFailed, envVar)
	}
	return val, nil
}

// ApplyAuth applies authentication to an HTTP request based on the AuthConfig.
// If resolvedKey is empty, no auth is applied.
func ApplyAuth(req *http.Request, auth AuthConfig, resolvedKey string) {
	if resolvedKey == "" {
		return
	}
	switch auth.Type {
	case "bearer":
		prefix := auth.TokenPrefix
		if prefix == "" {
			prefix = "Bearer "
		}
		req.Header.Set("Authorization", prefix+resolvedKey)
	case "header":
		req.Header.Set(auth.HeaderName, auth.TokenPrefix+resolvedKey)
	case "query":
		q := req.URL.Query()
		q.Set(auth.QueryParam, resolvedKey)
		req.URL.RawQuery = q.Encode()
	case "none", "":
		// No auth.
	}
}

// DefaultAuthForType returns the default AuthConfig for a provider type.
func DefaultAuthForType(typeName string) AuthConfig {
	switch typeName {
	case "openai", "openai-responses":
		return AuthConfig{Type: "bearer", TokenPrefix: "Bearer "}
	case "anthropic":
		return AuthConfig{Type: "header", HeaderName: "x-api-key"}
	case "gemini":
		return AuthConfig{Type: "query", QueryParam: "key"}
	case "ollama":
		return AuthConfig{Type: "none"}
	default:
		return AuthConfig{Type: "none"}
	}
}

// DefaultEndpointURL returns the default endpoint URL for a provider type.
// Returns empty string for types that have no default (e.g., "custom").
func DefaultEndpointURL(typeName string) string {
	switch typeName {
	case "openai", "openai-responses":
		return "https://api.openai.com"
	case "anthropic":
		return "https://api.anthropic.com"
	case "gemini":
		return "https://generativelanguage.googleapis.com"
	case "ollama":
		return "http://localhost:11434"
	default:
		return ""
	}
}
