package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mitchellh/go-homedir"
	yaml "sigs.k8s.io/yaml/goyaml.v3"

	fskit "github.com/italypaleale/go-kit/fs"
)

type LoadConfigOpts struct {
	EnvVar  string
	DirName string
}

type ConfigDest interface {
	SetLoadedConfigPath(path string)
}

func LoadConfig(dst ConfigDest, opts LoadConfigOpts) error {
	// Get the path to the config.yaml
	// First, try with the env var
	configFile := os.Getenv(opts.EnvVar)
	if configFile != "" {
		exists, _ := fskit.FileExists(configFile)
		if !exists {
			return NewConfigError("Environmental variable "+opts.EnvVar+" points to a file that does not exist", "Error loading config file")
		}
	} else {
		// Look in the default paths
		searchPaths := []string{".", "~/." + opts.DirName, "/etc/" + opts.DirName}

		// Note: It's .yaml not .yml! https://yaml.org/faq.html (insert "it's leviOsa, not levioSA" meme)
		configFile = findConfigFile("config.yaml", searchPaths...)
		if configFile == "" {
			// Ok, if you really, really want to use ".yml"....
			configFile = findConfigFile("config.yml", searchPaths...)
		}

		// Config file not found
		if configFile == "" {
			return NewConfigError("Could not find a configuration file config.yaml in the current folder, '~/."+opts.DirName+"', or '/etc/"+opts.DirName+"'", "Error loading config file")
		}
	}

	// Load the configuration
	// Note that configFile can be empty
	err := loadConfigFile(dst, configFile)
	if err != nil {
		return NewConfigError(err, "Error loading config file")
	}
	dst.SetLoadedConfigPath(configFile)

	return nil
}

// Loads the configuration from a file and from the environment.
// "dst" must be a pointer to a struct.
func loadConfigFile(dst any, filePath string) error {
	f, err := os.Open(filePath) //nolint:gosec
	if err != nil {
		return fmt.Errorf("failed to open config file '%s': %w", filePath, err)
	}
	defer f.Close() //nolint:errcheck

	yamlDec := yaml.NewDecoder(f)
	yamlDec.KnownFields(true)
	err = yamlDec.Decode(dst)
	if err != nil {
		return fmt.Errorf("failed to decode config file '%s': %w", filePath, err)
	}

	return nil
}

func findConfigFile(fileName string, searchPaths ...string) string {
	for _, path := range searchPaths {
		if path == "" {
			continue
		}

		p, _ := homedir.Expand(path)
		if p != "" {
			path = p
		}

		search := filepath.Join(path, fileName)
		exists, _ := fskit.FileExists(search)
		if exists {
			return search
		}
	}

	return ""
}
