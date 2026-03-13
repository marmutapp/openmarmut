package cli

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gajaai/opencode-go/internal/config"
	"github.com/gajaai/opencode-go/internal/localrt"
	"github.com/gajaai/opencode-go/internal/logger"
	"github.com/gajaai/opencode-go/internal/runtime"
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

	rt, err := newRuntime(cfg, log)
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

// newRuntime creates a Runtime from config. Only local mode is supported for now.
func newRuntime(cfg *config.Config, log *slog.Logger) (runtime.Runtime, error) {
	switch cfg.Mode {
	case "local":
		return localrt.New(cfg.TargetDir, cfg.DefaultTimeout, log), nil
	default:
		return nil, fmt.Errorf("unsupported runtime mode: %q", cfg.Mode)
	}
}
