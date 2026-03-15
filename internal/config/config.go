package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gajaai/openmarmut-go/internal/llm"
	"gopkg.in/yaml.v3"
)

// Config holds the fully-merged application configuration.
type Config struct {
	Mode           string        `yaml:"mode"`
	TargetDir      string        `yaml:"target_dir"`
	Docker         DockerConfig  `yaml:"docker"`
	Log            LogConfig     `yaml:"log"`
	DefaultTimeout time.Duration `yaml:"default_timeout"`
	LLM            LLMConfig     `yaml:"llm"`
	Agent          AgentConfig   `yaml:"agent"`
}

// AgentConfig holds agent-level settings for tool permissions and context management.
type AgentConfig struct {
	AutoAllow           []string `yaml:"auto_allow"`           // Tool names that execute without confirmation.
	Confirm             []string `yaml:"confirm"`              // Tool names that require user confirmation.
	ContextWindow       int      `yaml:"context_window"`       // Override provider's context window (0 = use provider default).
	TruncationThreshold float64  `yaml:"truncation_threshold"` // Fraction at which truncation triggers (0.0–1.0, default 0.80).
	KeepRecentTurns     int      `yaml:"keep_recent_turns"`    // Minimum recent turn pairs to preserve (default 4).
	SessionRetentionDays int     `yaml:"session_retention_days"` // Days to keep sessions before auto-cleanup (default 30).
	PlanProvider         string  `yaml:"plan_provider"`          // Provider name for plan mode analysis (empty = use active provider).
	AutoMemory           bool    `yaml:"auto_memory"`            // Enable auto-memory extraction on session end (default true).
	MemoryFile           string  `yaml:"memory_file"`            // Custom path for MEMORY.md (default ~/.openmarmut/memory/MEMORY.md).
}

// LLMConfig holds all LLM provider configuration.
type LLMConfig struct {
	Providers          []llm.ProviderEntry `yaml:"providers"`
	ActiveProvider     string              `yaml:"active_provider"`
	DefaultTemperature *float64            `yaml:"default_temperature"`
	DefaultMaxTokens   *int                `yaml:"default_max_tokens"`
	DefaultTimeout     time.Duration       `yaml:"default_timeout"`
	// ModelOverride is set from OPENMARMUT_LLM_MODEL env or --model flag.
	// Applied to the active provider at resolution time.
	ModelOverride string `yaml:"-"`
	// APIKeyOverride is set from OPENMARMUT_LLM_API_KEY env.
	// Applied to the active provider at resolution time.
	APIKeyOverride string `yaml:"-"`
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
	Mode           *string
	TargetDir      *string
	ConfigPath     *string
	LogLevel       *string
	LogFormat      *string
	DockerImage    *string
	LLMProvider    *string  // --provider flag → overrides active_provider
	LLMModel       *string  // --model flag → overrides active provider's model
	LLMTemperature *float64 // --temperature flag
	AutoApprove    bool     // --auto-approve flag → skip all tool confirmations
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
		Agent: AgentConfig{
			AutoMemory: true,
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
		// Determine the target directory early for config file discovery.
		// Check flags first, then env, then fall back to cwd.
		targetDir := ""
		if flags != nil && flags.TargetDir != nil {
			targetDir = *flags.TargetDir
		}
		if targetDir == "" {
			targetDir = os.Getenv("OPENMARMUT_TARGET_DIR")
		}
		if targetDir == "" {
			targetDir, _ = os.Getwd()
		}

		// Try .openmarmut.yaml in target dir, then cwd, then user config dir.
		var candidates []string
		if targetDir != "" {
			candidates = append(candidates, filepath.Join(targetDir, ".openmarmut.yaml"))
		}
		cwd, _ := os.Getwd()
		if cwd != "" && cwd != targetDir {
			candidates = append(candidates, filepath.Join(cwd, ".openmarmut.yaml"))
		}
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, ".config", "openmarmut", "config.yaml"))
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

