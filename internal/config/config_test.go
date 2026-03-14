package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gajaai/opencode-go/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string   { return &s }
func float64Ptr(f float64) *float64 { return &f }
func intPtr(i int) *int         { return &i }

func TestDefaults(t *testing.T) {
	cfg := defaults()

	assert.Equal(t, "local", cfg.Mode)
	assert.Equal(t, 30*time.Second, cfg.DefaultTimeout)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
	assert.Equal(t, "/workspace", cfg.Docker.MountPath)
	assert.Equal(t, "sh", cfg.Docker.Shell)
	assert.Equal(t, "none", cfg.Docker.NetworkMode)
}

func TestLoad_DefaultsWithNoOverrides(t *testing.T) {
	// Ensure no env vars interfere.
	for _, key := range []string{"OPENCODE_MODE", "OPENCODE_TARGET_DIR", "OPENCODE_LOG_LEVEL",
		"OPENCODE_LOG_FORMAT", "OPENCODE_DOCKER_IMAGE", "OPENCODE_DEFAULT_TIMEOUT"} {
		t.Setenv(key, "")
	}

	cfg, err := Load(nil)
	require.NoError(t, err)

	assert.Equal(t, "local", cfg.Mode)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
	assert.NotEmpty(t, cfg.TargetDir) // defaults to cwd
}

func TestLoad_FlagsOverrideDefaults(t *testing.T) {
	for _, key := range []string{"OPENCODE_MODE", "OPENCODE_LOG_LEVEL", "OPENCODE_LOG_FORMAT"} {
		t.Setenv(key, "")
	}

	targetDir := t.TempDir()
	flags := &FlagOverrides{
		Mode:      strPtr("local"),
		TargetDir: &targetDir,
		LogLevel:  strPtr("debug"),
		LogFormat: strPtr("json"),
	}

	cfg, err := Load(flags)
	require.NoError(t, err)

	assert.Equal(t, "local", cfg.Mode)
	assert.Equal(t, targetDir, cfg.TargetDir)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
}

func TestLoad_EnvOverridesDefaults(t *testing.T) {
	targetDir := t.TempDir()
	t.Setenv("OPENCODE_MODE", "local")
	t.Setenv("OPENCODE_TARGET_DIR", targetDir)
	t.Setenv("OPENCODE_LOG_LEVEL", "warn")
	t.Setenv("OPENCODE_LOG_FORMAT", "json")
	t.Setenv("OPENCODE_DEFAULT_TIMEOUT", "60s")

	cfg, err := Load(nil)
	require.NoError(t, err)

	assert.Equal(t, "local", cfg.Mode)
	assert.Equal(t, targetDir, cfg.TargetDir)
	assert.Equal(t, "warn", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, 60*time.Second, cfg.DefaultTimeout)
}

func TestLoad_FlagsOverrideEnv(t *testing.T) {
	targetDir := t.TempDir()
	t.Setenv("OPENCODE_MODE", "local")
	t.Setenv("OPENCODE_LOG_LEVEL", "warn")
	t.Setenv("OPENCODE_TARGET_DIR", targetDir)

	flags := &FlagOverrides{
		LogLevel: strPtr("error"),
	}

	cfg, err := Load(flags)
	require.NoError(t, err)

	assert.Equal(t, "local", cfg.Mode)    // from env
	assert.Equal(t, "error", cfg.Log.Level) // flag wins
}

func TestLoad_ConfigFile(t *testing.T) {
	dir := t.TempDir()
	configContent := `
mode: local
target_dir: ` + dir + `
log:
  level: debug
  format: json
default_timeout: 45s
`
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0644))

	// Clear env vars.
	for _, key := range []string{"OPENCODE_MODE", "OPENCODE_TARGET_DIR", "OPENCODE_LOG_LEVEL",
		"OPENCODE_LOG_FORMAT", "OPENCODE_DEFAULT_TIMEOUT"} {
		t.Setenv(key, "")
	}

	flags := &FlagOverrides{
		ConfigPath: &configPath,
	}

	cfg, err := Load(flags)
	require.NoError(t, err)

	assert.Equal(t, "local", cfg.Mode)
	assert.Equal(t, dir, cfg.TargetDir)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, 45*time.Second, cfg.DefaultTimeout)
}

