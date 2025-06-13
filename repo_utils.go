package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// getCurrentRepository returns the path of the current git repository
func getCurrentRepository() (string, error) {
	r, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	wt, err := r.Worktree()
	if err != nil {
		return "", fmt.Errorf("failed to get worktree: %w", err)
	}

	return wt.Filesystem.Root(), nil
}

// findTowerByName finds a tower by name in the given repository configuration
func findTowerByName(repo *RepoInfo, towerName string) *Tower {
	for _, tower := range repo.Towers {
		if tower.Name == towerName {
			return tower
		}
	}
	return nil
}

// findOrCreateTower finds a tower by name or creates it if it doesn't exist
func findOrCreateTower(repo *RepoInfo, towerName string) *Tower {
	tower := findTowerByName(repo, towerName)
	if tower == nil {
		tower = &Tower{
			Name:     towerName,
			Branches: []Branch{},
		}
		repo.Towers = append(repo.Towers, tower)
	}
	return tower
}

// getCurrentTower retrieves the current tower based on the repository's current setting.
func getCurrentTower(repo *RepoInfo) (*Tower, error) {
	if repo.Current == "" {
		return nil, fmt.Errorf("no current tower set, use 'ghenga current <tower-name>' to set one")
	}
	currentTower := findTowerByName(repo, repo.Current)
	if currentTower == nil {
		// This case should ideally not happen if repo.Current is set correctly,
		// but good to handle defensively.
		return nil, fmt.Errorf("current tower '%s' referenced but not found", repo.Current)
	}
	return currentTower, nil
}

// findRepoByPath finds a repository configuration by path
func findRepoByPath(config *Config, repoPath string) *RepoInfo {
	// Try to find the repo by exact path
	for _, repo := range config.Repos {
		if repo.Path == repoPath {
			return repo
		}
	}

	// Try to find the repo by normalized path (resolving symlinks)
	normalizedPath, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		return nil
	}

	for _, repo := range config.Repos {
		normalizedRepoPath, err := filepath.EvalSymlinks(repo.Path)
		if err != nil {
			continue
		}
		if normalizedRepoPath == normalizedPath {
			return repo
		}
	}

	return nil
}

// findOrCreateRepo finds a repository configuration by path or creates it if it doesn't exist
func findOrCreateRepo(config *Config, repoPath string) *RepoInfo {
	repo := findRepoByPath(config, repoPath)
	if repo == nil {
		repo = &RepoInfo{
			Path:   repoPath,
			Towers: []*Tower{},
		}
		config.Repos = append(config.Repos, repo)
	}
	return repo
}

// containsBranch checks if a tower contains a branch with the given name
func containsBranch(tower *Tower, branchName string) bool {
	for _, branch := range tower.Branches {
		if branch.Name == branchName {
			return true
		}
	}
	return false
}

// loadRepoConfig gets the current repository path, loads the configuration,
// and finds the repository entry in the config. It returns an error if the
// repository is not found in the config.
func loadRepoConfig() (*Config, *RepoInfo, string, error) {
	repoPath, err := getCurrentRepository()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to get current repository: %w", err)
	}

	config, err := LoadConfig()
	if err != nil {
		return nil, nil, repoPath, fmt.Errorf("failed to load configuration: %w", err)
	}

	repo := findRepoByPath(config, repoPath)
	if repo == nil {
		return config, nil, repoPath, fmt.Errorf("repository at '%s' not found in configuration. Run 'ghenga init'?", repoPath)
	}

	return config, repo, repoPath, nil
}

// loadOrCreateRepoConfig behaves like loadRepoConfig but creates the repository
// entry if it doesn't exist.
func loadOrCreateRepoConfig() (*Config, *RepoInfo, string, error) {
	repoPath, err := getCurrentRepository()
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to get current repository: %w", err)
	}

	config, err := LoadConfig()
	if err != nil {
		return nil, nil, repoPath, fmt.Errorf("failed to load configuration: %w", err)
	}

	repo := findOrCreateRepo(config, repoPath)

	return config, repo, repoPath, nil
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

