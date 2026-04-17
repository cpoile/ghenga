package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckoutBranch_NormalCheckout(t *testing.T) {
	repoPath, _ := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Change to repo directory
	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(originalDir)

	err = os.Chdir(repoPath)
	require.NoError(t, err)

	// Create a test branch
	createBranchForCheckoutTest(t, repoPath, "test-branch")

	// Test normal checkout
	err = CheckoutBranch(repoPath, "test-branch")
	require.NoError(t, err)

	// Verify we're on the correct branch
	cmd := exec.Command("git", "branch", "--show-current")
	output, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "test-branch", strings.TrimSpace(string(output)))
}

func TestCheckoutBranch_NonExistentBranch(t *testing.T) {
	repoPath, _ := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(originalDir)

	err = os.Chdir(repoPath)
	require.NoError(t, err)

	// Test checkout of non-existent branch
	err = CheckoutBranch(repoPath, "nonexistent-branch")
	require.Error(t, err)
	require.Contains(t, err.Error(), "nonexistent-branch")

	// Verify we're still on original branch
	cmd := exec.Command("git", "branch", "--show-current")
	output, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "main", strings.TrimSpace(string(output)))
}

func TestCheckoutBranch_WorktreeConflict(t *testing.T) {
	repoPath, _ := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(originalDir)

	err = os.Chdir(repoPath)
	require.NoError(t, err)

	// Create a test branch
	createBranchForCheckoutTest(t, repoPath, "worktree-branch")

	// Create a worktree for the branch
	worktreeDir := filepath.Join(repoPath, "..", "test-worktree")
	cmd := exec.Command("git", "worktree", "add", worktreeDir, "worktree-branch")
	err = cmd.Run()
	require.NoError(t, err)
	defer os.RemoveAll(worktreeDir)

	// Now try to checkout the branch that's already checked out in the worktree
	// This should switch us to the worktree directory
	err = CheckoutBranch(repoPath, "worktree-branch")
	require.NoError(t, err)

	// Verify we're now in the worktree directory
	currentDir, err := os.Getwd()
	require.NoError(t, err)
	resolvedCurrentDir, err := filepath.EvalSymlinks(currentDir)
	require.NoError(t, err)
	resolvedWorktreeDir, err := filepath.EvalSymlinks(worktreeDir)
	require.NoError(t, err)
	require.Equal(t, resolvedWorktreeDir, resolvedCurrentDir)

	// Verify we're on the correct branch
	cmd = exec.Command("git", "branch", "--show-current")
	output, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "worktree-branch", strings.TrimSpace(string(output)))
}

func TestParseWorktreeDir_ValidOutput(t *testing.T) {
	output := `worktree /Users/test/main-repo
HEAD 1234567890abcdef

worktree /Users/test/feature-worktree
branch refs/heads/feature-branch

worktree /Users/test/bugfix-worktree
branch refs/heads/bugfix-branch
`

	// Test finding existing branch
	dir, err := parseWorktreeDir(output, "feature-branch")
	require.NoError(t, err)
	require.Equal(t, "/Users/test/feature-worktree", dir)

	// Test finding another existing branch
	dir, err = parseWorktreeDir(output, "bugfix-branch")
	require.NoError(t, err)
	require.Equal(t, "/Users/test/bugfix-worktree", dir)

	// Test non-existent branch
	_, err = parseWorktreeDir(output, "nonexistent-branch")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no worktree found")
}

func TestParseWorktreeDir_EmptyOutput(t *testing.T) {
	_, err := parseWorktreeDir("", "any-branch")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no worktree found")
}

func TestParseWorktreeDir_MalformedOutput(t *testing.T) {
	output := `worktree /Users/test/main-repo
invalid line
branch refs/heads/feature-branch`

	_, err := parseWorktreeDir(output, "feature-branch")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no worktree found")
}

func TestCheckoutBranch_DirtyWorkingDir(t *testing.T) {
	repoPath, _ := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(originalDir)

	err = os.Chdir(repoPath)
	require.NoError(t, err)

	// Create a test branch
	createBranchForCheckoutTest(t, repoPath, "test-branch")

	// Create an uncommitted change
	testFile := filepath.Join(repoPath, "dirty-file.txt")
	err = os.WriteFile(testFile, []byte("uncommitted change"), 0644)
	require.NoError(t, err)

	// Try to checkout with dirty working directory
	err = CheckoutBranch(repoPath, "test-branch")
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not clean")

	// Verify we're still on original branch
	verifyCurrentBranch(t, "main")

	// Clean up the dirty file and try again
	err = os.Remove(testFile)
	require.NoError(t, err)

	// Now checkout should succeed
	err = CheckoutBranch(repoPath, "test-branch")
	require.NoError(t, err)
	verifyCurrentBranch(t, "test-branch")
}

func TestCheckoutBranch_Integration(t *testing.T) {
	repoPath, _ := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	originalDir, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(originalDir)

	err = os.Chdir(repoPath)
	require.NoError(t, err)

	// Create multiple branches
	createBranchForCheckoutTest(t, repoPath, "branch-a")
	createBranchForCheckoutTest(t, repoPath, "branch-b")

	// Normal checkout to branch-a
	err = CheckoutBranch(repoPath, "branch-a")
	require.NoError(t, err)
	verifyCurrentBranch(t, "branch-a")

	// Normal checkout to branch-b
	err = CheckoutBranch(repoPath, "branch-b")
	require.NoError(t, err)
	verifyCurrentBranch(t, "branch-b")

	// Back to main
	err = CheckoutBranch(repoPath, "main")
	require.NoError(t, err)
	verifyCurrentBranch(t, "main")
}

// Helper functions

func createBranchForCheckoutTest(t *testing.T, repoPath, branchName string) {
	// Create branch
	cmd := exec.Command("git", "checkout", "-b", branchName)
	cmd.Dir = repoPath
	err := cmd.Run()
	require.NoError(t, err)

	// Switch back to main
	cmd = exec.Command("git", "checkout", "main")
	cmd.Dir = repoPath
	err = cmd.Run()
	require.NoError(t, err)
}

func verifyCurrentBranch(t *testing.T, expectedBranch string) {
	cmd := exec.Command("git", "branch", "--show-current")
	output, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, expectedBranch, strings.TrimSpace(string(output)))
}
