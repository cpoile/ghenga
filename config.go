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

// Tower strategy values. A tower either rebases its branches up onto a moving
// base (rewriting commits) or merges the base down into each branch (preserving
// commits, which keeps reviewer state on higher PRs). An empty Strategy means
// rebase, so towers created before this field existed keep working unchanged.
const (
	StrategyRebase = "rebase"
	StrategyMerge  = "merge"
)

// Tower represents a stack of branches
type Tower struct {
	Name        string       `toml:"name"`
	Base        string       `toml:"base"`
	Strategy    string       `toml:"strategy,omitempty"` // "rebase" (default) or "merge"
	Branches    []Branch     `toml:"branches"`
	LastRebased string       `toml:"last_rebased,omitempty"` // Timestamp of the last rebase operation
	LastSynced  string       `toml:"last_synced,omitempty"`  // Timestamp of last sync
	RebaseState *RebaseState `toml:"rebaseState,omitempty"`  // Stores state if a rebase is paused
	MergeState  *MergeState  `toml:"mergeState,omitempty"`   // Stores state if a merge is paused
	Checkpoints []Checkpoint `toml:"checkpoints,omitempty"`  // Named snapshots of branch positions
}

// CheckpointBranch records one branch's position within a checkpoint.
type CheckpointBranch struct {
	Name string `toml:"name"`
	Hash string `toml:"hash"`
}

// Checkpoint is a named snapshot of a tower: the ordered branch list with each
// branch's commit hash, plus the base at capture time. 'ghenga restore' moves
// the branches back to these positions and restores the tower's membership and
// base. The default Name is a timestamp when none is given.
type Checkpoint struct {
	Name     string             `toml:"name"`
	Created  string             `toml:"created"` // RFC3339 capture time
	Base     string             `toml:"base"`
	Branches []CheckpointBranch `toml:"branches"`
}

// strategy returns the tower's configured strategy, defaulting to rebase when
// unset (for backward compatibility with towers created before strategies).
func (t *Tower) strategy() string {
	if t.Strategy == StrategyMerge {
		return StrategyMerge
	}
	return StrategyRebase
}

// operationInProgress reports whether a rebase or merge is paused mid-conflict.
func (t *Tower) operationInProgress() bool {
	return (t.RebaseState != nil && t.RebaseState.IsInProgress) ||
		(t.MergeState != nil && t.MergeState.IsInProgress)
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

// BranchMergeInfo is the minimal info needed to merge a branch's base into it
// when resuming a paused merge.
type BranchMergeInfo struct {
	Name           string `toml:"name"`
	BaseBranchName string `toml:"baseBranchName"` // ref to merge into Name
}

// MergeState stores the information needed to resume a paused merge operation.
// Unlike a rebase (which replays commits one at a time), a merge is atomic per
// branch, so there is no per-commit index to track — only which branch is being
// merged and which branches remain.
type MergeState struct {
	IsInProgress      bool              `toml:"isInProgress"`
	TargetBranch      string            `toml:"targetBranch"`      // branch currently being merged into
	BaseBranch        string            `toml:"baseBranch"`        // ref being merged into TargetBranch
	OriginalBranch    string            `toml:"originalBranch"`    // branch to restore upon completion (cwd's branch)
	MainRepoBranch    string            `toml:"mainRepoBranch"`    // branch the main repo was on (differs from OriginalBranch when run from worktree)
	MergedBranches    []string          `toml:"mergedBranches"`    // all branch names in this operation (for worktree reset filtering)
	RemainingBranches []BranchMergeInfo `toml:"remainingBranches"` // branches yet to be processed (including the current one if paused)
}

// RebaseState stores the necessary information to resume a paused rebase operation
type RebaseState struct {
	IsInProgress         bool               `toml:"isInProgress"`
	TargetBranch         string             `toml:"targetBranch"`         // Branch currently being rebased
	BaseBranch           string             `toml:"baseBranch"`           // Base for the current target
	TemporaryBranch      string             `toml:"temporaryBranch"`      // Temp branch holding picks
	CurrentCommitIndex   int                `toml:"currentCommitIndex"`   // Index in RemainingCommits that failed or is next
	RemainingCommits     []string           `toml:"remainingCommits"`     // Commits for the TargetBranch
	OriginalBranch       string             `toml:"originalBranch"`       // Branch to return to upon completion (CWD's branch)
	MainRepoBranch       string             `toml:"mainRepoBranch"`       // Branch the main repo was on before rebase (differs from OriginalBranch when run from worktree)
	RebasedBranches      []string           `toml:"rebasedBranches"`      // All branch names planned for rebase (for worktree reset filtering)
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
