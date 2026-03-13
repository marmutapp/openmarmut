package llm

import (
	"fmt"
	"log/slog"
)

// TypeConstructor creates a Provider from a ProviderEntry.
// Each wire format type ("openai", "anthropic", etc.) registers one of these.
type TypeConstructor func(entry ProviderEntry, logger *slog.Logger) (Provider, error)

var constructors = map[string]TypeConstructor{}

// RegisterType adds a wire format constructor for a given type name.
func RegisterType(typeName string, ctor TypeConstructor) {
	constructors[typeName] = ctor
}

// NewProvider creates a Provider from a ProviderEntry.
// It applies default auth, default endpoint URL, and resolves credentials
// before dispatching to the registered wire format constructor.
func NewProvider(entry ProviderEntry, logger *slog.Logger) (Provider, error) {
	// Check type is registered first.
	ctor, ok := constructors[entry.Type]
	if !ok {
		return nil, fmt.Errorf("llm.NewProvider: unknown provider type %q", entry.Type)
	}

	// Apply default auth if not set.
	if entry.Auth.Type == "" {
		entry.Auth = DefaultAuthForType(entry.Type)
	}

	// Apply default endpoint URL if not set.
	if entry.EndpointURL == "" {
		url := DefaultEndpointURL(entry.Type)
		if url == "" {
			return nil, fmt.Errorf("llm.NewProvider(%s): endpoint_url is required for type %q", entry.Name, entry.Type)
		}
		entry.EndpointURL = url
	}

	// Resolve credential from env var reference.
	if entry.APIKey != "" {
		resolved, err := ResolveCredential(entry.APIKey)
		if err != nil {
			return nil, fmt.Errorf("llm.NewProvider(%s): %w", entry.Name, err)
		}
		entry.APIKey = resolved
	}

	return ctor(entry, logger)
}