func TestLoad_ConfigFileNotFound(t *testing.T) {
	path := "/nonexistent/config.yaml"
	flags := &FlagOverrides{
		ConfigPath: &path,
	}

	_, err := Load(flags)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config file not found")
}

func TestLoad_RelativeTargetDirResolved(t *testing.T) {
	for _, key := range []string{"OPENCODE_MODE", "OPENCODE_TARGET_DIR", "OPENCODE_LOG_LEVEL",
		"OPENCODE_LOG_FORMAT"} {
		t.Setenv(key, "")
	}

	// Use "." which should resolve to cwd.
	dot := "."
	flags := &FlagOverrides{
		TargetDir: &dot,
	}

	cfg, err := Load(flags)
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(cfg.TargetDir))
}

func TestValidate_ValidLocal(t *testing.T) {
	cfg := &Config{
		Mode:           "local",
		TargetDir:      "/some/dir",
		DefaultTimeout: 30 * time.Second,
		Log:            LogConfig{Level: "info", Format: "text"},
	}
	assert.NoError(t, Validate(cfg))
}

func TestValidate_ValidDocker(t *testing.T) {
	cfg := &Config{
		Mode:           "docker",
		TargetDir:      "/some/dir",
		DefaultTimeout: 30 * time.Second,
		Log:            LogConfig{Level: "info", Format: "text"},
		Docker: DockerConfig{
			Image:     "ubuntu:24.04",
			MountPath: "/workspace",
		},
	}
	assert.NoError(t, Validate(cfg))
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := &Config{
		Mode:           "invalid",
		TargetDir:      "",
		DefaultTimeout: -1,
		Log:            LogConfig{Level: "verbose", Format: "xml"},
	}

	err := Validate(cfg)
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, "mode must be")
	assert.Contains(t, msg, "target_dir must not be empty")
	assert.Contains(t, msg, "default_timeout must be positive")
	assert.Contains(t, msg, "log.level must be")
	assert.Contains(t, msg, "log.format must be")
}

func TestValidate_DockerMissingImage(t *testing.T) {
	cfg := &Config{
		Mode:           "docker",
		TargetDir:      "/some/dir",
		DefaultTimeout: 30 * time.Second,
		Log:            LogConfig{Level: "info", Format: "text"},
		Docker:         DockerConfig{MountPath: "/workspace"},
	}

	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker.image is required")
}

func TestValidate_DockerRelativeMountPath(t *testing.T) {
	cfg := &Config{
		Mode:           "docker",
		TargetDir:      "/some/dir",
		DefaultTimeout: 30 * time.Second,
		Log:            LogConfig{Level: "info", Format: "text"},
		Docker:         DockerConfig{Image: "ubuntu", MountPath: "workspace"},
	}

	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "docker.mount_path must be absolute")
}

// --- LLM Config Tests ---

func validBaseConfig() *Config {
	return &Config{
		Mode:           "local",
		TargetDir:      "/some/dir",
		DefaultTimeout: 30 * time.Second,
		Log:            LogConfig{Level: "info", Format: "text"},
	}
}

func TestValidate_LLM_ValidSingleProvider(t *testing.T) {
	cfg := validBaseConfig()
	cfg.LLM = LLMConfig{
		ActiveProvider: "openai",
		Providers: []llm.ProviderEntry{
			{Name: "openai", Type: "openai", ModelName: "gpt-4o"},
		},
	}
	assert.NoError(t, Validate(cfg))
}

func TestValidate_LLM_ValidMultiProvider(t *testing.T) {
	cfg := validBaseConfig()
	cfg.LLM = LLMConfig{
		ActiveProvider: "claude",
		Providers: []llm.ProviderEntry{
			{Name: "claude", Type: "anthropic", ModelName: "claude-sonnet-4-20250514"},
			{Name: "gpt", Type: "openai", ModelName: "gpt-4o"},
			{Name: "local", Type: "ollama", ModelName: "llama3.1"},
		},
	}
	assert.NoError(t, Validate(cfg))
}

func TestValidate_LLM_NoProvidersSkipsValidation(t *testing.T) {
	cfg := validBaseConfig()
	// Empty LLM section — should pass validation.
	assert.NoError(t, Validate(cfg))
}

