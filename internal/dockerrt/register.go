package dockerrt

import (
	"log/slog"

	"github.com/gajaai/opencode-go/internal/config"
	"github.com/gajaai/opencode-go/internal/runtime"
)

func init() {
	runtime.Register("docker", func(cfg *config.Config, logger *slog.Logger) (runtime.Runtime, error) {
		return New(cfg.TargetDir, cfg.Docker, cfg.DefaultTimeout, logger), nil
	})
}
