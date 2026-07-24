package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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

	// Resolve worktree path to main repo path
	return resolveMainRepoPath(wt.Filesystem.Root())
}

// resolveMainRepoPath resolves a worktree path to the main repository path.
// If already in the main repo, returns the path unchanged.
func resolveMainRepoPath(repoRoot string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get git common dir: %w", err)
	}

	gitCommonDir := strings.TrimSpace(string(output))

	// If .git is relative, we're in the main repo
	if gitCommonDir == ".git" || !filepath.IsAbs(gitCommonDir) {
		return repoRoot, nil
	}

	// gitCommonDir is absolute (e.g., /path/to/main-repo/.git)
	// The main repo is the parent of the .git directory
	return filepath.Dir(gitCommonDir), nil
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

// getHead returns the HEAD reference, using git CLI as fallback for worktree support.
// go-git's PlainOpenWithOptions doesn't properly handle git worktrees - when opening
// a repo from a worktree directory, r.Head() fails because go-git doesn't correctly
// follow the worktree's HEAD reference.
func getHead(r *git.Repository) (*plumbing.Reference, error) {
	// Try go-git first
	headRef, err := r.Head()
	if err == nil {
		return headRef, nil
	}

	// Fallback to git CLI for worktree support
	cmd := exec.Command("git", "symbolic-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	refName := plumbing.ReferenceName(strings.TrimSpace(string(output)))

	// Get the hash for this reference
	hashCmd := exec.Command("git", "rev-parse", "HEAD")
	hashOutput, err := hashCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD hash: %w", err)
	}

	hash := plumbing.NewHash(strings.TrimSpace(string(hashOutput)))
	return plumbing.NewHashReference(refName, hash), nil
}

// getReference returns a reference by name using git CLI for worktree support.
// go-git's r.Reference() doesn't properly handle git worktrees.
func getReference(_ *git.Repository, refName plumbing.ReferenceName) (*plumbing.Reference, error) {
	cmd := exec.Command("git", "rev-parse", refName.String())
	output, err := cmd.Output()
	if err != nil {
		return nil, plumbing.ErrReferenceNotFound
	}

	hash := plumbing.NewHash(strings.TrimSpace(string(output)))
	return plumbing.NewHashReference(refName, hash), nil
}

// resolveRevision resolves a revision to a hash using git CLI for worktree support.
// go-git's r.ResolveRevision() doesn't properly handle git worktrees.
func resolveRevision(_ *git.Repository, rev plumbing.Revision) (*plumbing.Hash, error) {
	cmd := exec.Command("git", "rev-parse", string(rev))
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reference not found: %s", rev)
	}

	h := plumbing.NewHash(strings.TrimSpace(string(output)))
	return &h, nil
}

// findMergeBase finds the merge base (common ancestor) between two commits using git CLI.
// Uses CLI instead of go-git because go-git can't access objects from worktrees.
func findMergeBase(_ *git.Repository, commit1, commit2 plumbing.Hash) (plumbing.Hash, error) {
	// If commits are the same, return immediately
	if commit1 == commit2 {
		return commit1, nil
	}

	cmd := exec.Command("git", "merge-base", commit1.String(), commit2.String())
	output, err := cmd.Output()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("no common ancestor found")
	}

	return plumbing.NewHash(strings.TrimSpace(string(output))), nil
}

// CommitInfo holds basic commit information retrieved from git CLI.
type CommitInfo struct {
	Hash    plumbing.Hash
	Message string
}

