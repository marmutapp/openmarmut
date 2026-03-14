package localrt

import (
	"log/slog"

	"github.com/gajaai/openmarmut-go/internal/config"
	"github.com/gajaai/openmarmut-go/internal/runtime"
)

func init() {
	runtime.Register("local", func(cfg *config.Config, logger *slog.Logger) (runtime.Runtime, error) {
		return New(cfg.TargetDir, cfg.DefaultTimeout, logger), nil
	})
}
