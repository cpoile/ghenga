package main

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config represents the top-level configuration
type Config struct {
	Repos []*RepoInfo `toml:"repos"`
}

// RepoInfo represents Ghenga's git repository configuration
type RepoInfo struct {
	Path    string   `toml:"path"`
	Current string   `toml:"current"`
	Towers  []*Tower `toml:"towers"`
}

// Tower represents a stack of branches
type Tower struct {
	Name        string   `toml:"name"`
	Base        string   `toml:"base"`
	Branches    []Branch `toml:"branches"`
	LastRebased string   `toml:"last_rebased,omitempty"` // Timestamp of the last rebase operation
}

// Branch represents a git branch
type Branch struct {
	Name         string `toml:"name"`
	LastReflogID string `toml:"last_reflog_id,omitempty"` // Stores the commit hash before last rebase for undo operations
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

	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		defaultConfig := &Config{
			Repos: []*RepoInfo{},
		}
		if err := SaveConfig(defaultConfig); err != nil {
			return nil, err
		}
		return defaultConfig, nil
	}

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

	file, err := os.Create(configPath)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := toml.NewEncoder(file)
	return encoder.Encode(config)
}
