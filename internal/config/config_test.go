package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }

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
