package main

import (
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/fatih/color"
)

// StrategyCmd sets the merge/rebase strategy for the current tower without
// changing its base branch.
type StrategyCmd struct {
	Strategy string `arg:"" help:"Strategy to use for the current tower: 'merge' or 'rebase'" enum:"merge,rebase"`
}

func (s *StrategyCmd) Run(_ *kong.Context) error {
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	accepted, err := confirmAndSetTowerStrategy(currentTower, s.Strategy)
	if err != nil {
		return err
	}
	if !accepted {
		return nil
	}

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Set strategy for tower '%s' to '%s' in repository at '%s'\n", currentTower.Name, currentTower.strategy(), repoPath)
	return nil
}

// confirmAndSetTowerStrategy validates and applies a strategy change after
// confirming changes that would alter how future tower operations work. It
// returns false when the user declines the change.
func confirmAndSetTowerStrategy(tower *Tower, strategy string) (bool, error) {
	if strategy != StrategyMerge && strategy != StrategyRebase {
		return false, fmt.Errorf("invalid tower strategy %q: use 'merge' or 'rebase'", strategy)
	}

	if tower.strategy() != strategy {
		if tower.operationInProgress() {
			return false, fmt.Errorf("a merge/rebase is in progress for tower '%s'. Finish or cancel it before changing strategy", tower.Name)
		}

		warningColor := color.New(color.FgYellow).Add(color.Bold)
		warningColor.Printf("WARNING: changing strategy '%s' -> '%s' for tower '%s'.\n", tower.strategy(), strategy, tower.Name)
		fmt.Println("Existing branch history won't be rewritten; the new strategy applies to future merge/land/sync operations.")
		fmt.Print("Continue? [y/N]: ")

		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Println("Strategy change cancelled.")
			return false, nil
		}
	}

	tower.Strategy = strategy
	return true, nil
}
