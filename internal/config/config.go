package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the fully-merged application configuration.
type Config struct {
	Mode           string        `yaml:"mode"`
	TargetDir      string        `yaml:"target_dir"`
	Docker         DockerConfig  `yaml:"docker"`
	Log            LogConfig     `yaml:"log"`
	DefaultTimeout time.Duration `yaml:"default_timeout"`
}

// DockerConfig holds Docker-specific settings.
type DockerConfig struct {
	Image        string   `yaml:"image"`
	MountPath    string   `yaml:"mount_path"`
	Shell        string   `yaml:"shell"`
	ExtraVolumes []string `yaml:"extra_volumes"`
	EnvVars      []string `yaml:"env_vars"`
	Memory       string   `yaml:"memory"`
	CPUs         string   `yaml:"cpus"`
	NetworkMode  string   `yaml:"network_mode"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// FlagOverrides holds CLI flag values. Nil means "not set".
type FlagOverrides struct {
	Mode        *string
	TargetDir   *string
	ConfigPath  *string
	LogLevel    *string
	LogFormat   *string
	DockerImage *string
}

// defaults returns a Config with sensible default values.
func defaults() *Config {
	return &Config{
		Mode:           "local",
		DefaultTimeout: 30 * time.Second,
		Docker: DockerConfig{
			MountPath:   "/workspace",
			Shell:       "sh",
			NetworkMode: "none",
		},
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
	}
}

// Load builds a Config by merging sources: flags > env > file > defaults.
func Load(flags *FlagOverrides) (*Config, error) {
	cfg := defaults()

	// Load config file.
	if err := loadFile(cfg, flags); err != nil {
		return nil, fmt.Errorf("config.Load: %w", err)
	}

	// Apply environment variables.
	applyEnv(cfg)

	// Apply CLI flag overrides.
	if flags != nil {
		applyFlags(cfg, flags)
	}

	// Default target dir to cwd.
	if cfg.TargetDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("config.Load: get working directory: %w", err)
		}
		cfg.TargetDir = cwd
	}

	// Resolve relative target dir to absolute.
	if !filepath.IsAbs(cfg.TargetDir) {
		abs, err := filepath.Abs(cfg.TargetDir)
		if err != nil {
			return nil, fmt.Errorf("config.Load: resolve target dir: %w", err)
		}
		cfg.TargetDir = abs
	}

	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("config.Load: %w", err)
	}

	return cfg, nil
}

// loadFile reads and applies a YAML config file onto cfg.
func loadFile(cfg *Config, flags *FlagOverrides) error {
	var path string

	if flags != nil && flags.ConfigPath != nil {
		path = *flags.ConfigPath
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("config file not found: %s", path)
		}
	} else {
		// Try .opencode.yaml in cwd, then ~/.config/opencode/config.yaml.
		candidates := []string{".opencode.yaml"}
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, ".config", "opencode", "config.yaml"))
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				path = c
				break
			}
		}
	}

	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}

	return nil
}

// applyEnv applies OPENCODE_ environment variables onto cfg.
func applyEnv(cfg *Config) {
	if v := os.Getenv("OPENCODE_MODE"); v != "" {
		cfg.Mode = v
	}
	if v := os.Getenv("OPENCODE_TARGET_DIR"); v != "" {
		cfg.TargetDir = v
	}
	if v := os.Getenv("OPENCODE_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("OPENCODE_LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}
	if v := os.Getenv("OPENCODE_DOCKER_IMAGE"); v != "" {
		cfg.Docker.Image = v
	}
	if v := os.Getenv("OPENCODE_DOCKER_MOUNT_PATH"); v != "" {
		cfg.Docker.MountPath = v
	}
	if v := os.Getenv("OPENCODE_DOCKER_NETWORK_MODE"); v != "" {
		cfg.Docker.NetworkMode = v
	}
	if v := os.Getenv("OPENCODE_DEFAULT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.DefaultTimeout = d
		}
	}
}

// applyFlags applies non-nil CLI flag overrides onto cfg.
func applyFlags(cfg *Config, flags *FlagOverrides) {
	if flags.Mode != nil {
		cfg.Mode = *flags.Mode
	}
	if flags.TargetDir != nil {
		cfg.TargetDir = *flags.TargetDir
	}
	if flags.LogLevel != nil {
		cfg.Log.Level = *flags.LogLevel
	}
	if flags.LogFormat != nil {
		cfg.Log.Format = *flags.LogFormat
	}
	if flags.DockerImage != nil {
		cfg.Docker.Image = *flags.DockerImage
	}
}

// Validate checks the config for all violations and returns a combined error.
func Validate(cfg *Config) error {
	var errs []string

	if cfg.Mode != "local" && cfg.Mode != "docker" {
		errs = append(errs, fmt.Sprintf("mode must be \"local\" or \"docker\", got %q", cfg.Mode))
	}

	if cfg.TargetDir == "" {
		errs = append(errs, "target_dir must not be empty")
	}

	if cfg.Mode == "docker" {
		if cfg.Docker.Image == "" {
			errs = append(errs, "docker.image is required when mode is \"docker\"")
		}
		if !filepath.IsAbs(cfg.Docker.MountPath) {
			errs = append(errs, fmt.Sprintf("docker.mount_path must be absolute, got %q", cfg.Docker.MountPath))
		}
	}

	if cfg.DefaultTimeout <= 0 {
		errs = append(errs, "default_timeout must be positive")
	}

	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLevels[cfg.Log.Level] {
		errs = append(errs, fmt.Sprintf("log.level must be debug/info/warn/error, got %q", cfg.Log.Level))
	}

	validFormats := map[string]bool{"text": true, "json": true}
	if !validFormats[cfg.Log.Format] {
		errs = append(errs, fmt.Sprintf("log.format must be text/json, got %q", cfg.Log.Format))
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}

	return nil
}
