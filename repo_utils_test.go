package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveMainRepoPath_MainRepo(t *testing.T) {
	// Setup a test repository
	repoPath, _ := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// resolveMainRepoPath should return the same path when already in main repo
	result, err := resolveMainRepoPath(repoPath)
	require.NoError(t, err)

	// Normalize both paths to handle macOS symlink (/var -> /private/var)
	expectedPath, _ := filepath.EvalSymlinks(repoPath)
	resultPath, _ := filepath.EvalSymlinks(result)
	require.Equal(t, expectedPath, resultPath)
}

func TestResolveMainRepoPath_Worktree(t *testing.T) {
	// Setup a test repository
	mainRepoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(mainRepoPath)

	// Create a branch for the worktree (use unique name to avoid conflicts)
	createTestBranch(t, repo, "resolve-wt-branch", 1)

	// Checkout main so we can create worktree from the branch
	err := CheckoutBranch(mainRepoPath, "main")
	require.NoError(t, err)

	// Create a worktree directory with unique name
	worktreePath := filepath.Join(filepath.Dir(mainRepoPath), "resolve-main-repo-wt")
	defer func() {
		// Clean up worktree properly
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		cmd.Dir = mainRepoPath
		cmd.Run() // Ignore errors, just try to clean up
		os.RemoveAll(worktreePath)
	}()

	// Create the worktree using git command
	cmd := exec.Command("git", "worktree", "add", worktreePath, "resolve-wt-branch")
	cmd.Dir = mainRepoPath
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree: %s", string(output))

	// Verify worktree was created
	_, err = os.Stat(worktreePath)
	require.NoError(t, err, "Worktree directory should exist")

	// resolveMainRepoPath from worktree should return main repo path
	result, err := resolveMainRepoPath(worktreePath)
	require.NoError(t, err)

	// Normalize both paths to handle macOS symlink (/var -> /private/var)
	expectedPath, _ := filepath.EvalSymlinks(mainRepoPath)
	resultPath, _ := filepath.EvalSymlinks(result)
	require.Equal(t, expectedPath, resultPath, "resolveMainRepoPath should return main repo path when called from worktree")
}

func TestGetCurrentRepository_FromWorktree(t *testing.T) {
	// Setup a test repository
	mainRepoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(mainRepoPath)

	// Create a branch for the worktree (use unique name to avoid conflicts)
	createTestBranch(t, repo, "getcurrent-wt-branch", 1)

	// Checkout main so we can create worktree from the branch
	err := CheckoutBranch(mainRepoPath, "main")
	require.NoError(t, err)

	// Create a worktree directory with unique name
	worktreePath := filepath.Join(filepath.Dir(mainRepoPath), "get-current-repo-wt")
	defer func() {
		// Clean up worktree properly
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		cmd.Dir = mainRepoPath
		cmd.Run() // Ignore errors, just try to clean up
		os.RemoveAll(worktreePath)
	}()

	// Create the worktree using git command
	cmd := exec.Command("git", "worktree", "add", worktreePath, "getcurrent-wt-branch")
	cmd.Dir = mainRepoPath
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree: %s", string(output))

	// Change to worktree directory
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(oldWd)
	err = os.Chdir(worktreePath)
	require.NoError(t, err)

	// getCurrentRepository should return main repo path even when in worktree
	result, err := getCurrentRepository()
	require.NoError(t, err)

	// Normalize both paths to handle macOS symlink (/var -> /private/var)
	expectedPath, _ := filepath.EvalSymlinks(mainRepoPath)
	resultPath, _ := filepath.EvalSymlinks(result)
	require.Equal(t, expectedPath, resultPath, "getCurrentRepository should return main repo path when called from worktree")
}

func TestGetHead_FromWorktree(t *testing.T) {
	// Setup main repo
	mainRepoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(mainRepoPath)

	// Create a branch for the worktree
	createTestBranch(t, repo, "open-repo-wt-branch", 1)

	// Checkout main
	err := CheckoutBranch(mainRepoPath, "main")
	require.NoError(t, err)

	// Create worktree
	worktreePath := filepath.Join(filepath.Dir(mainRepoPath), "open-repo-wt")
	defer func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		cmd.Dir = mainRepoPath
		cmd.Run()
		os.RemoveAll(worktreePath)
	}()

	cmd := exec.Command("git", "worktree", "add", worktreePath, "open-repo-wt-branch")
	cmd.Dir = mainRepoPath
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree: %s", string(output))

	// Change to worktree
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(worktreePath)

	// Open repo and get HEAD - this should work from worktree
	r, err := openGitRepo()
	require.NoError(t, err)

	headRef, err := getHead(r)
	require.NoError(t, err, "Should be able to get HEAD from worktree")
	require.Equal(t, "open-repo-wt-branch", headRef.Name().Short())
}
