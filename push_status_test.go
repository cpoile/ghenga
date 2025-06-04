package main

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setGitConfig sets a git config value in the repository
func setGitConfig(t *testing.T, repoPath, key, value string) {
	t.Helper()
	cmd := exec.Command("git", "config", key, value)
	cmd.Dir = repoPath
	require.NoError(t, cmd.Run(), "Failed to set git config %s=%s", key, value)
}

// unsetGitConfig removes a git config value from the repository
func unsetGitConfig(t *testing.T, repoPath, key string) {
	t.Helper()
	cmd := exec.Command("git", "config", "--unset", key)
	cmd.Dir = repoPath
	cmd.Run() // Don't require success since config might not exist
}

// setupTestRepoWithRemotes creates a test repository with multiple remotes configured
func setupTestRepoWithRemotes(t *testing.T) (string, *git.Repository, map[string]string, func()) {
	t.Helper()

	// Create local repo
	localRepoPath, localRepo := setupTestRepo(t)

	// Create multiple remote repos
	originPath, _ := setupRemoteRepo(t)
	forkPath, _ := setupRemoteRepo(t)
	upstreamPath, _ := setupRemoteRepo(t)

	// Add remotes to the local repository
	_, err := localRepo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{originPath},
	})
	require.NoError(t, err)

	_, err = localRepo.CreateRemote(&config.RemoteConfig{
		Name: "fork",
		URLs: []string{forkPath},
	})
	require.NoError(t, err)

	_, err = localRepo.CreateRemote(&config.RemoteConfig{
		Name: "upstream",
		URLs: []string{upstreamPath},
	})
	require.NoError(t, err)

	remotes := map[string]string{
		"origin":   originPath,
		"fork":     forkPath,
		"upstream": upstreamPath,
	}

	cleanup := func() {
		os.RemoveAll(localRepoPath)
		os.RemoveAll(originPath)
		os.RemoveAll(forkPath)
		os.RemoveAll(upstreamPath)
	}

	return localRepoPath, localRepo, remotes, cleanup
}

// createBranchWithCommits creates a branch with specified number of commits and pushes to remote
func createBranchWithCommits(t *testing.T, repo *git.Repository, remoteName, branchName string, commitCount int) plumbing.Hash {
	t.Helper()

	// Create branch
	createTestBranch(t, repo, branchName, commitCount)

	// Push branch to remote
	wt, err := repo.Worktree()
	require.NoError(t, err)

	repoPath := wt.Filesystem.Root()
	cmd := exec.Command("git", "push", remoteName, branchName)
	cmd.Dir = repoPath
	require.NoError(t, cmd.Run(), "Failed to push branch %s to %s", branchName, remoteName)

	// Get the branch hash
	branchRef, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err)

	return branchRef.Hash()
}

// addCommitsToBranch adds more commits to an existing branch
func addCommitsToBranch(t *testing.T, repo *git.Repository, branchName string, commitCount int) {
	t.Helper()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Checkout the branch
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branchName),
	})
	require.NoError(t, err)

	// Add commits with unique filenames using timestamp to avoid conflicts
	repoPath := wt.Filesystem.Root()
	timestamp := time.Now().UnixNano()
	for i := range commitCount {
		filename := fmt.Sprintf("extra-%s-%d-%d.txt", branchName, i, timestamp)
		content := fmt.Sprintf("Extra content for %s commit %d at %d", branchName, i, timestamp)
		message := fmt.Sprintf("Extra commit %d for %s", i, branchName)
		addSingleCommit(t, repoPath, wt, filename, content, message)
	}
}

// TestGetPushRemoteForBranch tests the push remote detection logic
func TestGetPushRemoteForBranch(t *testing.T) {
	tests := []struct {
		name         string
		branchName   string
		setupConfig  func(t *testing.T, repoPath string)
		expectedName string
		expectError  bool
	}{
		{
			name:       "branch-specific pushRemote takes precedence",
			branchName: "feature",
			setupConfig: func(t *testing.T, repoPath string) {
				setGitConfig(t, repoPath, "branch.feature.pushRemote", "fork")
				setGitConfig(t, repoPath, "remote.pushDefault", "upstream")
				setGitConfig(t, repoPath, "branch.feature.remote", "origin")
			},
			expectedName: "fork",
		},
		{
			name:       "global pushDefault when no branch-specific pushRemote",
			branchName: "feature",
			setupConfig: func(t *testing.T, repoPath string) {
				setGitConfig(t, repoPath, "remote.pushDefault", "upstream")
				setGitConfig(t, repoPath, "branch.feature.remote", "origin")
			},
			expectedName: "upstream",
		},
		{
			name:       "upstream remote when no pushRemote or pushDefault",
			branchName: "feature",
			setupConfig: func(t *testing.T, repoPath string) {
				setGitConfig(t, repoPath, "branch.feature.remote", "origin")
			},
			expectedName: "origin",
		},
		{
			name:       "fallback to origin when no config",
			branchName: "feature",
			setupConfig: func(t *testing.T, repoPath string) {
				// No config setup
			},
			expectedName: "origin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoPath, repo, _, cleanup := setupTestRepoWithRemotes(t)
			defer cleanup()

			// Create the test branch
			createTestBranch(t, repo, tt.branchName, 1)

			// Setup git configuration
			tt.setupConfig(t, repoPath)

			// Test the function
			remoteName, err := getPushRemoteForBranch(repo, tt.branchName)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedName, remoteName)
			}
		})
	}
}