// getLogCommits retrieves commits using git CLI, which works in worktrees.
// go-git's r.Log() doesn't work in worktrees because it can't access the object store.
// maxCount limits the number of commits returned (0 means no limit).
func getLogCommits(fromHash plumbing.Hash, maxCount int) ([]CommitInfo, error) {
	args := []string{"log", "--format=%H %s", fromHash.String()}
	if maxCount > 0 {
		args = []string{"log", fmt.Sprintf("-n%d", maxCount), "--format=%H %s", fromHash.String()}
	}

	cmd := exec.Command("git", args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	commits := make([]CommitInfo, 0, len(lines))

	for _, line := range lines {
		if line == "" {
			continue
		}
		// Format is "hash message", split at first space
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 1 {
			continue
		}
		hash := plumbing.NewHash(parts[0])
		message := ""
		if len(parts) > 1 {
			message = parts[1]
		}
		commits = append(commits, CommitInfo{
			Hash:    hash,
			Message: message,
		})
	}

	return commits, nil
}

// getRangeCommits returns the commits reachable from toHash but not from fromHash
// (git's "fromHash..toHash" range), newest first. This is topology-correct for
// branches that contain merge commits — unlike walking a linear log and stopping
// at the first sighting of fromHash, which truncates early when a merge commit
// pulls the lower branch's tip in as a second parent.
func getRangeCommits(fromHash, toHash plumbing.Hash, maxCount int) ([]CommitInfo, error) {
	rangeArg := fromHash.String() + ".." + toHash.String()
	args := []string{"log", "--format=%H %s", rangeArg}
	if maxCount > 0 {
		args = []string{"log", fmt.Sprintf("-n%d", maxCount), "--format=%H %s", rangeArg}
	}

	cmd := exec.Command("git", args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log range failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	commits := make([]CommitInfo, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 1 {
			continue
		}
		message := ""
		if len(parts) > 1 {
			message = parts[1]
		}
		commits = append(commits, CommitInfo{Hash: plumbing.NewHash(parts[0]), Message: message})
	}
	return commits, nil
}

// detectDefaultBranch attempts to determine the default branch name for the repository.
// Uses CLI instead of go-git because go-git doesn't work properly in worktrees.
func detectDefaultBranch(r *git.Repository) string {
	// Common default branch names to check in priority order
	possibleDefaults := []string{"main", "master", "trunk", "development"}

	// First check if origin remote exists
	remotesCmd := exec.Command("git", "remote")
	remotesOutput, err := remotesCmd.Output()
	if err == nil {
		remotes := strings.Split(strings.TrimSpace(string(remotesOutput)), "\n")
		hasOrigin := false
		for _, remote := range remotes {
			if remote == "origin" {
				hasOrigin = true
				break
			}
		}

		// If we found origin, try to get its HEAD reference
		if hasOrigin {
			// Try to resolve refs/remotes/origin/HEAD
			cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
			output, err := cmd.Output()
			if err == nil {
				target := strings.TrimSpace(string(output))
				if strings.HasPrefix(target, "refs/remotes/origin/") {
					branch := strings.TrimPrefix(target, "refs/remotes/origin/")
					possibleDefaults = []string{branch} // Override with the actual default
				}
			}
		}
	}

	// Check each possible default branch to see if it exists
	for _, branchName := range possibleDefaults {
		branchRef, err := getReference(r, plumbing.NewBranchReferenceName(branchName))
		if err == nil && branchRef != nil {
			return branchName
		}
	}

	// If no default branch was found, return "main" as fallback
	return "main"
}

// updateLocalBranchFromRemote fetches remote and fast-forwards the local base branch to match it.
// When the base branch is not the one currently checked out, its ref is advanced directly via
// update-ref so the working tree is never switched off the current branch.
func updateLocalBranchFromRemote(repoPath string, r *git.Repository, remoteName, localBranch, currentBranchToPreserve string) error {
	fmt.Printf("    Fetching remote '%s'...\n", remoteName)
	if output, err := runGitCommandWithRetry(repoPath, "fetch", remoteName); err != nil {
		return fmt.Errorf("git fetch failed: %w\nOutput: %s", err, string(output))
	}

	// Ensure the branch we want to update exists locally
	localRefName := plumbing.NewBranchReferenceName(localBranch)
	if _, err := getReference(r, localRefName); err != nil {
		return fmt.Errorf("local base branch '%s' not found: %w", localBranch, err)
	}

	// Ensure the remote tracking branch exists
	remoteRefName := plumbing.NewRemoteReferenceName(remoteName, localBranch)
	remoteRef, err := getReference(r, remoteRefName)
	if err != nil {
		return fmt.Errorf("remote tracking branch '%s' not found: %w", remoteRefName, err)
	}

	// If the base branch is the one currently checked out here, reset it in place so the working
	// tree follows the new commit.
	if currentBranchToPreserve == localBranch {
		fmt.Printf("    Resetting '%s' to '%s' (%s)...\n", localBranch, remoteRefName, remoteRef.Hash().String()[:7])
		if output, err := runGitCommandWithRetry(repoPath, "reset", "--hard", remoteRefName.String()); err != nil {
			return fmt.Errorf("git reset --hard failed: %w\nOutput: %s", err, string(output))
		}
		return nil
	}

	// Otherwise move the base branch ref directly to the remote commit without checking it out.
	// Checking out the base would switch the working tree off the current branch, and any build
	// artifacts hidden by the current branch's .gitignore (but not the base branch's) would then
	// surface as untracked files, leaving the tree "not clean" and stranding us on the base branch.
	// update-ref touches only the ref, never the working tree, and (unlike 'git branch -f') works
	// even when the base is checked out in another worktree.
	fmt.Printf("    Updating base branch '%s' to '%s' (%s)...\n", localBranch, remoteRefName, remoteRef.Hash().String()[:7])
	if output, err := runGitCommandWithRetry(repoPath, "update-ref", localRefName.String(), remoteRef.Hash().String()); err != nil {
		return fmt.Errorf("failed to update base branch '%s' to '%s': %w\nOutput: %s", localBranch, remoteRef.Hash().String()[:7], err, string(output))
	}

	return nil
}

// getCurrentBranchName returns the name of the current branch
func getCurrentBranchName(r *git.Repository) (string, error) {
	headRef, err := getHead(r)
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
		return fmt.Errorf("working directory '%s' is not clean. Please commit or stash your changes", wt.Filesystem.Root())
	}

	return nil
}

// runGitCommandWithRetry executes a git command with retry logic for lock file errors
func runGitCommandWithRetry(repoPath string, args ...string) ([]byte, error) {
	return runGitCommandWithRetryEnv(repoPath, nil, args...)
}

// runGitCommandWithRetryEnv is runGitCommandWithRetry with extra environment
// variables appended to the inherited environment. Used to suppress the merge
// commit editor (GIT_EDITOR=true) on 'git merge --continue', which otherwise
// launches an interactive editor and hangs because ghenga captures git's output
// rather than handing it a terminal.
func runGitCommandWithRetryEnv(repoPath string, extraEnv []string, args ...string) ([]byte, error) {
	maxRetries := 10
	baseSleepMs := 100

	for attempt := 0; attempt <= maxRetries; attempt++ {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		if len(extraEnv) > 0 {
			cmd.Env = append(os.Environ(), extraEnv...)
		}
		output, err := cmd.CombinedOutput()

		if err == nil {
			return output, nil // Success
		}

		// Check if this is a lock file error and we haven't exhausted retries
		if attempt < maxRetries && isGitLockFileError(string(output)) {
			sleepMs := baseSleepMs * (1 << attempt) // Exponential backoff
			if sleepMs > 2000 {
				sleepMs = 2000 // Cap at 2 seconds
			}

			fmt.Printf("    Git lock file detected, retrying in %dms (attempt %d/%d)...\n",
				sleepMs, attempt+1, maxRetries+1)
			time.Sleep(time.Duration(sleepMs) * time.Millisecond)
			continue
		}

		// Either not a lock file error, or we've exhausted retries
		if isGitLockFileError(string(output)) {
			return output, fmt.Errorf("git lock file persisted after %d retries: %w", maxRetries+1, err)
		}

		return output, err // Return original error
	}

	// Should never reach here, but just in case
	return nil, fmt.Errorf("unexpected error in git command retry logic")
}

// isGitLockFileError checks if the error output indicates a git lock file issue
func isGitLockFileError(output string) bool {
	return strings.Contains(output, "index.lock") &&
		(strings.Contains(output, "File exists") || strings.Contains(output, "Another git process"))
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