func TestValidate_LLM_ActiveProviderMismatch(t *testing.T) {
	cfg := validBaseConfig()
	cfg.LLM = LLMConfig{
		ActiveProvider: "nonexistent",
		Providers: []llm.ProviderEntry{
			{Name: "openai", Type: "openai", ModelName: "gpt-4o"},
		},
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `active_provider "nonexistent" does not match`)
}

func TestValidate_LLM_DuplicateProviderNames(t *testing.T) {
	cfg := validBaseConfig()
	cfg.LLM = LLMConfig{
		ActiveProvider: "dup",
		Providers: []llm.ProviderEntry{
			{Name: "dup", Type: "openai", ModelName: "gpt-4o"},
			{Name: "dup", Type: "anthropic", ModelName: "claude"},
		},
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"dup" is a duplicate`)
}

func TestValidate_LLM_EmptyProviderName(t *testing.T) {
	cfg := validBaseConfig()
	cfg.LLM = LLMConfig{
		Providers: []llm.ProviderEntry{
			{Name: "", Type: "openai", ModelName: "gpt-4o"},
		},
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name must not be empty")
}

func TestValidate_LLM_InvalidType(t *testing.T) {
	cfg := validBaseConfig()
	cfg.LLM = LLMConfig{
		ActiveProvider: "bad",
		Providers: []llm.ProviderEntry{
			{Name: "bad", Type: "unknown_format", ModelName: "m"},
		},
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "type must be openai/anthropic/gemini/ollama/custom")
}

func TestValidate_LLM_EmptyModel(t *testing.T) {
	cfg := validBaseConfig()
	cfg.LLM = LLMConfig{
		ActiveProvider: "p",
		Providers: []llm.ProviderEntry{
			{Name: "p", Type: "openai", ModelName: ""},
		},
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model must not be empty")
}

func TestValidate_LLM_TemperatureOutOfRange(t *testing.T) {
	cfg := validBaseConfig()
	badTemp := 3.0
	cfg.LLM = LLMConfig{
		ActiveProvider: "p",
		Providers: []llm.ProviderEntry{
			{Name: "p", Type: "openai", ModelName: "m", Temperature: &badTemp},
		},
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "temperature must be in [0.0, 2.0]")
}

func TestValidate_LLM_NegativeMaxTokens(t *testing.T) {
	cfg := validBaseConfig()
	bad := -1
	cfg.LLM = LLMConfig{
		ActiveProvider: "p",
		Providers: []llm.ProviderEntry{
			{Name: "p", Type: "openai", ModelName: "m", MaxTokens: &bad},
		},
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_tokens must be positive")
}

func TestValidate_LLM_DefaultTemperatureOutOfRange(t *testing.T) {
	cfg := validBaseConfig()
	cfg.LLM = LLMConfig{
		ActiveProvider:     "p",
		DefaultTemperature: float64Ptr(-0.5),
		Providers: []llm.ProviderEntry{
			{Name: "p", Type: "openai", ModelName: "m"},
		},
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "llm.default_temperature must be in [0.0, 2.0]")
}

func TestValidate_LLM_DefaultMaxTokensInvalid(t *testing.T) {
	cfg := validBaseConfig()
	cfg.LLM = LLMConfig{
		ActiveProvider:   "p",
		DefaultMaxTokens: intPtr(0),
		Providers: []llm.ProviderEntry{
			{Name: "p", Type: "openai", ModelName: "m"},
		},
	}
	err := Validate(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "llm.default_max_tokens must be positive")
}

func TestValidate_LLM_MultipleErrors(t *testing.T) {
	cfg := validBaseConfig()
	badTemp := 5.0
	cfg.LLM = LLMConfig{
		ActiveProvider: "missing",
		Providers: []llm.ProviderEntry{
			{Name: "", Type: "invalid", ModelName: "", Temperature: &badTemp},
		},
	}
	err := Validate(cfg)
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "name must not be empty")
	assert.Contains(t, msg, "type must be openai")
	assert.Contains(t, msg, "model must not be empty")
	assert.Contains(t, msg, "temperature must be in")
	assert.Contains(t, msg, "active_provider")
}

func TestLoad_LLM_ConfigFile(t *testing.T) {
	dir := t.TempDir()
	configContent := `
mode: local
target_dir: ` + dir + `
llm:
  active_provider: myopenai
  default_temperature: 0.5
  default_max_tokens: 4096
  default_timeout: 120s
  providers:
    - name: myopenai
      type: openai
      model: gpt-4o
      api_key: "$OPENAI_API_KEY"
      temperature: 0.2
      max_tokens: 8192
    - name: local
      type: ollama
      model: llama3.1
`
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o644))

	for _, key := range []string{"OPENCODE_MODE", "OPENCODE_TARGET_DIR", "OPENCODE_LOG_LEVEL",
		"OPENCODE_LOG_FORMAT", "OPENCODE_DEFAULT_TIMEOUT",
		"OPENCODE_LLM_PROVIDER", "OPENCODE_LLM_MODEL", "OPENCODE_LLM_API_KEY"} {
		t.Setenv(key, "")
	}

	cfg, err := Load(&FlagOverrides{ConfigPath: &configPath})
	require.NoError(t, err)

	assert.Equal(t, "myopenai", cfg.LLM.ActiveProvider)
	assert.Equal(t, 0.5, *cfg.LLM.DefaultTemperature)
	assert.Equal(t, 4096, *cfg.LLM.DefaultMaxTokens)
	assert.Equal(t, 120*time.Second, cfg.LLM.DefaultTimeout)
	require.Len(t, cfg.LLM.Providers, 2)
	assert.Equal(t, "myopenai", cfg.LLM.Providers[0].Name)
	assert.Equal(t, "openai", cfg.LLM.Providers[0].Type)
	assert.Equal(t, "gpt-4o", cfg.LLM.Providers[0].ModelName)
	assert.Equal(t, "$OPENAI_API_KEY", cfg.LLM.Providers[0].APIKey)
	assert.Equal(t, 0.2, *cfg.LLM.Providers[0].Temperature)
	assert.Equal(t, 8192, *cfg.LLM.Providers[0].MaxTokens)
	assert.Equal(t, "local", cfg.LLM.Providers[1].Name)
	assert.Equal(t, "ollama", cfg.LLM.Providers[1].Type)
}

func TestLoad_LLM_EnvOverrides(t *testing.T) {
	dir := t.TempDir()
	configContent := `
mode: local
target_dir: ` + dir + `
llm:
  active_provider: openai
  providers:
    - name: openai
      type: openai
      model: gpt-4o
    - name: claude
      type: anthropic
      model: claude-sonnet
`
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o644))

	for _, key := range []string{"OPENCODE_MODE", "OPENCODE_TARGET_DIR", "OPENCODE_LOG_LEVEL",
		"OPENCODE_LOG_FORMAT", "OPENCODE_DEFAULT_TIMEOUT"} {
		t.Setenv(key, "")
	}
	t.Setenv("OPENCODE_LLM_PROVIDER", "claude")
	t.Setenv("OPENCODE_LLM_MODEL", "claude-opus")
	t.Setenv("OPENCODE_LLM_API_KEY", "test-key-123")

	cfg, err := Load(&FlagOverrides{ConfigPath: &configPath})
	require.NoError(t, err)

	assert.Equal(t, "claude", cfg.LLM.ActiveProvider)
	assert.Equal(t, "claude-opus", cfg.LLM.ModelOverride)
	assert.Equal(t, "test-key-123", cfg.LLM.APIKeyOverride)
}

func TestLoad_LLM_FlagsOverrideEnv(t *testing.T) {
	dir := t.TempDir()
	configContent := `
mode: local
target_dir: ` + dir + `
llm:
  active_provider: openai
  providers:
    - name: openai
      type: openai
      model: gpt-4o
    - name: claude
      type: anthropic
      model: claude-sonnet
`
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o644))

	for _, key := range []string{"OPENCODE_MODE", "OPENCODE_TARGET_DIR", "OPENCODE_LOG_LEVEL",
		"OPENCODE_LOG_FORMAT", "OPENCODE_DEFAULT_TIMEOUT"} {
		t.Setenv(key, "")
	}
	t.Setenv("OPENCODE_LLM_PROVIDER", "openai")
	t.Setenv("OPENCODE_LLM_MODEL", "")
	t.Setenv("OPENCODE_LLM_API_KEY", "")

	temp := 0.7
	flags := &FlagOverrides{
		ConfigPath:     &configPath,
		LLMProvider:    strPtr("claude"),
		LLMModel:       strPtr("claude-opus"),
		LLMTemperature: &temp,
	}

	cfg, err := Load(flags)
	require.NoError(t, err)

	assert.Equal(t, "claude", cfg.LLM.ActiveProvider)     // flag wins over env
	assert.Equal(t, "claude-opus", cfg.LLM.ModelOverride)  // flag
	assert.Equal(t, 0.7, *cfg.LLM.DefaultTemperature)      // flag
}

// --- ResolveActiveProvider Tests ---

func TestResolveActiveProvider_ByName(t *testing.T) {
	llmCfg := &LLMConfig{
		ActiveProvider: "claude",
		Providers: []llm.ProviderEntry{
			{Name: "openai", Type: "openai", ModelName: "gpt-4o"},
			{Name: "claude", Type: "anthropic", ModelName: "claude-sonnet"},
		},
	}
	p, err := llmCfg.ResolveActiveProvider()
	require.NoError(t, err)
	assert.Equal(t, "claude", p.Name)
	assert.Equal(t, "claude-sonnet", p.ModelName)
}

func TestResolveActiveProvider_SingleEntryFallback(t *testing.T) {
	llmCfg := &LLMConfig{
		Providers: []llm.ProviderEntry{
			{Name: "only", Type: "openai", ModelName: "gpt-4o"},
		},
	}
	p, err := llmCfg.ResolveActiveProvider()
	require.NoError(t, err)
	assert.Equal(t, "only", p.Name)
}

func TestResolveActiveProvider_ModelOverride(t *testing.T) {
	llmCfg := &LLMConfig{
		ActiveProvider: "p",
		ModelOverride:  "gpt-4o-mini",
		Providers: []llm.ProviderEntry{
			{Name: "p", Type: "openai", ModelName: "gpt-4o"},
		},
	}
	p, err := llmCfg.ResolveActiveProvider()
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o-mini", p.ModelName)
}

func TestResolveActiveProvider_APIKeyOverride(t *testing.T) {
	llmCfg := &LLMConfig{
		ActiveProvider: "p",
		APIKeyOverride: "override-key",
		Providers: []llm.ProviderEntry{
			{Name: "p", Type: "openai", ModelName: "m", APIKey: "$ORIGINAL"},
		},
	}
	p, err := llmCfg.ResolveActiveProvider()
	require.NoError(t, err)
	assert.Equal(t, "override-key", p.APIKey)
}

func TestResolveActiveProvider_NotFound(t *testing.T) {
	llmCfg := &LLMConfig{
		ActiveProvider: "missing",
		Providers: []llm.ProviderEntry{
			{Name: "other", Type: "openai", ModelName: "m"},
		},
	}
	_, err := llmCfg.ResolveActiveProvider()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"missing" not found`)
}

