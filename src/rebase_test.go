package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRebaseBranchSpecificSave(t *testing.T) {
	// Setup test environment
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Get the worktree and initial commit hash
	worktree, err := repo.Worktree()
	require.NoError(t, err)
	headRef, err := repo.Head()
	require.NoError(t, err)
	initialCommitHash := headRef.Hash()

	// Create some branches with different commits
	branches := []string{"base-branch", "feature-1", "feature-2"}
	branchHashes := make(map[string]string)

	for i, branchName := range branches {
		// Checkout initial commit first to create branches from it
		err = worktree.Checkout(&git.CheckoutOptions{
			Hash: initialCommitHash,
		})
		require.NoError(t, err)

		// Create and checkout the branch
		err = worktree.Checkout(&git.CheckoutOptions{
			Branch: plumbing.NewBranchReferenceName(branchName),
			Create: true,
		})
		require.NoError(t, err)

		// Add a unique commit to this branch
		filename := "file.txt" // Overwrite the same file for simplicity
		content := fmt.Sprintf("%s content %c", branchName, 'A'+i)
		message := fmt.Sprintf("Commit on %s", branchName)
		commitHash := addSingleCommit(t, repoPath, worktree, filename, content, message)

		// Store the branch hash
		branchHashes[branchName] = commitHash.String()
	}

	// Create a tower with the branches
	towerName := "test-tower"
	towerBranches := make([]Branch, len(branches))
	for i, name := range branches {
		towerBranches[i] = Branch{Name: name}
	}
	towers := []*Tower{
		{
			Name:     towerName,
			Branches: towerBranches,
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, "") // No explicit base needed for this test

	// Save the config (handled by setupTestEnv initially, need to save changes)
	err = SaveConfig(config)
	require.NoError(t, err)

	// Test the reflog saving functionality
	// (The test logic manipulates the loaded config directly)

	// Load the config
	loadedConfig, err := LoadConfig()
	require.NoError(t, err)

	// Get the tower from config
	tower := FindTowerByName(loadedConfig.Repos[0], towerName)
	require.NotNil(t, tower, "Tower should exist in config")

	// Simulate storing reflog IDs (using the commit hashes we created)
	tower.LastRebased = time.Now().Format(time.RFC3339)
	for i := range tower.Branches {
		branch := &tower.Branches[i]
		branch.LastReflogID = branchHashes[branch.Name]
	}

	// Save the config with simulated reflog IDs
	err = SaveConfig(loadedConfig)
	require.NoError(t, err)

	// Load the config again
	updatedConfig, err := LoadConfig()
	require.NoError(t, err)

	// Check that the reflog IDs were saved correctly
	updatedTower := FindTowerByName(updatedConfig.Repos[0], towerName)
	require.NotNil(t, updatedTower, "Tower should exist in config")
	assert.NotEmpty(t, updatedTower.LastRebased, "Last rebased timestamp should be set")

	for _, branch := range updatedTower.Branches {
		assert.Equal(t, branchHashes[branch.Name], branch.LastReflogID,
			"Branch %s should have correct LastReflogID", branch.Name)
	}

	// Simulate the undo by clearing the reflog IDs in the config
	for i := range updatedTower.Branches {
		branch := &updatedTower.Branches[i]
		branch.LastReflogID = ""
	}
	updatedTower.LastRebased = ""

	// Save the config with cleared IDs
	err = SaveConfig(updatedConfig)
	require.NoError(t, err)

	// Verify they were cleared
	finalConfig, err := LoadConfig()
	require.NoError(t, err)

	finalTower := FindTowerByName(finalConfig.Repos[0], towerName)
	require.NotNil(t, finalTower, "Tower should exist in config")
	assert.Empty(t, finalTower.LastRebased, "Last rebased timestamp should be cleared")

	for _, branch := range finalTower.Branches {
		assert.Empty(t, branch.LastReflogID,
			"Branch %s should have LastReflogID cleared", branch.Name)
	}
}

func TestRebaseAndUndoWithActualRepo(t *testing.T) {
	// Setup test repository (using basic helper, not full env setup)
	tempDir, repo := setupTestRepo(t) // Use tempDir as repoPath
	defer os.RemoveAll(tempDir)

	// Get the worktree and initial commit
	wt, err := repo.Worktree()
	require.NoError(t, err)
	headRef, err := repo.Head()
	require.NoError(t, err)
	initialCommitHash := headRef.Hash()

	// Step 1: Create the base branch with 3 commits
	createTestBranch(t, repo, "base-branch", 3)

	// Step 2: Create middle-branch from base branch's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base-branch")})
	require.NoError(t, err)
	createTestBranch(t, repo, "middle-branch", 2)
	middleRef, err := repo.Reference(plumbing.NewBranchReferenceName("middle-branch"), true)
	require.NoError(t, err)

	// Step 3: Create top-branch from middle branch's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{Hash: middleRef.Hash()})
	require.NoError(t, err)
	createTestBranch(t, repo, "top-branch", 2)

	// Step 4: Go back to base-branch and add one more commit (creates divergence)
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("base-branch"),
	})
	require.NoError(t, err)
	addSingleCommit(t, tempDir, wt, "divergent-base.txt", "divergent base content", "Divergent commit on base branch")

	// Temporarily change working directory for config setup
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(oldWd)

	// Create a separate temp directory for the config file
	configTempDir, err := os.MkdirTemp("", "ghenga-test-rebase-actual-config")
	require.NoError(t, err)
	defer os.RemoveAll(configTempDir)

	err = os.Chdir(configTempDir) // Change CWD to config dir temporarily
	require.NoError(t, err)

	// Mock ConfigPath to point to the file in configTempDir
	configFile := filepath.Join(configTempDir, "config.toml")
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()
	ConfigPath = mockedConfigPath(configFile)

	// Initialize config with one tower and all branches in order
	towerName := "test-tower"
	towers := []*Tower{
		{
			Name: towerName,
			// Base and Branches are set via createTestConfig
			Branches: []Branch{
				{Name: "base-branch"},
				{Name: "middle-branch"},
				{Name: "top-branch"},
			},
		},
	}
	config := createTestConfig(t, tempDir, towerName, towers, initialCommitHash.String())
	err = SaveConfig(config)
	require.NoError(t, err)

	// Change CWD back to the repo directory for commands
	err = os.Chdir(tempDir)
	require.NoError(t, err)

	// 1. Create functions to capture command output
	captureListOutput := func() string {
		output, err := CaptureOutput(func() error {
			listCmd := &LsCmd{}
			return listCmd.Run(nil)
		})
		if err != nil {
			t.Fatalf("Failed to run list command: %v", err)
		}
		return output
	}

	// 2. Run list command to capture pre-rebase state
	preRebaseOutput := captureListOutput()
	t.Logf("Pre-rebase list output captured (%d bytes)", len(preRebaseOutput))

	// Verify divergence exists in the pre-rebase output
	assert.Contains(t, preRebaseOutput, "⚠️ This branch has diverged", "Pre-rebase output should show divergence")

	// 3. Mock user input for the rebase command (automatic "yes" to prompts)
	mockInput := func(input string) func() {
		oldStdin := os.Stdin
		r, w, _ := os.Pipe()
		os.Stdin = r
		w.Write([]byte(input + "\n"))
		w.Close()
		return func() { os.Stdin = oldStdin }
	}
	restoreStdin := mockInput("y")
	defer restoreStdin()

	// 4. Run the rebase command
	rebaseCmd := &RebaseDoCmd{}
	err = rebaseCmd.Run(nil)
	if err != nil {
		t.Fatalf("Failed to run rebase command: %v", err)
	}

	// 5. Run list command again to see post-rebase state
	postRebaseOutput := captureListOutput()
	t.Logf("Post-rebase list output captured (%d bytes)", len(postRebaseOutput))

	// Verify divergence no longer exists in the post-rebase output
	assert.NotContains(t, postRebaseOutput, "⚠️ This branch has diverged",
		"Post-rebase output should not show divergence")

	// 6. Run rebase undo
	restoreStdin = mockInput("y") // Refresh the stdin pipe for the undo prompt
	defer restoreStdin()

	undoCmd := &RebaseUndoCmd{}
	err = undoCmd.Run(nil)
	if err != nil {
		t.Fatalf("Failed to run rebase undo command: %v", err)
	}

	// 7. Run list command again to see post-undo state
	postUndoOutput := captureListOutput()
	t.Logf("Post-undo list output captured (%d bytes)", len(postUndoOutput))

	// Verify that divergence is back in the post-undo output
	assert.Contains(t, postUndoOutput, "⚠️ This branch has diverged",
		"Post-undo output should show divergence again")

	// 8. Verify stored branch-specific reflog IDs were cleared
	finalConfig, err := LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	tower := FindTowerByName(finalConfig.Repos[0], "test-tower")
	assert.NotNil(t, tower, "Tower should exist")
	assert.Empty(t, tower.LastRebased, "LastRebased should be cleared after undo")

	for _, branch := range tower.Branches {
		assert.Empty(t, branch.LastReflogID,
			"Branch %s should have LastReflogID cleared after undo", branch.Name)
	}
}