// TestGetBranchPushStatus_AutoDetection tests auto-detection when remoteName is empty
func TestGetBranchPushStatus_AutoDetection(t *testing.T) {
	tests := []struct {
		name           string
		branchName     string
		setupConfig    func(t *testing.T, repoPath string)
		setupRepo      func(t *testing.T, repo *git.Repository)
		expectedStatus PushStatus
		expectError    bool
	}{
		{
			name:       "auto-detect with branch pushRemote",
			branchName: "feature",
			setupConfig: func(t *testing.T, repoPath string) {
				setGitConfig(t, repoPath, "branch.feature.pushRemote", "fork")
			},
			setupRepo: func(t *testing.T, repo *git.Repository) {
				createBranchWithCommits(t, repo, "fork", "feature", 1)
			},
			expectedStatus: UpToDate,
		},
		{
			name:       "auto-detect with global pushDefault",
			branchName: "feature",
			setupConfig: func(t *testing.T, repoPath string) {
				setGitConfig(t, repoPath, "remote.pushDefault", "upstream")
			},
			setupRepo: func(t *testing.T, repo *git.Repository) {
				createBranchWithCommits(t, repo, "upstream", "feature", 1)
			},
			expectedStatus: UpToDate,
		},
		{
			name:       "auto-detect with upstream remote",
			branchName: "feature",
			setupConfig: func(t *testing.T, repoPath string) {
				setGitConfig(t, repoPath, "branch.feature.remote", "origin")
			},
			setupRepo: func(t *testing.T, repo *git.Repository) {
				createBranchWithCommits(t, repo, "origin", "feature", 1)
			},
			expectedStatus: UpToDate,
		},
		{
			name:       "auto-detect fallback to origin",
			branchName: "feature",
			setupConfig: func(t *testing.T, repoPath string) {
				// No config
			},
			setupRepo: func(t *testing.T, repo *git.Repository) {
				createBranchWithCommits(t, repo, "origin", "feature", 1)
			},
			expectedStatus: UpToDate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoPath, repo, _, cleanup := setupTestRepoWithRemotes(t)
			defer cleanup()

			// Setup configuration and repository state
			tt.setupConfig(t, repoPath)
			tt.setupRepo(t, repo)

			// Test with empty remoteName (should auto-detect)
			status, _, _, err := GetBranchPushStatus(repo, "", tt.branchName)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedStatus, status)
			}
		})
	}
}

// TestGetBranchPushStatus_ExplicitRemote tests backward compatibility with explicit remote names
func TestGetBranchPushStatus_ExplicitRemote(t *testing.T) {
	repoPath, repo, _, cleanup := setupTestRepoWithRemotes(t)
	defer cleanup()

	branchName := "feature"

	// Setup conflicting configuration (pushRemote = fork, but we'll explicitly use origin)
	setGitConfig(t, repoPath, "branch.feature.pushRemote", "fork")

	// Create branch and push to origin first
	createBranchWithCommits(t, repo, "origin", branchName, 1)

	// Add more commits and push to fork
	addCommitsToBranch(t, repo, branchName, 1)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	cmd := exec.Command("git", "push", "fork", branchName)
	cmd.Dir = wt.Filesystem.Root()
	require.NoError(t, cmd.Run(), "Failed to push branch %s to fork", branchName)

	// Now origin has 1 commit, fork has 2 commits, local has 2 commits

	// Test with explicit remote name - should use origin regardless of pushRemote config
	status, _, _, err := GetBranchPushStatus(repo, "origin", branchName)
	assert.NoError(t, err)
	assert.Equal(t, LocalAhead, status) // Local is ahead of origin

	// Test with different explicit remote
	status, _, _, err = GetBranchPushStatus(repo, "fork", branchName)
	assert.NoError(t, err)
	assert.Equal(t, UpToDate, status) // Local is up to date with fork
}

