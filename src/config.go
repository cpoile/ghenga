package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config represents the top-level configuration
type Config struct {
	Repos []*Repo `toml:"repos"`
}

// Repo represents a git repository configuration
type Repo struct {
	Path   string   `toml:"path"`
	Towers []*Tower `toml:"towers"`
}

// Tower represents a stack of branches
type Tower struct {
	Name     string   `toml:"name"`
	Branches []Branch `toml:"branches"`
}

// Branch represents a git branch
type Branch struct {
	Name string `toml:"name"`
}

// ConfigPathFunc is a function type that returns the path to the config file
type ConfigPathFunc func() (string, error)

// ConfigPath is a variable that holds the function to get the config path
var ConfigPath ConfigPathFunc = defaultConfigPath

// defaultConfigPath returns the default path to the config file
func defaultConfigPath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}

	// Create ghenga config directory if it doesn't exist
	ghengaDir := filepath.Join(configDir, "ghenga")
	if err := os.MkdirAll(ghengaDir, 0755); err != nil {
		return "", err
	}

	return filepath.Join(ghengaDir, "config.toml"), nil
}

// LoadConfig loads the configuration from the config file
func LoadConfig() (*Config, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	// Check if config file exists
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		// Create default config if it doesn't exist
		defaultConfig := &Config{
			Repos: []*Repo{},
		}
		if err := SaveConfig(defaultConfig); err != nil {
			return nil, err
		}
		return defaultConfig, nil
	}

	// Read and parse config file
	var config Config
	_, err = toml.DecodeFile(configPath, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

// SaveConfig saves the configuration to the config file
func SaveConfig(config *Config) error {
	configPath, err := ConfigPath()
	if err != nil {
		return err
	}

	// Create the file
	file, err := os.Create(configPath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Encode config to TOML and write to file
	encoder := toml.NewEncoder(file)
	return encoder.Encode(config)
}
