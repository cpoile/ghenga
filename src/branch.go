package main

import (
	"fmt"
	"slices"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/posener/complete"
)

const MAX_BRANCHES_TO_PICK_BASE_FROM = 20

// SetCurrentTower sets the current tower for a repository
func SetCurrentTower(repo *RepoInfo, towerName string) error {
	tower := findTowerByName(repo, towerName)
	if tower == nil {
		return fmt.Errorf("tower '%s' not found", towerName)
	}

	repo.Current = towerName
	return nil
}

// GetCurrentTower gets the current tower for a repository
// If no current tower is set, returns the first tower or nil if no towers exist
func GetCurrentTower(repo *RepoInfo) *Tower {
	if repo.Current != "" {
		tower := findTowerByName(repo, repo.Current)
		if tower != nil {
			return tower
		}
	}

	// Fallback to first tower if available
	if len(repo.Towers) > 0 {
		return repo.Towers[0]
	}

	return nil
}

type BranchCmd struct {
	Add AddCmd `cmd:"add" help:"Add a branch to a tower in current repository"`
	Rm  RmCmd  `cmd:"rm" help:"Remove a branch from the current tower"`
}

type AddCmd struct {
	Name  string `arg:"" help:"Name of the branch to add" predictor:"predictBranches"`
	Tower string `help:"Name of the tower to add the branch to (optional, defaults to current tower)"`
}

func (a *AddCmd) Run(_ *kong.Context) error {
	config, repo, repoPath, err := loadOrCreateRepoConfig()
	if err != nil {
		return err
	}

	targetTowerName := a.Tower
	if targetTowerName == "" && repo.Current != "" {
		targetTowerName = repo.Current
	}
	if targetTowerName == "" {
		return fmt.Errorf("no tower specified, use 'ghenga current <tower-name>' to set one or specify --tower")
	}

	tower := findOrCreateTower(repo, targetTowerName)

	if containsBranch(tower, a.Name) {
		return fmt.Errorf("branch '%s' already exists in tower '%s'", a.Name, targetTowerName)
	}

	r, err := openGitRepo()
	if err != nil {
		return err
	}

	// Verify the branch to add exists
	addBranchRefName := plumbing.NewBranchReferenceName(a.Name)
	addBranchRef, err := r.Reference(addBranchRefName, true)
	if err != nil {
		return fmt.Errorf("branch '%s' not found in repository", a.Name)
	}

	// If this is the first branch, prompt for a base branch
	if len(tower.Branches) == 0 {
		defaultBranch := detectDefaultBranch(r)

		branchLister := BranchLister{}
		branches := branchLister.Predict(complete.Args{})

		if len(branches) == 0 {
			return fmt.Errorf("no branches found in repository")
		}

		for i, branch := range branches {
			if branch == defaultBranch {
				branches = slices.Delete(branches, i, i+1)
				branches = append([]string{defaultBranch}, branches...)
				break
			}
		}

		fmt.Printf("This is the first branch in tower '%s'. Please select a base branch.\n", tower.Name)
		fmt.Println("Available branches:")

		// Display branches with numbers
		for i, branch := range branches {
			if i < 9 {
				if branch == defaultBranch {
					fmt.Printf(" %d: %s (default)\n", i+1, branch)
				} else {
					fmt.Printf(" %d: %s\n", i+1, branch)
				}
			} else {
				fmt.Printf("%d: %s\n", i+1, branch)
			}

			if i >= MAX_BRANCHES_TO_PICK_BASE_FROM-1 {
				fmt.Println("... and more")
				break
			}
		}

		fmt.Printf("\nEnter branch number or name (default is 1, %s): ", defaultBranch)
		var input string
		fmt.Scanln(&input)

		var baseBranch string

		if input == "" {
			baseBranch = defaultBranch
		} else {
			var selection int
			_, err := fmt.Sscanf(input, "%d", &selection)
			if err == nil && selection > 0 && selection <= len(branches) && selection <= MAX_BRANCHES_TO_PICK_BASE_FROM {
				baseBranch = branches[selection-1]
			} else {
				// Input was not a number or out of range, treat as branch name
				baseBranch = input
			}
		}

		baseBranchRefName := plumbing.NewBranchReferenceName(baseBranch)
		baseBranchRef, err := r.Reference(baseBranchRefName, true)
		if err != nil {
			return fmt.Errorf("base branch '%s' not found in repository", baseBranch)
		}

		mergeBase, err := findMergeBase(r, addBranchRef.Hash(), baseBranchRef.Hash())
		if err != nil {
			fmt.Printf("Could not find merge base, using HEAD of %s as tower base\n", baseBranch)
			tower.Base = baseBranchRef.Hash().String()
		} else {
			tower.Base = mergeBase.String()
			fmt.Printf("Found merge base between %s and %s, using it as tower base\n", baseBranch, a.Name)
			fmt.Println("If this is not what you want, you can set the base branch manually using 'ghenga base <commit>'")
		}
	}

	tower.Branches = append(tower.Branches, Branch{Name: a.Name})

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Added branch '%s' to tower '%s' in repository at '%s'\n", a.Name, tower.Name, repoPath)
	return nil
}

type RmCmd struct {
	Name string `arg:"" help:"Name of the branch to remove" predictor:"predictTowerBranches"`
}

func (r *RmCmd) Run(_ *kong.Context) error {
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	branchIndex := -1
	for i, branch := range currentTower.Branches {
		if branch.Name == r.Name {
			branchIndex = i
			break
		}
	}

	if branchIndex == -1 {
		return fmt.Errorf("branch '%s' not found in tower '%s'", r.Name, currentTower.Name)
	}

	currentTower.Branches = slices.Delete(currentTower.Branches, branchIndex, branchIndex+1)

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Removed branch '%s' from tower '%s' in repository at '%s'\n", r.Name, currentTower.Name, repoPath)
	return nil
}
