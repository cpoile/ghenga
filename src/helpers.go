package main

import (
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// loadRepoConfig gets the current repository path, loads the configuration,
// and finds the repository entry in the config. It returns an error if the
// repository is not found in the config.
func loadRepoConfig() (*Config, *RepoInfo, string, error) {
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to get current repository: %w", err)
	}

	config, err := LoadConfig()
	if err != nil {
		return nil, nil, repoPath, fmt.Errorf("failed to load configuration: %w", err)
	}

	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return config, nil, repoPath, fmt.Errorf("repository at '%s' not found in configuration. Run 'ghenga init'?", repoPath)
	}

	return config, repo, repoPath, nil
}

// loadOrCreateRepoConfig behaves like loadRepoConfig but creates the repository
// entry if it doesn't exist.
func loadOrCreateRepoConfig() (*Config, *RepoInfo, string, error) {
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to get current repository: %w", err)
	}

	config, err := LoadConfig()
	if err != nil {
		return nil, nil, repoPath, fmt.Errorf("failed to load configuration: %w", err)
	}

	repo := FindOrCreateRepo(config, repoPath)

	return config, repo, repoPath, nil
}

// getCurrentTower retrieves the current tower based on the repository's current setting.
func getCurrentTower(repo *RepoInfo) (*Tower, error) {
	if repo.Current == "" {
		return nil, fmt.Errorf("no current tower set, use 'ghenga current <tower-name>' to set one")
	}
	currentTower := FindTowerByName(repo, repo.Current)
	if currentTower == nil {
		// This case should ideally not happen if repo.Current is set correctly,
		// but good to handle defensively.
		return nil, fmt.Errorf("current tower '%s' referenced but not found", repo.Current)
	}
	return currentTower, nil
}

// loadRepoInfoAndCurrentTower loads the config, finds the repo info, and gets the current tower.
func loadRepoInfoAndCurrentTower() (*Config, *RepoInfo, *Tower, string, error) {
	config, repo, repoPath, err := loadRepoConfig()
	if err != nil {
		return nil, nil, nil, repoPath, err
	}

	currentTower, err := getCurrentTower(repo)
	if err != nil {
		return nil, nil, nil, repoPath, err
	}

	return config, repo, currentTower, repoPath, nil
}

// openGitRepo opens the Git repository in the current directory.
func openGitRepo() (*git.Repository, error) {
	r, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open git repository in current directory: %w", err)
	}
	return r, nil
}

// findMergeBase finds the merge base (common ancestor) between two commits
func findMergeBase(r *git.Repository, commit1, commit2 plumbing.Hash) (plumbing.Hash, error) {
	// If commits are the same, return immediately
	if commit1 == commit2 {
		return commit1, nil
	}

	// Get commit objects
	c1, err := r.CommitObject(commit1)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	c2, err := r.CommitObject(commit2)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	// Find merge base using go-git's MergeBase function
	mergeBase, err := c1.MergeBase(c2)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	if len(mergeBase) == 0 {
		return plumbing.ZeroHash, fmt.Errorf("no common ancestor found")
	}

	return mergeBase[0].Hash, nil
}

// detectDefaultBranch attempts to determine the default branch name for the repository
func detectDefaultBranch(r *git.Repository) string {
	// Common default branch names to check in priority order
	possibleDefaults := []string{"main", "master", "trunk", "development"}

	// First try to get the HEAD reference of the origin remote
	remotes, err := r.Remotes()
	if err == nil && len(remotes) > 0 {
		// Try to find the origin remote
		var originRemote *git.Remote
		for _, remote := range remotes {
			if remote.Config().Name == "origin" {
				originRemote = remote
				break
			}
		}

		// If we found origin, try to get its HEAD reference
		if originRemote != nil {
			// List references from the remote
			refs, err := r.References()
			if err == nil {
				// Look for a HEAD symbolic reference
				refs.ForEach(func(ref *plumbing.Reference) error {
					if ref.Name().String() == "refs/remotes/origin/HEAD" {
						// Extract the branch name from the target
						target := ref.Target().String()
						if strings.HasPrefix(target, "refs/remotes/origin/") {
							branch := strings.TrimPrefix(target, "refs/remotes/origin/")
							possibleDefaults = []string{branch} // Override with the actual default
							return fmt.Errorf("stop")
						}
					}
					return nil
				})
			}
		}
	}

	// Check each possible default branch to see if it exists
	for _, branchName := range possibleDefaults {
		branchRef, err := r.Reference(plumbing.NewBranchReferenceName(branchName), true)
		if err == nil && branchRef != nil {
			return branchName
		}
	}

	// If no default branch was found, return "main" as fallback
	return "main"
}