func TestResolveActiveProvider_NoProviders(t *testing.T) {
	llmCfg := &LLMConfig{}
	_, err := llmCfg.ResolveActiveProvider()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no providers configured")
}

func TestResolveActiveProvider_NoActiveMultipleProviders(t *testing.T) {
	llmCfg := &LLMConfig{
		Providers: []llm.ProviderEntry{
			{Name: "a", Type: "openai", ModelName: "m"},
			{Name: "b", Type: "anthropic", ModelName: "m"},
		},
	}
	_, err := llmCfg.ResolveActiveProvider()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active provider configured")
}

func TestActiveProviderName_Explicit(t *testing.T) {
	llmCfg := &LLMConfig{ActiveProvider: "claude"}
	assert.Equal(t, "claude", llmCfg.ActiveProviderName())
}

func TestActiveProviderName_SingleFallback(t *testing.T) {
	llmCfg := &LLMConfig{
		Providers: []llm.ProviderEntry{{Name: "only"}},
	}
	assert.Equal(t, "only", llmCfg.ActiveProviderName())
}

func TestActiveProviderName_Empty(t *testing.T) {
	llmCfg := &LLMConfig{
		Providers: []llm.ProviderEntry{{Name: "a"}, {Name: "b"}},
	}
	assert.Equal(t, "", llmCfg.ActiveProviderName())
}

