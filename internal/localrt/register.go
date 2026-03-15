package localrt

import (
	"log/slog"

	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/marmutapp/openmarmut/internal/runtime"
)

func init() {
	runtime.Register("local", func(cfg *config.Config, logger *slog.Logger) (runtime.Runtime, error) {
		return New(cfg.TargetDir, cfg.DefaultTimeout, logger), nil
	})
}
