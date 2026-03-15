package runtime

import (
	"fmt"
	"log/slog"

	"github.com/marmutapp/openmarmut/internal/config"
)

// RuntimeConstructor is a function that creates a Runtime from config.
// Registered by each runtime package to avoid import cycles.
type RuntimeConstructor func(cfg *config.Config, logger *slog.Logger) (Runtime, error)

var constructors = map[string]RuntimeConstructor{}

// Register adds a runtime constructor for a given mode name.
func Register(mode string, ctor RuntimeConstructor) {
	constructors[mode] = ctor
}

// NewRuntime creates the appropriate Runtime based on config.Mode.
func NewRuntime(cfg *config.Config, logger *slog.Logger) (Runtime, error) {
	ctor, ok := constructors[cfg.Mode]
	if !ok {
		return nil, fmt.Errorf("runtime.NewRuntime: unknown mode %q", cfg.Mode)
	}
	return ctor(cfg, logger)
}