// updateLocalBranchFromRemote fetches remote and updates local branch
func updateLocalBranchFromRemote(repoPath string, r *git.Repository, remoteName, localBranch, currentBranchToPreserve string) error {
	fmt.Printf("    Fetching remote '%s'...\n", remoteName)
	fetchCmd := exec.Command("git", "fetch", remoteName)
	fetchCmd.Dir = repoPath
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch failed: %w\nOutput: %s", err, string(output))
	}

	// Ensure the branch we want to update exists locally
	localRefName := plumbing.NewBranchReferenceName(localBranch)
	_, err := r.Reference(localRefName, true)
	if err != nil {
		return fmt.Errorf("local base branch '%s' not found: %w", localBranch, err)
	}

	// Ensure the remote tracking branch exists
	remoteRefName := plumbing.NewRemoteReferenceName(remoteName, localBranch)
	remoteRef, err := r.Reference(remoteRefName, true)
	if err != nil {
		return fmt.Errorf("remote tracking branch '%s' not found: %w", remoteRefName, err)
	}

	// Checkout the local branch if we are not already on it
	currentlyOnLocalBranch := currentBranchToPreserve == localBranch
	if !currentlyOnLocalBranch {
		fmt.Printf("    Checking out local branch '%s'...\n", localBranch)
		if err := CheckoutBranch(repoPath, localBranch); err != nil {
			return fmt.Errorf("failed to checkout local branch '%s': %w", localBranch, err)
		}
	}

	// Reset the local branch to the remote's state
	fmt.Printf("    Resetting '%s' to '%s' (%s)...\n", localBranch, remoteRefName, remoteRef.Hash().String()[:7])
	resetCmd := exec.Command("git", "reset", "--hard", remoteRefName.String())
	resetCmd.Dir = repoPath
	if output, err := resetCmd.CombinedOutput(); err != nil {
		// Attempt to checkout original branch even if reset fails
		return fmt.Errorf("git reset --hard failed: %w\nOutput: %s", err, string(output))
	}

	// If we checked out the local branch temporarily, check back out to original branch now
	if !currentlyOnLocalBranch && currentBranchToPreserve != "" {
		fmt.Printf("    Checking out original branch '%s'...\n", currentBranchToPreserve)
		if err := CheckoutBranch(repoPath, currentBranchToPreserve); err != nil {
			// This is problematic, we updated local but couldn't switch back
			return fmt.Errorf("CRITICAL: failed to checkout original branch '%s' after updating base branch: %w", currentBranchToPreserve, err)
		}
	}

	return nil
}

// getCurrentBranchName returns the name of the current branch
func getCurrentBranchName(r *git.Repository) (string, error) {
	headRef, err := r.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD: %w", err)
	}
	if !headRef.Name().IsBranch() {
		return "", fmt.Errorf("HEAD is detached")
	}
	return headRef.Name().Short(), nil
}

// isWorkingDirectoryClean checks if the working directory has no uncommitted changes
// Uses git CLI to respect .gitignore rules properly
func isWorkingDirectoryClean(r *git.Repository) error {
	wt, err := r.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	// Use git status command which properly respects .gitignore
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = wt.Filesystem.Root()
	output, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get git status: %w", err)
	}

	// If there's any output, the working directory is not clean
	if len(strings.TrimSpace(string(output))) > 0 {
		return fmt.Errorf("working directory is not clean. Please commit or stash your changes")
	}

	return nil
}

// Helper function: hasConflicts checks git status for unmerged paths
func hasConflicts(repoPath string) (bool, error) {
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repoPath
	output, err := statusCmd.Output()
	if err != nil {
		// Check if the error is because we are mid-rebase (often non-zero exit code)
		// but still want to parse the output for 'U' markers.
		// If no output AND error, then it's likely a real error.
		if len(output) == 0 {
			return false, fmt.Errorf("failed to get git status: %w", err)
		}
		// Otherwise, proceed to parse the output even if exit code was non-zero
	}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		// Look for standard conflict markers (Unmerged) or Added/Deleted by both
		if len(line) >= 2 && (line[0] == 'U' || (line[0] == 'A' && line[1] == 'A') || (line[0] == 'D' && line[1] == 'D') || (line[0] == 'R' && line[1] == 'U') || (line[0] == 'U' && line[1] == 'R')) {
			return true, nil
		}
	}
	return false, nil
}

// validateTowerBranchStatus checks all branches in a tower for divergence from remote
// and returns an error if any branch is diverged or behind the remote.
// This is used by commands that require the tower to be in a "clean" state.
func validateTowerBranchStatus(r *git.Repository, remoteName string, tower *Tower) error {
	fmt.Println("Checking tower branches for divergence...")
	hasDiverged := false
	
	for _, branch := range tower.Branches {
		status, _, _, err := GetBranchPushStatus(r, remoteName, branch.Name)
		if err != nil {
			// Handle cases like local branch deleted but still in config
			fmt.Printf("  Warning: Could not check status for branch '%s': %v\n", branch.Name, err)
			continue
		}

		if status == Diverged {
			fmt.Printf("  Error: Branch '%s' has diverged from the remote '%s'.\n", branch.Name, remoteName)
			hasDiverged = true
		} else if status == RemoteAhead {
			// Also consider RemoteAhead as needing attention before operations
			fmt.Printf("  Error: Remote branch '%s/%s' is ahead of local branch '%s'.\n", remoteName, branch.Name, branch.Name)
			hasDiverged = true // Treat as needing rebase/sync
		}
	}
	
	if hasDiverged {
		return fmt.Errorf("one or more tower branches have diverged or are behind the remote. Please run 'ghenga rebase' or 'ghenga sync' (respectively) to bring them up to date")
	}
	
	fmt.Println("  All tower branches are up-to-date or ahead of remote.")
	return nil
}