func TestFindProvider(t *testing.T) {
	llmCfg := &LLMConfig{
		Providers: []llm.ProviderEntry{
			{Name: "a", Type: "openai"},
			{Name: "b", Type: "anthropic"},
		},
	}
	assert.NotNil(t, llmCfg.FindProvider("b"))
	assert.Equal(t, "anthropic", llmCfg.FindProvider("b").Type)
	assert.Nil(t, llmCfg.FindProvider("c"))
}

func TestValidate_LLM_AllProviderTypes(t *testing.T) {
	for _, typ := range []string{"openai", "anthropic", "gemini", "ollama", "custom"} {
		t.Run(typ, func(t *testing.T) {
			cfg := validBaseConfig()
			cfg.LLM = LLMConfig{
				ActiveProvider: "p",
				Providers: []llm.ProviderEntry{
					{Name: "p", Type: typ, ModelName: "m"},
				},
			}
			assert.NoError(t, Validate(cfg))
		})
	}
}

func TestLoad_LLM_AzureOpenAIConfigFile(t *testing.T) {
	dir := t.TempDir()
	configContent := `
mode: local
target_dir: ` + dir + `
llm:
  active_provider: azure-gpt
  providers:
    - name: azure-gpt
      type: openai
      endpoint_url: "https://example.com/v1/chat/completions"
      model: gpt-4o
      api_key: "$AZURE_OPENAI_API_KEY"
      headers:
        api-key: "$AZURE_OPENAI_API_KEY"
`
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0o644))

	clearLLMEnv(t)

	cfg, err := Load(&FlagOverrides{ConfigPath: &configPath})
	require.NoError(t, err)

	require.Len(t, cfg.LLM.Providers, 1)
	p := cfg.LLM.Providers[0]
	assert.Equal(t, "azure-gpt", p.Name)
	assert.Equal(t, "openai", p.Type)
	assert.Equal(t, "https://example.com/v1/chat/completions", p.EndpointURL)
	assert.Equal(t, "gpt-4o", p.ModelName)
	assert.Equal(t, "$AZURE_OPENAI_API_KEY", p.APIKey)
	assert.Equal(t, "$AZURE_OPENAI_API_KEY", p.Headers["api-key"])
	assert.Equal(t, "azure-gpt", cfg.LLM.ActiveProvider)
}

