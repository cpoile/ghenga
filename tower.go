package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/fatih/color"
	"github.com/go-git/go-git/v5/plumbing"
)

type NewCmd struct {
	Name string `arg:"" help:"Name of the tower to create"`
}

func (n *NewCmd) Run(_ *kong.Context) error {
	config, repo, repoPath, err := loadOrCreateRepoConfig()
	if err != nil {
		return err
	}

	for _, tower := range repo.Towers {
		if tower.Name == n.Name {
			return fmt.Errorf("tower '%s' already exists in repository at '%s'", n.Name, repoPath)
		}
	}

	tower := findOrCreateTower(repo, n.Name)

	repo.Current = n.Name

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Created tower '%s' in repository at '%s' and set it as the current tower\n", tower.Name, repoPath)
	return nil
}

type CurrentCmd struct {
	Tower string `arg:"" help:"Name of the tower to set as current" predictor:"predictTowers"`
}

func (c *CurrentCmd) Run(_ *kong.Context) error {
	config, repo, repoPath, err := loadRepoConfig()
	if err != nil {
		// Handle the specific error where the repo is not found but config loaded
		if repo == nil && strings.Contains(err.Error(), "not found in configuration") {
			return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
		}
		return err
	}

	if err := SetCurrentTower(repo, c.Tower); err != nil {
		return err
	}

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Set current tower to '%s' in repository at '%s'\n", c.Tower, repoPath)
	return nil
}

type RenameCmd struct {
	NewName string `arg:"" help:"New name for the current tower"`
}

func (r *RenameCmd) Run(_ *kong.Context) error {
	config, repoInfo, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	for _, tower := range repoInfo.Towers {
		if tower.Name == r.NewName {
			return fmt.Errorf("tower with name '%s' already exists", r.NewName)
		}
	}

	oldName := currentTower.Name
	currentTower.Name = r.NewName
	repoInfo.Current = r.NewName

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Renamed tower from '%s' to '%s' in repository at '%s'\n", oldName, r.NewName, repoPath)
	return nil
}

type BaseCmd struct {
	BaseBranch string `arg:"" help:"Branch name to set as the tower's base" predictor:"predictGitRefs"`
}

func (b *BaseCmd) Run(_ *kong.Context) error {
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	r, err := openGitRepo()
	if err != nil {
		return err
	}

	// Validate that the branch exists
	refName := plumbing.NewBranchReferenceName(b.BaseBranch)
	_, err = r.Reference(refName, true) // 'true' resolves symbolic refs like HEAD
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			// Also check remote refs just in case it's not local yet
			remoteRefName := plumbing.NewRemoteReferenceName("origin", b.BaseBranch) // Assuming "origin"
			_, errRem := r.Reference(remoteRefName, true)
			if errors.Is(errRem, plumbing.ErrReferenceNotFound) {
				return fmt.Errorf("branch '%s' not found locally or on origin", b.BaseBranch)
			} else if errRem != nil {
				return fmt.Errorf("failed to check remote branch '%s': %w", b.BaseBranch, errRem)
			}
			// If remote exists but local doesn't, it's still valid to set as base
		} else {
			return fmt.Errorf("failed to validate base branch '%s': %w", b.BaseBranch, err)
		}
	}

	currentTower.Base = b.BaseBranch // Store the branch name

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Set base branch for tower '%s' to '%s' in repository at '%s'\n", currentTower.Name, currentTower.Base, repoPath)
	return nil
}

type RmTowerCmd struct {
	Name string `arg:"" help:"Name of the tower to remove" predictor:"predictTowers"`
}

func (r *RmTowerCmd) Run(_ *kong.Context) error {
	config, repo, repoPath, err := loadRepoConfig()
	if err != nil {
		// Handle the specific error where the repo is not found but config loaded
		if repo == nil && strings.Contains(err.Error(), "not found in configuration") {
			return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
		}
		return err
	}

	towerIndex := -1
	for i, tower := range repo.Towers {
		if tower.Name == r.Name {
			towerIndex = i
			break
		}
	}

	if towerIndex == -1 {
		return fmt.Errorf("tower '%s' not found in repository", r.Name)
	}

	isCurrent := repo.Current == r.Name

	warningColor := color.New(color.FgRed).Add(color.Bold)

	if isCurrent {
		warningColor.Printf("WARNING: You are about to remove the current tower '%s'!\n", r.Name)
	} else {
		warningColor.Printf("WARNING: You are about to remove tower '%s'!\n", r.Name)
	}

	branchCount := len(repo.Towers[towerIndex].Branches)
	if branchCount > 0 {
		warningColor.Printf("This tower contains %d branch(es). The branches will still exist, but the tower that tracks them will be removed.\n", branchCount)
	}

	fmt.Print("Are you sure you want to continue? [y/N]: ")

	var response string
	fmt.Scanln(&response)

	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Operation cancelled.")
		return nil
	}

	repo.Towers = slices.Delete(repo.Towers, towerIndex, towerIndex+1)

	if isCurrent {
		repo.Current = ""
	}

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Removed tower '%s' from repository at '%s'\n", r.Name, repoPath)
	if isCurrent {
		fmt.Println("Note: Removed the current tower. Use 'ghenga current <tower-name>' to set a new current tower.")
	}

	return nil
}
