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
	Name        string       `toml:"name"`
	Base        string       `toml:"base"`
	Branches    []Branch     `toml:"branches"`
	LastRebased string       `toml:"last_rebased,omitempty"` // Timestamp of the last rebase operation
	LastSynced  string       `toml:"last_synced,omitempty"`  // Timestamp of last sync
	RebaseState *RebaseState `toml:"rebaseState,omitempty"`  // Stores state if a rebase is paused
}

// Branch represents a git branch
type Branch struct {
	Name            string `toml:"name"`
	LastReflogID    string `toml:"last_reflog_id,omitempty"`     // Stores the commit hash before last rebase for undo operations
	PreSyncReflogID string `toml:"pre_sync_reflog_id,omitempty"` // Used for sync undo
}

// Minimal info needed to restart rebase for a branch
type BranchRebaseInfo struct {
	Name           string   `toml:"name"`
	BaseBranchName string   `toml:"baseBranchName"`
	UniqueCommits  []string `toml:"uniqueCommits"`
}

// RebaseState stores the necessary information to resume a paused rebase operation
type RebaseState struct {
	IsInProgress         bool               `toml:"isInProgress"`
	TargetBranch         string             `toml:"targetBranch"`         // Branch currently being rebased
	BaseBranch           string             `toml:"baseBranch"`           // Base for the current target
	TemporaryBranch      string             `toml:"temporaryBranch"`      // Temp branch holding picks
	CurrentCommitIndex   int                `toml:"currentCommitIndex"`   // Index in RemainingCommits that failed or is next
	RemainingCommits     []string           `toml:"remainingCommits"`     // Commits for the TargetBranch
	OriginalBranch       string             `toml:"originalBranch"`       // Branch to return to upon completion
	RemainingBranchInfos []BranchRebaseInfo `toml:"remainingBranchInfos"` // Info for branches yet to be processed (including current one if paused)
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
