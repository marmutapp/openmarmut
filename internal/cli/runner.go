package cli

import (
	"context"
	"fmt"

	"github.com/gajaai/opencode-go/internal/config"
	"github.com/gajaai/opencode-go/internal/logger"
	"github.com/gajaai/opencode-go/internal/runtime"

	// Register runtime implementations.
	_ "github.com/gajaai/opencode-go/internal/dockerrt"
	_ "github.com/gajaai/opencode-go/internal/localrt"
)

// Runner manages the config → logger → runtime → init → fn → close lifecycle.
type Runner struct {
	flags *config.FlagOverrides
}

// NewRunner creates a Runner that will use the given flag overrides.
func NewRunner(flags *config.FlagOverrides) *Runner {
	return &Runner{flags: flags}
}

// Run executes the full lifecycle and calls fn with the initialized runtime.
func (r *Runner) Run(ctx context.Context, fn func(ctx context.Context, rt runtime.Runtime) error) error {
	cfg, err := config.Load(r.flags)
	if err != nil {
		return fmt.Errorf("cli.Runner.Run: %w", err)
	}

	log := logger.New(cfg.Log)

	rt, err := runtime.NewRuntime(cfg, log)
	if err != nil {
		return fmt.Errorf("cli.Runner.Run: %w", err)
	}

	if err := rt.Init(ctx); err != nil {
		return fmt.Errorf("cli.Runner.Run: init runtime: %w", err)
	}
	defer func() {
		if closeErr := rt.Close(ctx); closeErr != nil {
			log.Warn("runtime close failed", "error", closeErr)
		}
	}()

	return fn(ctx, rt)
}
