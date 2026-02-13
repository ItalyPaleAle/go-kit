package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mitchellh/go-homedir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Config represents the application configuration.
type Config struct {
	Foo string `yaml:"foo"`
	Bar int    `yaml:"bar"`

	// Internal keys
	internal internal `yaml:"-"`
}

// Internal properties.
type internal struct {
	configFileLoaded string // Path to the config file that was loaded
}

// SetLoadedConfigPath sets the path to the config file that was loaded.
func (c *Config) SetLoadedConfigPath(filePath string) {
	c.internal.configFileLoaded = filePath
}

func TestLoadConfig_EnvVarPath_Success(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "custom.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("foo: env\nbar: 42\n"), 0o600))

	t.Setenv("APP_CONFIG", configPath)

	cfg := &Config{}
	err := LoadConfig(cfg, LoadConfigOpts{
		EnvVar:  "APP_CONFIG",
		DirName: "myapp",
	})
	require.NoError(t, err)

	assert.Equal(t, "env", cfg.Foo)
	assert.Equal(t, 42, cfg.Bar)
	assert.Equal(t, configPath, cfg.internal.configFileLoaded)
}

func TestLoadConfig_EnvVarPath_FileMissingReturnsError(t *testing.T) {
	t.Setenv("APP_CONFIG", "/path/does/not/exist.yaml")

	cfg := &Config{}
	err := LoadConfig(cfg, LoadConfigOpts{
		EnvVar:  "APP_CONFIG",
		DirName: "myapp",
	})
	require.Error(t, err)

	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	require.ErrorContains(t, err, "Environmental variable APP_CONFIG points to a file that does not exist")
	require.ErrorContains(t, err, "Error loading config file")
	assert.Empty(t, cfg.internal.configFileLoaded)
}

func TestLoadConfig_EnvVarPath_TakesPrecedenceOverDefaultSearchPaths(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte("foo: from-default\nbar: 1\n"), 0o600))

	envConfigPath := filepath.Join(tmpDir, "env-config.yaml")
	require.NoError(t, os.WriteFile(envConfigPath, []byte("foo: from-env\nbar: 2\n"), 0o600))
	t.Setenv("APP_CONFIG", envConfigPath)

	cfg := &Config{}
	err := LoadConfig(cfg, LoadConfigOpts{
		EnvVar:  "APP_CONFIG",
		DirName: "myapp",
	})
	require.NoError(t, err)

	assert.Equal(t, "from-env", cfg.Foo)
	assert.Equal(t, 2, cfg.Bar)
	assert.Equal(t, envConfigPath, cfg.internal.configFileLoaded)
}

func TestLoadConfig_SearchesCurrentDirForConfigYAML(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("APP_CONFIG", "")

	configPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("foo: current-dir\nbar: 7\n"), 0o600))

	cfg := &Config{}
	err := LoadConfig(cfg, LoadConfigOpts{
		EnvVar:  "APP_CONFIG",
		DirName: "myapp",
	})
	require.NoError(t, err)

	assert.Equal(t, "current-dir", cfg.Foo)
	assert.Equal(t, 7, cfg.Bar)
	assert.Equal(t, "config.yaml", cfg.internal.configFileLoaded)
}

func TestLoadConfig_FallsBackToConfigYML(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("APP_CONFIG", "")

	configPath := filepath.Join(tmpDir, "config.yml")
	require.NoError(t, os.WriteFile(configPath, []byte("foo: yml\nbar: 33\n"), 0o600))

	cfg := &Config{}
	err := LoadConfig(cfg, LoadConfigOpts{
		EnvVar:  "APP_CONFIG",
		DirName: "myapp",
	})
	require.NoError(t, err)

	assert.Equal(t, "yml", cfg.Foo)
	assert.Equal(t, 33, cfg.Bar)
	assert.Equal(t, "config.yml", cfg.internal.configFileLoaded)
}

func TestLoadConfig_SearchesHomeDir(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("APP_CONFIG", "")
	t.Setenv("HOME", tmpDir)
	homedir.Reset()
	t.Cleanup(homedir.Reset)

	homeConfigDir := filepath.Join(tmpDir, ".myapp")
	require.NoError(t, os.MkdirAll(homeConfigDir, 0o700))

	configPath := filepath.Join(homeConfigDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("foo: home\nbar: 9\n"), 0o600))

	cfg := &Config{}
	err := LoadConfig(cfg, LoadConfigOpts{
		EnvVar:  "APP_CONFIG",
		DirName: "myapp",
	})
	require.NoError(t, err)

	assert.Equal(t, "home", cfg.Foo)
	assert.Equal(t, 9, cfg.Bar)
	assert.Equal(t, configPath, cfg.internal.configFileLoaded)
}

func TestLoadConfig_NoConfigFoundReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("APP_CONFIG", "")
	t.Setenv("HOME", tmpDir)

	cfg := &Config{}
	err := LoadConfig(cfg, LoadConfigOpts{
		EnvVar:  "APP_CONFIG",
		DirName: "myapp",
	})
	require.Error(t, err)

	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	require.ErrorContains(t, err, "Could not find a configuration file config.yaml")
	require.ErrorContains(t, err, "Error loading config file")
	assert.Empty(t, cfg.internal.configFileLoaded)
}

func TestLoadConfig_UnknownFieldReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	t.Setenv("APP_CONFIG", "")

	configPath := filepath.Join(tmpDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("foo: ok\nbar: 1\nbaz: nope\n"), 0o600))

	cfg := &Config{}
	err := LoadConfig(cfg, LoadConfigOpts{
		EnvVar:  "APP_CONFIG",
		DirName: "myapp",
	})
	require.Error(t, err)

	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	require.ErrorContains(t, err, "failed to decode config file")
	require.ErrorContains(t, err, "field baz not found")
	assert.Empty(t, cfg.internal.configFileLoaded)
}