func TestLoad_ConfigFileDiscoveredInTargetDir(t *testing.T) {
	dir := t.TempDir()
	configContent := `
mode: local
target_dir: ` + dir + `
llm:
  active_provider: local-llm
  providers:
    - name: local-llm
      type: ollama
      model: llama3.1
`
	// Write .opencode.yaml inside the target dir, not cwd.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".opencode.yaml"), []byte(configContent), 0o644))

	clearLLMEnv(t)

	// Pass target dir via flag, no explicit config path.
	cfg, err := Load(&FlagOverrides{TargetDir: &dir})
	require.NoError(t, err)

	require.Len(t, cfg.LLM.Providers, 1)
	assert.Equal(t, "local-llm", cfg.LLM.Providers[0].Name)
	assert.Equal(t, "ollama", cfg.LLM.Providers[0].Type)
	assert.Equal(t, "llama3.1", cfg.LLM.Providers[0].ModelName)
}

func TestLoad_ConfigFileDiscoveredInTargetDirViaEnv(t *testing.T) {
	dir := t.TempDir()
	configContent := `
mode: local
target_dir: ` + dir + `
llm:
  active_provider: env-llm
  providers:
    - name: env-llm
      type: anthropic
      model: claude-sonnet
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".opencode.yaml"), []byte(configContent), 0o644))

	clearLLMEnv(t)
	t.Setenv("OPENCODE_TARGET_DIR", dir)

	cfg, err := Load(nil)
	require.NoError(t, err)

	require.Len(t, cfg.LLM.Providers, 1)
	assert.Equal(t, "env-llm", cfg.LLM.Providers[0].Name)
}

// clearLLMEnv clears all env vars that could interfere with config tests.
func clearLLMEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"OPENCODE_MODE", "OPENCODE_TARGET_DIR", "OPENCODE_LOG_LEVEL",
		"OPENCODE_LOG_FORMAT", "OPENCODE_DEFAULT_TIMEOUT",
		"OPENCODE_LLM_PROVIDER", "OPENCODE_LLM_MODEL", "OPENCODE_LLM_API_KEY",
	} {
		t.Setenv(key, "")
	}
}