// TestGetBranchPushStatus_PushStatusScenarios tests all push status types
func TestGetBranchPushStatus_PushStatusScenarios(t *testing.T) {
	tests := []struct {
		name           string
		setupScenario  func(t *testing.T, repo *git.Repository, branchName string)
		expectedStatus PushStatus
	}{
		{
			name: "UpToDate - local and remote are same",
			setupScenario: func(t *testing.T, repo *git.Repository, branchName string) {
				createBranchWithCommits(t, repo, "origin", branchName, 1)
			},
			expectedStatus: UpToDate,
		},
		{
			name: "LocalAhead - local has additional commits",
			setupScenario: func(t *testing.T, repo *git.Repository, branchName string) {
				createBranchWithCommits(t, repo, "origin", branchName, 1)
				addCommitsToBranch(t, repo, branchName, 1) // Add local commit
			},
			expectedStatus: LocalAhead,
		},
		{
			name: "RemoteAhead - remote has additional commits",
			setupScenario: func(t *testing.T, repo *git.Repository, branchName string) {
				// Create branch locally
				createTestBranch(t, repo, branchName, 1)

				// Push to remote
				wt, err := repo.Worktree()
				require.NoError(t, err)
				repoPath := wt.Filesystem.Root()
				cmd := exec.Command("git", "push", "origin", branchName)
				cmd.Dir = repoPath
				require.NoError(t, cmd.Run())

				// Add more commits to remote by pushing from another location
				// For simplicity, we'll simulate this by creating additional commits
				// and pushing them
				addCommitsToBranch(t, repo, branchName, 1)
				cmd = exec.Command("git", "push", "origin", branchName)
				cmd.Dir = repoPath
				require.NoError(t, cmd.Run())

				// Reset local branch to previous commit
				cmd = exec.Command("git", "reset", "--hard", "HEAD~1")
				cmd.Dir = repoPath
				require.NoError(t, cmd.Run())
			},
			expectedStatus: RemoteAhead,
		},
		{
			name: "NoRemote - remote branch doesn't exist",
			setupScenario: func(t *testing.T, repo *git.Repository, branchName string) {
				createTestBranch(t, repo, branchName, 1) // Only create locally
			},
			expectedStatus: NoRemote,
		},
		{
			name: "Diverged - both have unique commits",
			setupScenario: func(t *testing.T, repo *git.Repository, branchName string) {
				// Create base branch and push
				createBranchWithCommits(t, repo, "origin", branchName, 1)

				// Add local commit
				addCommitsToBranch(t, repo, branchName, 1)

				// Simulate remote having different commits by:
				// 1. Creating another commit locally
				// 2. Pushing it
				// 3. Resetting local to exclude this commit but keep the previous one
				addCommitsToBranch(t, repo, branchName, 1)

				wt, err := repo.Worktree()
				require.NoError(t, err)
				repoPath := wt.Filesystem.Root()

				// Push the divergent commit
				cmd := exec.Command("git", "push", "origin", branchName)
				cmd.Dir = repoPath
				require.NoError(t, cmd.Run())

				// Reset local to previous commit and add a different commit
				cmd = exec.Command("git", "reset", "--hard", "HEAD~1")
				cmd.Dir = repoPath
				require.NoError(t, cmd.Run())

				addCommitsToBranch(t, repo, branchName, 1) // Different commit
			},
			expectedStatus: Diverged,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, repo, _, cleanup := setupTestRepoWithRemotes(t)
			defer cleanup()

			branchName := "test-branch"

			// Setup the scenario
			tt.setupScenario(t, repo, branchName)

			// Test the status
			status, _, _, err := GetBranchPushStatus(repo, "origin", branchName)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedStatus, status)
		})
	}
}

// TestGetBranchPushStatus_EdgeCases tests error conditions and edge cases
func TestGetBranchPushStatus_EdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		setupScenario  func(t *testing.T, repo *git.Repository) string // returns branch name
		remoteName     string
		expectError    bool
		expectedStatus PushStatus
	}{
		{
			name: "nonexistent local branch",
			setupScenario: func(t *testing.T, repo *git.Repository) string {
				return "nonexistent-branch"
			},
			remoteName:     "origin",
			expectError:    true,
			expectedStatus: StatusError,
		},
		{
			name: "nonexistent remote",
			setupScenario: func(t *testing.T, repo *git.Repository) string {
				branchName := "test-branch"
				createTestBranch(t, repo, branchName, 1)
				return branchName
			},
			remoteName:     "nonexistent-remote",
			expectError:    false,
			expectedStatus: NoRemote,
		},
		{
			name: "auto-detect with invalid pushRemote config",
			setupScenario: func(t *testing.T, repo *git.Repository) string {
				branchName := "test-branch"
				createTestBranch(t, repo, branchName, 1)

				// Set invalid pushRemote
				wt, err := repo.Worktree()
				require.NoError(t, err)
				setGitConfig(t, wt.Filesystem.Root(), "branch.test-branch.pushRemote", "invalid-remote")

				return branchName
			},
			remoteName:     "", // Auto-detect
			expectError:    false,
			expectedStatus: NoRemote,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, repo, _, cleanup := setupTestRepoWithRemotes(t)
			defer cleanup()

			branchName := tt.setupScenario(t, repo)

			status, _, _, err := GetBranchPushStatus(repo, tt.remoteName, branchName)

			if tt.expectError {
				assert.Error(t, err)
				assert.Equal(t, tt.expectedStatus, status)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedStatus, status)
			}
		})
	}
}