// applyEnv applies OPENMARMUT_ environment variables onto cfg.
func applyEnv(cfg *Config) {
	if v := os.Getenv("OPENMARMUT_MODE"); v != "" {
		cfg.Mode = v
	}
	if v := os.Getenv("OPENMARMUT_TARGET_DIR"); v != "" {
		cfg.TargetDir = v
	}
	if v := os.Getenv("OPENMARMUT_LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("OPENMARMUT_LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}
	if v := os.Getenv("OPENMARMUT_DOCKER_IMAGE"); v != "" {
		cfg.Docker.Image = v
	}
	if v := os.Getenv("OPENMARMUT_DOCKER_MOUNT_PATH"); v != "" {
		cfg.Docker.MountPath = v
	}
	if v := os.Getenv("OPENMARMUT_DOCKER_NETWORK_MODE"); v != "" {
		cfg.Docker.NetworkMode = v
	}
	if v := os.Getenv("OPENMARMUT_DEFAULT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.DefaultTimeout = d
		}
	}

	// LLM env overrides.
	if v := os.Getenv("OPENMARMUT_LLM_PROVIDER"); v != "" {
		cfg.LLM.ActiveProvider = v
	}
	if v := os.Getenv("OPENMARMUT_LLM_MODEL"); v != "" {
		cfg.LLM.ModelOverride = v
	}
	if v := os.Getenv("OPENMARMUT_LLM_API_KEY"); v != "" {
		cfg.LLM.APIKeyOverride = v
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
	if flags.LLMProvider != nil {
		cfg.LLM.ActiveProvider = *flags.LLMProvider
	}
	if flags.LLMModel != nil {
		cfg.LLM.ModelOverride = *flags.LLMModel
	}
	if flags.LLMTemperature != nil {
		cfg.LLM.DefaultTemperature = flags.LLMTemperature
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

	// Validate LLM config if any providers are configured.
	if len(cfg.LLM.Providers) > 0 {
		validateLLM(&cfg.LLM, &errs)
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}

	return nil
}

// validProviderTypes lists the recognized wire format type names.
var validProviderTypes = map[string]bool{
	"openai":            true,
	"openai-responses":  true,
	"anthropic":         true,
	"gemini":            true,
	"ollama":            true,
	"custom":            true,
}

// validateLLM checks the LLM config section and appends any errors to errs.
func validateLLM(llmCfg *LLMConfig, errs *[]string) {
	seen := make(map[string]bool, len(llmCfg.Providers))
	for i, p := range llmCfg.Providers {
		prefix := fmt.Sprintf("llm.providers[%d]", i)
		if p.Name == "" {
			*errs = append(*errs, prefix+".name must not be empty")
		} else if seen[p.Name] {
			*errs = append(*errs, fmt.Sprintf("%s.name %q is a duplicate", prefix, p.Name))
		} else {
			seen[p.Name] = true
		}
		if !validProviderTypes[p.Type] {
			*errs = append(*errs, fmt.Sprintf("%s.type must be openai/openai-responses/anthropic/gemini/ollama/custom, got %q", prefix, p.Type))
		}
		if p.ModelName == "" {
			*errs = append(*errs, prefix+".model must not be empty")
		}
		if p.Temperature != nil && (*p.Temperature < 0 || *p.Temperature > 2) {
			*errs = append(*errs, fmt.Sprintf("%s.temperature must be in [0.0, 2.0], got %g", prefix, *p.Temperature))
		}
		if p.MaxTokens != nil && *p.MaxTokens <= 0 {
			*errs = append(*errs, fmt.Sprintf("%s.max_tokens must be positive, got %d", prefix, *p.MaxTokens))
		}
	}

	// active_provider must match a named provider if set.
	if llmCfg.ActiveProvider != "" && !seen[llmCfg.ActiveProvider] {
		*errs = append(*errs, fmt.Sprintf("llm.active_provider %q does not match any configured provider name", llmCfg.ActiveProvider))
	}

	// LLM-level defaults.
	if llmCfg.DefaultTemperature != nil && (*llmCfg.DefaultTemperature < 0 || *llmCfg.DefaultTemperature > 2) {
		*errs = append(*errs, fmt.Sprintf("llm.default_temperature must be in [0.0, 2.0], got %g", *llmCfg.DefaultTemperature))
	}
	if llmCfg.DefaultMaxTokens != nil && *llmCfg.DefaultMaxTokens <= 0 {
		*errs = append(*errs, fmt.Sprintf("llm.default_max_tokens must be positive, got %d", *llmCfg.DefaultMaxTokens))
	}
	if llmCfg.DefaultTimeout < 0 {
		*errs = append(*errs, "llm.default_timeout must not be negative")
	}
}

// ResolveActiveProvider returns the active ProviderEntry from the LLM config,
// applying flag/env overrides for active selection, model, and API key.
// Returns an error if no active provider can be determined.
func (c *LLMConfig) ResolveActiveProvider() (*llm.ProviderEntry, error) {
	if len(c.Providers) == 0 {
		return nil, fmt.Errorf("config.ResolveActiveProvider: no providers configured")
	}

	name := c.ActiveProvider
	if name == "" && len(c.Providers) == 1 {
		name = c.Providers[0].Name
	}
	if name == "" {
		return nil, fmt.Errorf("config.ResolveActiveProvider: no active provider configured")
	}

	for _, p := range c.Providers {
		if p.Name == name {
			entry := p // copy
			if c.ModelOverride != "" {
				entry.ModelName = c.ModelOverride
			}
			if c.APIKeyOverride != "" {
				entry.APIKey = c.APIKeyOverride
			}
			return &entry, nil
		}
	}

	return nil, fmt.Errorf("config.ResolveActiveProvider: provider %q not found", name)
}

// ActiveProviderName returns the resolved active provider name, considering
// single-entry fallback. Returns empty string if unresolvable.
func (c *LLMConfig) ActiveProviderName() string {
	if c.ActiveProvider != "" {
		return c.ActiveProvider
	}
	if len(c.Providers) == 1 {
		return c.Providers[0].Name
	}
	return ""
}

// FindProvider returns the ProviderEntry with the given name, or nil if not found.
func (c *LLMConfig) FindProvider(name string) *llm.ProviderEntry {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i]
		}
	}
	return nil
}
