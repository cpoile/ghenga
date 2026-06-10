package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// Get the worktree and initial commit
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
	config := createTestConfig(t, repoPath, towerName, towers, "main")

	// Save the config (handled by setupTestEnv initially, need to save changes)
	err = SaveConfig(config)
	require.NoError(t, err)

	// Test the reflog saving functionality
	// (The test logic manipulates the loaded config directly)

	// Load the config
	loadedConfig, err := LoadConfig()
	require.NoError(t, err)

	// Get the tower from config
	tower := findTowerByName(loadedConfig.Repos[0], towerName)
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
	updatedTower := findTowerByName(updatedConfig.Repos[0], towerName)
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

	finalTower := findTowerByName(finalConfig.Repos[0], towerName)
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

	// Get the worktree (initial commit hash not needed directly here)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = repo.Head() // Ensure repo head is read okay
	require.NoError(t, err)

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
	config := createTestConfig(t, tempDir, towerName, towers, "main")
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

	tower := findTowerByName(finalConfig.Repos[0], "test-tower")
	assert.NotNil(t, tower, "Tower should exist")
	assert.Empty(t, tower.LastRebased, "LastRebased should be cleared after undo")

	for _, branch := range tower.Branches {
		assert.Empty(t, branch.LastReflogID,
			"Branch %s should have LastReflogID cleared after undo", branch.Name)
	}
}

func TestRebaseUndoRecreatesDeletedBranch(t *testing.T) {
	// Setup test environment
	tempDir, repo := setupTestRepo(t)
	defer os.RemoveAll(tempDir)

	// Get the worktree (initial commit hash not needed directly here)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = repo.Head() // Ensure repo head is read okay
	require.NoError(t, err)

	// Create branches similar to TestRebaseAndUndoWithActualRepo
	createTestBranch(t, repo, "base-branch", 3)
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base-branch")})
	require.NoError(t, err)
	createTestBranch(t, repo, "middle-branch", 2)
	middleRef, err := repo.Reference(plumbing.NewBranchReferenceName("middle-branch"), true)
	require.NoError(t, err)
	err = wt.Checkout(&git.CheckoutOptions{Hash: middleRef.Hash()})
	require.NoError(t, err)
	createTestBranch(t, repo, "top-branch", 2)
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base-branch")})
	require.NoError(t, err)
	addSingleCommit(t, tempDir, wt, "divergent-base.txt", "divergent base content", "Divergent commit on base branch")

	// Setup Config
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(oldWd)
	configTempDir, err := os.MkdirTemp("", "ghenga-test-recreate-config")
	require.NoError(t, err)
	defer os.RemoveAll(configTempDir)
	err = os.Chdir(configTempDir)
	require.NoError(t, err)
	configFile := filepath.Join(configTempDir, "config.toml")
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()
	ConfigPath = mockedConfigPath(configFile)
	towerName := "test-tower-recreate"
	towers := []*Tower{
		{
			Name: towerName,
			Branches: []Branch{
				{Name: "base-branch"},
				{Name: "middle-branch"},
				{Name: "top-branch"},
			},
		},
	}
	config := createTestConfig(t, tempDir, towerName, towers, "main")
	err = SaveConfig(config)
	require.NoError(t, err)
	err = os.Chdir(tempDir)
	require.NoError(t, err)

	// Mock user input for the rebase command
	restoreRebaseStdin := mockInput("y")
	defer restoreRebaseStdin()

	// Run the rebase command
	rebaseCmd := &RebaseDoCmd{}
	err = rebaseCmd.Run(nil)
	require.NoError(t, err, "Failed to run rebase command")

	// Load config to get the reflog ID stored by the rebase command
	configAfterRebase, err := LoadConfig()
	require.NoError(t, err)
	towerAfterRebase := findTowerByName(configAfterRebase.Repos[0], towerName)
	require.NotNil(t, towerAfterRebase)

	branchToDeleTe := "middle-branch"
	var preRebaseHashOfDeletedBranch string
	for _, b := range towerAfterRebase.Branches {
		if b.Name == branchToDeleTe {
			preRebaseHashOfDeletedBranch = b.LastReflogID
			break
		}
	}
	require.NotEmpty(t, preRebaseHashOfDeletedBranch, "Could not find pre-rebase hash for %s", branchToDeleTe)

	// Delete the middle branch
	headAfterRebase, err := repo.Head()
	require.NoError(t, err)
	if headAfterRebase.Name().Short() == branchToDeleTe {
		err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base-branch")})
		require.NoError(t, err)
	}
	err = repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(branchToDeleTe))
	require.NoError(t, err, "Failed to delete branch %s", branchToDeleTe)

	// Mock user input for the undo command
	restoreUndoStdin := mockInput("y")
	defer restoreUndoStdin()

	// Run rebase undo
	undoCmd := &RebaseUndoCmd{}
	err = undoCmd.Run(nil)
	require.NoError(t, err, "Failed to run rebase undo command")

	// Verify the deleted branch was recreated and points to the correct commit
	recreatedRef, err := repo.Reference(plumbing.NewBranchReferenceName(branchToDeleTe), true)
	require.NoError(t, err, "Branch %s should have been recreated by undo", branchToDeleTe)
	assert.Equal(t, preRebaseHashOfDeletedBranch, recreatedRef.Hash().String(),
		"Recreated branch %s should point to its pre-rebase hash", branchToDeleTe)

	// Verify stored branch-specific reflog IDs were cleared
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(finalConfig.Repos[0], towerName)
	assert.NotNil(t, tower, "Tower should exist")
	assert.Empty(t, tower.LastRebased, "LastRebased should be cleared after undo")
	for _, branch := range tower.Branches {
		assert.Empty(t, branch.LastReflogID,
			"Branch %s should have LastReflogID cleared after undo", branch.Name)
	}
}

// --- Conflict Tests ---

// Helper to resolve a conflict by writing specific content and staging
func resolveConflict(t *testing.T, repoPath, filename, resolvedContent string) {
	filePath := filepath.Join(repoPath, filename)
	err := os.WriteFile(filePath, []byte(resolvedContent), 0644)
	require.NoError(t, err, "Failed to write resolved content to %s", filename)

	addCmd := exec.Command("git", "add", filename)
	addCmd.Dir = repoPath
	output, err := addCmd.CombinedOutput()
	require.NoError(t, err, "git add %s failed: %s", filename, string(output))
}

// Helper to check git status for conflicts
func checkHasConflicts(t *testing.T, repoPath string) bool {
	hasConflicts, err := hasConflicts(repoPath) // Use the actual function from rebase.go
	require.NoError(t, err, "Error checking git conflict status")
	return hasConflicts
}

func TestRebaseConflictSingleBranch(t *testing.T) {
	// Setup: base -> feature1, feature1 has one commit, base gets a conflicting commit
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()
	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Base branch setup
	baseCommit1Hash := addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2\nLine 3\n", "Base Commit 1")
	err = wt.Checkout(&git.CheckoutOptions{ // Create base branch
		Hash:   baseCommit1Hash,
		Branch: plumbing.NewBranchReferenceName("base"),
		Create: true,
	})
	require.NoError(t, err, "Failed to create base branch")

	// Feature branch setup (from Base Commit 1)
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseCommit1Hash}) // Go back to base commit 1 first
	require.NoError(t, err)
	featureCommit1Hash := addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2 MODIFIED ON FEATURE\nLine 3\n", "Feature Commit 1")
	err = wt.Checkout(&git.CheckoutOptions{ // Create feature branch
		Hash:   featureCommit1Hash,
		Branch: plumbing.NewBranchReferenceName("feature1"),
		Create: true,
	})
	require.NoError(t, err, "Failed to create feature1 branch")

	// Create conflicting commit on base
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base")})
	require.NoError(t, err)
	baseCommit2Hash := addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2 MODIFIED ON BASE\nLine 3\n", "Base Commit 2")

	// Setup Ghenga Config
	towerName := "conflict-tower"
	towers := []*Tower{
		{
			Name: towerName,
			Base: "base",
			Branches: []Branch{
				{Name: "base"},
				{Name: "feature1"},
			},
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, "base") // Set base explicitly
	err = SaveConfig(config)
	require.NoError(t, err)

	// Ensure initial state: feature1 is based on Base Commit 1
	feature1Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)
	feature1Commit, err := repo.CommitObject(feature1Ref.Hash())
	require.NoError(t, err)
	require.Equal(t, 1, feature1Commit.NumParents(), "feature1 should have 1 parent before rebase")
	require.Equal(t, baseCommit1Hash, feature1Commit.ParentHashes[0], "feature1 parent should be base commit 1")

	// --- Run Rebase (Expect Pause) ---
	t.Log("Running initial rebase, expecting pause...")
	restoreStdin := mockInput("y") // Confirm rebase start
	rebaseCmd := &RebaseDoCmd{}
	err = rebaseCmd.Run(nil)
	require.NoError(t, err, "rebase do command itself should return nil on pause")
	restoreStdin() // Restore stdin

	// --- Verify Pause State ---
	// 1. Check Git Status for conflicts
	t.Log("Verifying git status shows conflicts...")
	require.True(t, checkHasConflicts(t, repoPath), "Git status should show conflicts after pause")

	// 2. Check Ghenga Config State
	t.Log("Verifying ghenga config state is saved...")
	pausedConfig, err := LoadConfig()
	require.NoError(t, err)
	pausedRepoInfo := findRepoByPath(pausedConfig, repoPath)
	require.NotNil(t, pausedRepoInfo)
	pausedTower := findTowerByName(pausedRepoInfo, towerName)
	require.NotNil(t, pausedTower)
	require.NotNil(t, pausedTower.RebaseState, "RebaseState should exist in config")
	require.True(t, pausedTower.RebaseState.IsInProgress, "RebaseState.IsInProgress should be true")
	assert.Equal(t, "feature1", pausedTower.RebaseState.TargetBranch, "Paused target branch should be feature1")
	assert.Equal(t, "base", pausedTower.RebaseState.BaseBranch, "Paused base branch should be base")
	assert.NotEmpty(t, pausedTower.RebaseState.TemporaryBranch, "Temporary branch name should be saved")
	assert.Equal(t, 0, pausedTower.RebaseState.CurrentCommitIndex, "Paused commit index should be 0 (first commit failed)")
	require.Len(t, pausedTower.RebaseState.RemainingCommits, 1, "Should have 1 remaining commit for feature1")
	assert.Equal(t, featureCommit1Hash.String(), pausedTower.RebaseState.RemainingCommits[0], "Remaining commit hash mismatch")
	require.Len(t, pausedTower.RebaseState.RemainingBranchInfos, 1, "Should have 1 remaining branch info")
	assert.Equal(t, "feature1", pausedTower.RebaseState.RemainingBranchInfos[0].Name, "Remaining branch info name mismatch")

	// --- Resolve Conflict ---
	t.Log("Resolving conflict...")
	resolveConflict(t, repoPath, "file.txt", "Line 1\nRESOLVED CONTENT\nLine 3\n")

	// --- Run Continue ---
	t.Log("Running rebase continue...")
	continueCmd := &RebaseContinueCmd{}
	err = continueCmd.Run(nil)
	require.NoError(t, err, "rebase continue command failed")

	// --- Verify Final State ---
	// 1. Check Git Status is clean
	t.Log("Verifying git status is clean...")
	require.False(t, checkHasConflicts(t, repoPath), "Git status should be clean after continue")
	statusCmdClean := exec.Command("git", "status", "--porcelain")
	statusCmdClean.Dir = repoPath
	outputBytes, err := statusCmdClean.Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(outputBytes)), "Git status porcelain should be empty")

	// 2. Check Ghenga Config State is cleared
	t.Log("Verifying ghenga config state is cleared...")
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	finalRepoInfo := findRepoByPath(finalConfig, repoPath)
	require.NotNil(t, finalRepoInfo)
	finalTower := findTowerByName(finalRepoInfo, towerName)
	require.NotNil(t, finalTower)
	assert.Nil(t, finalTower.RebaseState, "RebaseState should be nil after successful continue")

	// 3. Check Branch History
	t.Log("Verifying branch history...")
	finalFeature1Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)
	finalFeature1Commit, err := repo.CommitObject(finalFeature1Ref.Hash())
	require.NoError(t, err)
	// The new feature1 commit should have baseCommit2 as its parent
	require.Equal(t, 1, finalFeature1Commit.NumParents(), "feature1 should have 1 parent after rebase")
	assert.Equal(t, baseCommit2Hash, finalFeature1Commit.ParentHashes[0], "feature1 parent should be base commit 2 after rebase")

	// 4. Check File Content
	t.Log("Verifying file content...")
	// Checkout feature1 to check its content, then go back to base
	checkoutFeatureCmd := exec.Command("git", "checkout", "feature1")
	checkoutFeatureCmd.Dir = repoPath
	checkoutOutputBytes1, err := checkoutFeatureCmd.CombinedOutput()
	require.NoError(t, err, "Failed to checkout feature1 post-rebase: %s", string(checkoutOutputBytes1))
	contentBytes, err := os.ReadFile(filepath.Join(repoPath, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "Line 1\nRESOLVED CONTENT\nLine 3\n", string(contentBytes), "File content after rebase is incorrect")
	// Go back to the original branch ('base' in this test) to leave repo state clean
	checkoutBaseCmd := exec.Command("git", "checkout", "base")
	checkoutBaseCmd.Dir = repoPath
	checkoutOutputBytes2, err := checkoutBaseCmd.CombinedOutput()
	require.NoError(t, err, "Failed to checkout base after checking feature1 content: %s", string(checkoutOutputBytes2))
}

func TestRebaseConflictMultiBranch(t *testing.T) {
	// Setup: base -> feature1 -> feature2
	// Conflict on feature1, then conflict on feature2
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()
	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Base branch setup
	_ = addSingleCommit(t, repoPath, wt, "file1.txt", "File1 Line1\nFile1 Line2\n", "Base C1") // baseC1 hash not used
	baseC2 := addSingleCommit(t, repoPath, wt, "file2.txt", "File2 Line1\nFile2 Line2\n", "Base C2")
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseC2, Branch: plumbing.NewBranchReferenceName("base"), Create: true})
	require.NoError(t, err)

	// Feature1 branch setup (from Base C2)
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseC2})
	require.NoError(t, err)
	// Modify file1 (will conflict later)
	f1C1 := addSingleCommit(t, repoPath, wt, "file1.txt", "File1 Line1\nFile1 Line2 F1 MOD\n", "F1 C1")
	err = wt.Checkout(&git.CheckoutOptions{Hash: f1C1, Branch: plumbing.NewBranchReferenceName("feature1"), Create: true})
	require.NoError(t, err)

	// Feature2 branch setup (from F1 C1)
	err = wt.Checkout(&git.CheckoutOptions{Hash: f1C1})
	require.NoError(t, err)
	// Modify file2 (will conflict later)
	f2C1 := addSingleCommit(t, repoPath, wt, "file2.txt", "File2 Line1 F2 MOD\nFile2 Line2\n", "F2 C1")
	err = wt.Checkout(&git.CheckoutOptions{Hash: f2C1, Branch: plumbing.NewBranchReferenceName("feature2"), Create: true})
	require.NoError(t, err)

	// Create conflicting commits on base and feature1
	// Conflict for feature1 rebase
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base")})
	require.NoError(t, err)
	_ = addSingleCommit(t, repoPath, wt, "file1.txt", "File1 Line1\nFile1 Line2 BASE MOD\n", "Base C3") // baseC3 hash not used

	// Conflict for feature2 rebase (needs to be on feature1 *after* it diverges from base)
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("feature1")})
	require.NoError(t, err)
	_ = addSingleCommit(t, repoPath, wt, "file2.txt", "File2 Line1 F1 LATER MOD\nFile2 Line2\n", "F1 C2") // f1C2 hash not used

	// Setup Ghenga Config
	towerName := "multi-conflict-tower"
	towers := []*Tower{
		{
			Name: towerName,
			Base: "base",
			Branches: []Branch{
				{Name: "base"},
				{Name: "feature1"},
				{Name: "feature2"},
			},
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, "base")
	err = SaveConfig(config)
	require.NoError(t, err)

	// --- Run Rebase (Expect Pause on feature1) ---
	t.Log("Running initial rebase, expecting pause on feature1...")
	restoreStdin := mockInput("y")
	rebaseCmd := &RebaseDoCmd{}
	err = rebaseCmd.Run(nil)
	require.NoError(t, err, "rebase do should return nil on first pause")
	restoreStdin()

	// --- Verify Pause State (feature1) ---
	t.Log("Verifying state after first pause (feature1)...")
	require.True(t, checkHasConflicts(t, repoPath), "Git status should show conflicts after first pause")
	pausedConfig1, err := LoadConfig()
	require.NoError(t, err)
	pausedTower1 := findTowerByName(findRepoByPath(pausedConfig1, repoPath), towerName)
	require.NotNil(t, pausedTower1)
	require.NotNil(t, pausedTower1.RebaseState, "RebaseState should exist")
	assert.Equal(t, "feature1", pausedTower1.RebaseState.TargetBranch)
	assert.Equal(t, "base", pausedTower1.RebaseState.BaseBranch)
	assert.Equal(t, 0, pausedTower1.RebaseState.CurrentCommitIndex) // f1C1 failed
	require.Len(t, pausedTower1.RebaseState.RemainingBranchInfos, 2, "Should have feature1 and feature2 remaining")
	assert.Equal(t, "feature1", pausedTower1.RebaseState.RemainingBranchInfos[0].Name)
	assert.Equal(t, "feature2", pausedTower1.RebaseState.RemainingBranchInfos[1].Name)

	// --- Resolve Conflict 1 & Continue (Expect Pause on feature2) ---
	t.Log("Resolving conflict on file1 and running continue, expecting pause on feature2...")
	resolveConflict(t, repoPath, "file1.txt", "File1 Line1\nRESOLVED F1\n")
	continueCmd := &RebaseContinueCmd{}
	err = continueCmd.Run(nil)
	require.NoError(t, err, "rebase continue should return nil on second pause")

	// --- Verify Pause State (feature2) ---
	t.Log("Verifying state after second pause (feature2)...")
	require.True(t, checkHasConflicts(t, repoPath), "Git status should show conflicts after second pause")
	pausedConfig2, err := LoadConfig()
	require.NoError(t, err)
	pausedTower2 := findTowerByName(findRepoByPath(pausedConfig2, repoPath), towerName)
	require.NotNil(t, pausedTower2)
	require.NotNil(t, pausedTower2.RebaseState, "RebaseState should exist")
	assert.True(t, pausedTower2.RebaseState.IsInProgress)
	assert.Equal(t, "feature2", pausedTower2.RebaseState.TargetBranch, "Paused target should now be feature2")
	assert.Equal(t, "feature1", pausedTower2.RebaseState.BaseBranch, "Paused base should now be feature1") // Base for feature2 is feature1
	assert.Equal(t, 0, pausedTower2.RebaseState.CurrentCommitIndex)                                        // f2C1 failed
	require.Len(t, pausedTower2.RebaseState.RemainingBranchInfos, 1, "Should only have feature2 remaining")
	assert.Equal(t, "feature2", pausedTower2.RebaseState.RemainingBranchInfos[0].Name)

	// --- Resolve Conflict 2 & Continue (Expect Success) ---
	t.Log("Resolving conflict on file2 and running continue, expecting success...")
	resolveConflict(t, repoPath, "file2.txt", "File2 Line1 RESOLVED F2\nFile2 Line2\n")
	err = continueCmd.Run(nil) // Run continue again
	require.NoError(t, err, "second rebase continue command failed")

	// --- Verify Final State ---
	t.Log("Verifying final state...")
	require.False(t, checkHasConflicts(t, repoPath), "Git status should be clean after second continue")
	statusCmdCleanMulti := exec.Command("git", "status", "--porcelain")
	statusCmdCleanMulti.Dir = repoPath
	outputBytesMulti, err := statusCmdCleanMulti.Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(outputBytesMulti)), "Git status porcelain should be empty")

	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	finalTower := findTowerByName(findRepoByPath(finalConfig, repoPath), towerName)
	require.NotNil(t, finalTower)
	assert.Nil(t, finalTower.RebaseState, "RebaseState should be nil after successful completion")

	// Check history (simplified check: feature2 parent should be feature1's new head)
	finalF2Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature2"), true)
	require.NoError(t, err)
	finalF1Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)
	finalF2Commit, err := repo.CommitObject(finalF2Ref.Hash())
	require.NoError(t, err)
	assert.Equal(t, finalF1Ref.Hash(), finalF2Commit.ParentHashes[0], "feature2 parent should be feature1 head after rebase")

	// Check file contents on final branches
	checkoutCmd := exec.Command("git", "checkout", "feature1")
	checkoutCmd.Dir = repoPath
	checkoutOutputBytesMulti1, err := checkoutCmd.CombinedOutput()
	require.NoError(t, err, "Failed checkout f1 multi: %s", string(checkoutOutputBytesMulti1))
	content1Bytes, err := os.ReadFile(filepath.Join(repoPath, "file1.txt"))
	require.NoError(t, err)
	assert.Equal(t, "File1 Line1\nRESOLVED F1\n", string(content1Bytes), "File1 content on feature1 incorrect")

	checkoutCmd = exec.Command("git", "checkout", "feature2")
	checkoutCmd.Dir = repoPath
	checkoutOutputBytesMulti2, err := checkoutCmd.CombinedOutput()
	require.NoError(t, err, "Failed checkout f2 multi: %s", string(checkoutOutputBytesMulti2))
	content2Bytes, err := os.ReadFile(filepath.Join(repoPath, "file2.txt"))
	require.NoError(t, err)
	assert.Equal(t, "File2 Line1 RESOLVED F2\nFile2 Line2\n", string(content2Bytes), "File2 content on feature2 incorrect")
}

func TestRebaseConflictContinueWithoutResolving(t *testing.T) {
	// Setup: Same as SingleBranch conflict
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()
	wt, err := repo.Worktree()
	require.NoError(t, err)

	baseCommit1Hash := addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2\nLine 3\n", "Base Commit 1")
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseCommit1Hash, Branch: plumbing.NewBranchReferenceName("base"), Create: true})
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{Hash: baseCommit1Hash})
	require.NoError(t, err)
	featureCommit1Hash := addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2 MODIFIED ON FEATURE\nLine 3\n", "Feature Commit 1")
	err = wt.Checkout(&git.CheckoutOptions{Hash: featureCommit1Hash, Branch: plumbing.NewBranchReferenceName("feature1"), Create: true})
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base")})
	require.NoError(t, err)
	_ = addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2 MODIFIED ON BASE\nLine 3\n", "Base Commit 2")

	towerName := "conflict-tower-noresolve"
	towers := []*Tower{
		{
			Name:     towerName,
			Base:     "base",
			Branches: []Branch{{Name: "base"}, {Name: "feature1"}},
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, "base")
	err = SaveConfig(config)
	require.NoError(t, err)

	// --- Run Rebase (Expect Pause) ---
	t.Log("Running initial rebase, expecting pause...")
	restoreStdin := mockInput("y") // Confirm rebase start
	rebaseCmd := &RebaseDoCmd{}
	err = rebaseCmd.Run(nil)
	require.NoError(t, err, "rebase do should return nil on pause")
	restoreStdin()

	// --- Verify Pause State ---
	t.Log("Verifying git status shows conflicts...")
	require.True(t, checkHasConflicts(t, repoPath), "Git status should show conflicts after pause")
	// Check config briefly
	pausedConfig, err := LoadConfig()
	require.NoError(t, err)
	pausedTower := findTowerByName(findRepoByPath(pausedConfig, repoPath), towerName)
	require.NotNil(t, pausedTower)
	require.NotNil(t, pausedTower.RebaseState)
	initialStateJSON, _ := json.Marshal(pausedTower.RebaseState) // Save state for comparison

	// --- Run Continue WITHOUT Resolving ---
	t.Log("Running rebase continue without resolving...")
	continueCmd := &RebaseContinueCmd{}
	// Capture output to check message
	continueCapturedOutputStr, err := CaptureOutput(func() error {
		return continueCmd.Run(nil)
	})
	require.NoError(t, err, "rebase continue should return nil error when conflicts exist")
	t.Logf("Continue output:\n%s", continueCapturedOutputStr)

	// --- Verify State After Failed Continue ---
	// 1. Check Output Message
	assert.Contains(t, continueCapturedOutputStr, "Conflicts still detected", "Output should mention detected conflicts")
	assert.Contains(t, continueCapturedOutputStr, "Please resolve the conflicts", "Output should instruct user to resolve")

	// 2. Check Git Status still shows conflicts
	t.Log("Verifying git status still shows conflicts...")
	require.True(t, checkHasConflicts(t, repoPath), "Git status should still show conflicts after failed continue")

	// 3. Check Ghenga Config State UNCHANGED
	t.Log("Verifying ghenga config state is unchanged...")
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	finalTower := findTowerByName(findRepoByPath(finalConfig, repoPath), towerName)
	require.NotNil(t, finalTower)
	require.NotNil(t, finalTower.RebaseState, "RebaseState should still exist")
	finalStateJSON, _ := json.Marshal(finalTower.RebaseState)
	assert.JSONEq(t, string(initialStateJSON), string(finalStateJSON), "RebaseState in config should be unchanged")
}

func TestRebaseConflictContinueWithManualCommit(t *testing.T) {
	// Setup: Same as SingleBranch conflict
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()
	wt, err := repo.Worktree()
	require.NoError(t, err)

	baseCommit1Hash := addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2\nLine 3\n", "Base Commit 1")
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseCommit1Hash, Branch: plumbing.NewBranchReferenceName("base"), Create: true})
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{Hash: baseCommit1Hash})
	require.NoError(t, err)
	featureCommit1Hash := addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2 MODIFIED ON FEATURE\nLine 3\n", "Feature Commit 1")
	err = wt.Checkout(&git.CheckoutOptions{Hash: featureCommit1Hash, Branch: plumbing.NewBranchReferenceName("feature1"), Create: true})
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base")})
	require.NoError(t, err)
	baseCommit2Hash := addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2 MODIFIED ON BASE\nLine 3\n", "Base Commit 2")

	towerName := "conflict-tower-manualcommit"
	towers := []*Tower{
		{
			Name:     towerName,
			Base:     "base",
			Branches: []Branch{{Name: "base"}, {Name: "feature1"}},
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, "base")
	err = SaveConfig(config)
	require.NoError(t, err)

	// --- Run Rebase (Expect Pause) ---
	t.Log("Running initial rebase, expecting pause...")
	restoreStdin := mockInput("y") // Confirm rebase start
	rebaseCmd := &RebaseDoCmd{}
	err = rebaseCmd.Run(nil)
	require.NoError(t, err, "rebase do should return nil on pause")
	restoreStdin()

	// --- Verify Pause State ---
	t.Log("Verifying git status shows conflicts...")
	require.True(t, checkHasConflicts(t, repoPath), "Git status should show conflicts after pause")

	// --- Resolve Conflict & COMMIT MANUALLY ---
	t.Log("Resolving conflict and committing manually...")
	resolveConflict(t, repoPath, "file.txt", "Line 1\nMANUAL RESOLVE & COMMIT\nLine 3\n")
	// Manually commit instead of just staging
	commitCmd := exec.Command("git", "commit", "-m", "Manually resolved conflict")
	commitCmd.Dir = repoPath
	commitOutputBytes, err := commitCmd.CombinedOutput()
	require.NoError(t, err, "git commit failed: %s", string(commitOutputBytes))

	// Get the hash of the manual commit
	manualCommitHash, err := repo.ResolveRevision(plumbing.Revision("HEAD"))
	require.NoError(t, err)

	// --- Run Continue ---
	t.Log("Running rebase continue after manual commit...")
	continueCmd := &RebaseContinueCmd{}
	// Capture output to check message
	continueCapturedOutputStr, err := CaptureOutput(func() error {
		return continueCmd.Run(nil)
	})
	require.NoError(t, err, "rebase continue command failed after manual commit")
	t.Logf("Continue output:\n%s", continueCapturedOutputStr)

	// --- Verify Final State ---
	// 1. Check Output Message
	assert.Contains(t, continueCapturedOutputStr, "No cherry-pick operation to continue directly", "Output should indicate manual commit was detected")

	// 2. Check Git Status is clean
	t.Log("Verifying git status is clean...")
	require.False(t, checkHasConflicts(t, repoPath), "Git status should be clean after continue")
	statusCmdManual := exec.Command("git", "status", "--porcelain")
	statusCmdManual.Dir = repoPath
	statusOutputBytes, err := statusCmdManual.Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(statusOutputBytes)), "Git status porcelain should be empty")

	// 3. Check Ghenga Config State is cleared
	t.Log("Verifying ghenga config state is cleared...")
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	finalTower := findTowerByName(findRepoByPath(finalConfig, repoPath), towerName)
	require.NotNil(t, finalTower)
	assert.Nil(t, finalTower.RebaseState, "RebaseState should be nil after successful continue")

	// 4. Check Branch History
	t.Log("Verifying branch history...")
	finalFeature1Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)
	// The head of feature1 should be our manual commit
	assert.Equal(t, manualCommitHash.String(), finalFeature1Ref.Hash().String(), "feature1 head should be the manual commit hash")
	// Its parent should be the base branch's head before the rebase started
	finalFeature1Commit, err := repo.CommitObject(finalFeature1Ref.Hash())
	require.NoError(t, err)
	require.Equal(t, 1, finalFeature1Commit.NumParents(), "feature1 should have 1 parent after rebase")
	assert.Equal(t, baseCommit2Hash, finalFeature1Commit.ParentHashes[0], "feature1 parent should be base commit 2 after rebase")

	// 5. Check File Content
	t.Log("Verifying file content...")
	checkoutFeatureCmd := exec.Command("git", "checkout", "feature1")
	checkoutFeatureCmd.Dir = repoPath
	checkoutOutputBytesManual, err := checkoutFeatureCmd.CombinedOutput()
	require.NoError(t, err, "Failed to checkout feature1 post-rebase: %s", string(checkoutOutputBytesManual))
	contentBytesManual, err := os.ReadFile(filepath.Join(repoPath, "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "Line 1\nMANUAL RESOLVE & COMMIT\nLine 3\n", string(contentBytesManual), "File content after rebase is incorrect")
}

func TestRebaseWithWorktreeBranches(t *testing.T) {
	// Setup test repository
	tempDir, repo := setupTestRepo(t)
	defer os.RemoveAll(tempDir)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create tower structure similar to TestRebaseAndUndoWithActualRepo:
	// main (tower base) -> feat1 (in tower, 2 commits) -> feat2 (in tower, 2 commits)
	// Then we'll add a diverging commit on feat1 to trigger rebase for feat2
	// (The rebase loop starts from index 1, so feat2 is compared against feat1)

	// Step 1: Create feat1 branch from main with 2 commits
	createTestBranch(t, repo, "feat1", 2)

	// Step 2: Create feat2 branch from feat1's HEAD with 2 commits
	createTestBranch(t, repo, "feat2", 2)

	// Step 3: Go back to main before creating worktrees
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	// Create worktrees for feat1 and feat2 - use absolute paths with unique names
	wt1Dir, err := filepath.Abs(filepath.Join(tempDir, "..", "wt-feat1-"+filepath.Base(tempDir)))
	require.NoError(t, err)
	wt2Dir, err := filepath.Abs(filepath.Join(tempDir, "..", "wt-feat2-"+filepath.Base(tempDir)))
	require.NoError(t, err)

	cmd := exec.Command("git", "worktree", "add", wt1Dir, "feat1")
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree for feat1: %s", string(output))
	defer os.RemoveAll(wt1Dir)

	cmd = exec.Command("git", "worktree", "add", wt2Dir, "feat2")
	cmd.Dir = tempDir
	output, err = cmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree for feat2: %s", string(output))
	defer os.RemoveAll(wt2Dir)

	// Step 4: Add diverging commit to feat1 (creates divergence for feat2)
	// feat2 was created from feat1's original tip, so adding a commit to feat1 causes divergence
	// Note: We need to use the worktree for feat1 since we can't checkout feat1 from main repo
	wt1File := filepath.Join(wt1Dir, "diverge.txt")
	err = os.WriteFile(wt1File, []byte("diverge"), 0644)
	require.NoError(t, err)
	cmd = exec.Command("git", "add", "diverge.txt")
	cmd.Dir = wt1Dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "Divergent commit on feat1")
	cmd.Dir = wt1Dir
	require.NoError(t, cmd.Run())

	// Setup config
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)

	configTempDir, _ := os.MkdirTemp("", "ghenga-test-worktree-config")
	defer os.RemoveAll(configTempDir)

	configFile := filepath.Join(configTempDir, "config.toml")
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()
	ConfigPath = mockedConfigPath(configFile)

	// Tower: main (base) -> feat1 -> feat2
	// feat1 is at index 0, feat2 is at index 1
	// Rebase loop starts at index 1, so feat2 is compared against feat1
	towers := []*Tower{{
		Name:     "test-tower",
		Branches: []Branch{{Name: "feat1"}, {Name: "feat2"}},
	}}
	config := createTestConfig(t, tempDir, "test-tower", towers, "main")
	SaveConfig(config)

	os.Chdir(tempDir)

	// First verify divergence exists
	preOutput, _ := CaptureOutput(func() error {
		return (&LsCmd{}).Run(nil)
	})
	require.Contains(t, preOutput, "⚠️ This branch has diverged",
		"Pre-rebase should show divergence - test setup issue if not")

	// Run rebase - should succeed even with worktrees
	restoreStdin := mockInput("y")
	defer restoreStdin()

	rebaseCmd := &RebaseDoCmd{}
	err = rebaseCmd.Run(nil)

	// This should NOT fail - currently it does with "already used by worktree"
	require.NoError(t, err, "Rebase should succeed with worktree branches")

	// Verify branches were actually rebased (no divergence warning)
	lsOutput, _ := CaptureOutput(func() error {
		return (&LsCmd{}).Run(nil)
	})
	assert.NotContains(t, lsOutput, "⚠️ This branch has diverged",
		"Post-rebase should not show divergence")
}

func TestRebaseWithWorktreeBranchesShowsWarnings(t *testing.T) {
	// This test verifies that worktree warnings are displayed during rebase
	tempDir, repo := setupTestRepo(t)
	defer os.RemoveAll(tempDir)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create tower structure: main -> feat1 -> feat2
	createTestBranch(t, repo, "feat1", 2)
	createTestBranch(t, repo, "feat2", 2)

	// Go back to main
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	// Create worktree for feat1
	wt1Dir, err := filepath.Abs(filepath.Join(tempDir, "..", "wt-feat1-warn-"+filepath.Base(tempDir)))
	require.NoError(t, err)
	cmd := exec.Command("git", "worktree", "add", wt1Dir, "feat1")
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree for feat1: %s", string(output))
	defer os.RemoveAll(wt1Dir)

	// Add diverging commit to feat1 (via worktree)
	wt1File := filepath.Join(wt1Dir, "diverge.txt")
	err = os.WriteFile(wt1File, []byte("diverge"), 0644)
	require.NoError(t, err)
	cmd = exec.Command("git", "add", "diverge.txt")
	cmd.Dir = wt1Dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "Divergent commit on feat1")
	cmd.Dir = wt1Dir
	require.NoError(t, cmd.Run())

	// Setup config
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)

	configTempDir, _ := os.MkdirTemp("", "ghenga-test-worktree-warn-config")
	defer os.RemoveAll(configTempDir)

	configFile := filepath.Join(configTempDir, "config.toml")
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()
	ConfigPath = mockedConfigPath(configFile)

	towers := []*Tower{{
		Name:     "test-tower",
		Branches: []Branch{{Name: "feat1"}, {Name: "feat2"}},
	}}
	config := createTestConfig(t, tempDir, "test-tower", towers, "main")
	SaveConfig(config)

	os.Chdir(tempDir)

	// Run rebase and capture output
	restoreStdin := mockInput("y")
	defer restoreStdin()

	rebaseOutput, err := CaptureOutput(func() error {
		rebaseCmd := &RebaseDoCmd{}
		return rebaseCmd.Run(nil)
	})
	require.NoError(t, err, "Rebase should succeed")

	// Verify worktree warning was shown before rebase
	assert.Contains(t, rebaseOutput, "branches are checked out in worktrees",
		"Should show worktree warning before rebase")
	assert.Contains(t, rebaseOutput, wt1Dir,
		"Should show worktree path in warning")

	// feat1 is in a worktree but was NOT rebased (only feat2 was rebased),
	// so post-rebase reset instructions should NOT appear for feat1
	assert.NotContains(t, rebaseOutput, "Worktrees need syncing",
		"Should not show worktree sync instructions when no rebased branches are in worktrees")
}

func TestRebaseWithWorktreeBranchesResetsWhenConfirmed(t *testing.T) {
	// This test verifies that when user confirms, rebased worktrees are reset.
	// feat2 is the rebased branch and is in a worktree, so reset should be offered.
	tempDir, repo := setupTestRepo(t)
	defer os.RemoveAll(tempDir)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create tower structure: main -> feat1 -> feat2
	createTestBranch(t, repo, "feat1", 2)
	createTestBranch(t, repo, "feat2", 2)

	// Go back to main
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	// Create worktree for feat2 (the branch that will be rebased)
	wt2Dir, err := filepath.Abs(filepath.Join(tempDir, "..", "wt-feat2-reset-"+filepath.Base(tempDir)))
	require.NoError(t, err)
	cmd := exec.Command("git", "worktree", "add", wt2Dir, "feat2")
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree for feat2: %s", string(output))
	defer os.RemoveAll(wt2Dir)

	// Add diverging commit to feat1 (from main repo) - this causes feat2 to need rebasing
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("feat1")})
	require.NoError(t, err)
	addSingleCommit(t, tempDir, wt, "diverge.txt", "diverge", "Divergent commit on feat1")
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	// Setup config
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)

	configTempDir, _ := os.MkdirTemp("", "ghenga-test-worktree-reset-config")
	defer os.RemoveAll(configTempDir)

	configFile := filepath.Join(configTempDir, "config.toml")
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()
	ConfigPath = mockedConfigPath(configFile)

	towers := []*Tower{{
		Name:     "test-tower",
		Branches: []Branch{{Name: "feat1"}, {Name: "feat2"}},
	}}
	config := createTestConfig(t, tempDir, "test-tower", towers, "main")
	SaveConfig(config)

	os.Chdir(tempDir)

	// Run rebase with TWO "y" responses: one for rebase confirmation, one for reset confirmation
	restoreStdin := mockInput("y\ny")
	defer restoreStdin()

	rebaseOutput, err := CaptureOutput(func() error {
		rebaseCmd := &RebaseDoCmd{}
		return rebaseCmd.Run(nil)
	})
	require.NoError(t, err, "Rebase should succeed")

	// Verify reset was offered and performed for feat2 (the rebased worktree branch)
	assert.Contains(t, rebaseOutput, "Worktrees need syncing",
		"Should show worktree sync instructions for rebased branch in worktree")
	assert.Contains(t, rebaseOutput, "Successfully reset",
		"Should show success message for reset")
	assert.Contains(t, rebaseOutput, "feat2",
		"Should mention the rebased branch being reset")
}

func TestRebaseBlocksDirtyWorktrees(t *testing.T) {
	// This test verifies that rebase is blocked when worktrees have uncommitted changes
	tempDir, repo := setupTestRepo(t)
	defer os.RemoveAll(tempDir)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create tower structure: main -> feat1 -> feat2
	createTestBranch(t, repo, "feat1", 2)
	createTestBranch(t, repo, "feat2", 2)

	// Go back to main
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	// Create worktree for feat1
	wt1Dir, err := filepath.Abs(filepath.Join(tempDir, "..", "wt-feat1-dirty-"+filepath.Base(tempDir)))
	require.NoError(t, err)
	cmd := exec.Command("git", "worktree", "add", wt1Dir, "feat1")
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree for feat1: %s", string(output))
	defer os.RemoveAll(wt1Dir)

	// Add diverging commit to feat1 (via worktree) to create something to rebase
	wt1File := filepath.Join(wt1Dir, "diverge.txt")
	err = os.WriteFile(wt1File, []byte("diverge"), 0644)
	require.NoError(t, err)
	cmd = exec.Command("git", "add", "diverge.txt")
	cmd.Dir = wt1Dir
	require.NoError(t, cmd.Run())
	cmd = exec.Command("git", "commit", "-m", "Divergent commit on feat1")
	cmd.Dir = wt1Dir
	require.NoError(t, cmd.Run())

	// Now create uncommitted changes in the worktree
	err = os.WriteFile(wt1File, []byte("modified content"), 0644)
	require.NoError(t, err)

	// Setup config
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)

	configTempDir, _ := os.MkdirTemp("", "ghenga-test-dirty-worktree-config")
	defer os.RemoveAll(configTempDir)

	configFile := filepath.Join(configTempDir, "config.toml")
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()
	ConfigPath = mockedConfigPath(configFile)

	towers := []*Tower{{
		Name:     "test-tower",
		Branches: []Branch{{Name: "feat1"}, {Name: "feat2"}},
	}}
	config := createTestConfig(t, tempDir, "test-tower", towers, "main")
	SaveConfig(config)

	os.Chdir(tempDir)

	// Run rebase - should fail due to dirty worktree
	restoreStdin := mockInput("y")
	defer restoreStdin()

	rebaseOutput, err := CaptureOutput(func() error {
		rebaseCmd := &RebaseDoCmd{}
		return rebaseCmd.Run(nil)
	})

	// Should fail with error about uncommitted changes
	require.Error(t, err, "Rebase should fail with dirty worktree")
	assert.Contains(t, err.Error(), "uncommitted changes detected",
		"Error should mention uncommitted changes")
	assert.Contains(t, err.Error(), wt1Dir,
		"Error should mention the dirty worktree path")
	assert.Contains(t, err.Error(), "feat1",
		"Error should mention the branch with dirty worktree")

	// Output should be empty or minimal since we failed early
	_ = rebaseOutput // Just to use the variable
}

// TestRebaseKeepsEmptyCommitsWithoutPausing verifies that the rebase runs unattended when a
// cherry-picked commit is empty — whether it was empty on the tower (e.g. a "no code changes this
// phase" marker) or becomes empty because its change is already in the new base (e.g. after a
// squash-merge). Such commits are kept with their original message instead of stopping the rebase
// to demand a manual 'git commit --allow-empty'.
func TestRebaseKeepsEmptyCommitsWithoutPausing(t *testing.T) {
	tempDir, repo := setupTestRepo(t)
	defer os.RemoveAll(tempDir)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// branch1 from main with commit A.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("branch1"), Create: true}))
	addSingleCommit(t, tempDir, wt, "fileA.txt", "content A", "Commit A on branch1")

	// branch2 from branch1 with commit B.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("branch2"), Create: true}))
	addSingleCommit(t, tempDir, wt, "fileB.txt", "content B", "Commit B on branch2")

	// branch1 diverges with commits C and D.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("branch1")}))
	commitC := addSingleCommit(t, tempDir, wt, "fileC.txt", "content C", "Commit C on branch1")
	addSingleCommit(t, tempDir, wt, "fileD.txt", "content D", "Commit D on branch1")

	// branch2: a normal commit E, an intentionally empty marker commit, then a copy of C that
	// becomes redundant once branch2 is rebased onto branch1 (which already has C).
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("branch2")}))
	addSingleCommit(t, tempDir, wt, "fileE.txt", "content E", "Commit E on branch2")
	runGitCommand(t, tempDir, "commit", "--allow-empty", "-m", "Empty marker commit on branch2")
	runGitCommand(t, tempDir, "cherry-pick", commitC.String())

	// Tower [branch1, branch2] on base main.
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	configTempDir, _ := os.MkdirTemp("", "ghenga-test-empty-keep-config")
	defer os.RemoveAll(configTempDir)
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()
	ConfigPath = mockedConfigPath(filepath.Join(configTempDir, "config.toml"))

	towers := []*Tower{{Name: "test-tower", Branches: []Branch{{Name: "branch1"}, {Name: "branch2"}}}}
	config := createTestConfig(t, tempDir, "test-tower", towers, "main")
	require.NoError(t, SaveConfig(config))

	require.NoError(t, os.Chdir(tempDir))
	restoreStdin := mockInput("y")
	defer restoreStdin()

	output, err := CaptureOutput(func() error { return (&RebaseDoCmd{}).Run(nil) })
	t.Logf("Rebase output:\n%s", output)

	// The rebase must run to completion without stopping on the empty/redundant commits.
	require.NoError(t, err, "rebase should not error")
	require.NotContains(t, output, "Cherry-pick failed", "empty commits must not pause the rebase")
	require.Contains(t, output, "rebase completed successfully", "rebase should complete")

	// No paused rebase state should remain.
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	finalTower := findTowerByName(findRepoByPath(finalConfig, tempDir), "test-tower")
	require.NotNil(t, finalTower)
	require.Nil(t, finalTower.RebaseState, "no paused rebase state should remain")

	// The empty marker commit is preserved with its original message on the rebased branch2.
	logOut := string(runGitCommand(t, tempDir, "log", "--format=%s", "branch2"))
	require.Contains(t, logOut, "Empty marker commit on branch2",
		"empty marker commit should be preserved with its message")
}

func TestRebaseRestoresMainRepoBranchWhenRunFromWorktree(t *testing.T) {
	// This test verifies that when rebase is run from a worktree directory,
	// the main repo's original branch is preserved after the rebase completes.
	//
	// Bug scenario:
	// 1. Main repo is on "master" branch
	// 2. User has a worktree for "feat2" and runs ghenga rebase from there
	// 3. Rebase operations modify the main repo's HEAD (detached)
	// 4. After rebase, main repo should be restored to "master"
	//
	// Previously, the code only saved the worktree's branch (feat2) as
	// "originalBranch" and never saved/restored the main repo's branch.
	tempDir, repo := setupTestRepo(t)
	defer os.RemoveAll(tempDir)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create tower: main (base) -> feat1 -> feat2
	createTestBranch(t, repo, "feat1", 2)
	createTestBranch(t, repo, "feat2", 2)

	// Go back to main in the main repo
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	// Create worktree for feat2
	wt2Dir, err := filepath.Abs(filepath.Join(tempDir, "..", "wt-feat2-restore-main-"+filepath.Base(tempDir)))
	require.NoError(t, err)

	cmd := exec.Command("git", "worktree", "add", wt2Dir, "feat2")
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree for feat2: %s", string(output))
	defer os.RemoveAll(wt2Dir)

	// Add diverging commit to feat1 (from main repo since feat1 is not in worktree)
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("feat1")})
	require.NoError(t, err)
	addSingleCommit(t, tempDir, wt, "diverge.txt", "diverge", "Divergent commit on feat1")

	// Go back to main in the main repo
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	// Verify main repo is on "main" before rebase
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = tempDir
	out, err := cmd.Output()
	require.NoError(t, err)
	require.Equal(t, "main", strings.TrimSpace(string(out)), "Main repo should be on main before rebase")

	// Setup config
	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)

	configTempDir, _ := os.MkdirTemp("", "ghenga-test-restore-main-branch-config")
	defer os.RemoveAll(configTempDir)

	configFile := filepath.Join(configTempDir, "config.toml")
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()
	ConfigPath = mockedConfigPath(configFile)

	towers := []*Tower{{
		Name:     "test-tower",
		Branches: []Branch{{Name: "feat1"}, {Name: "feat2"}},
	}}
	config := createTestConfig(t, tempDir, "test-tower", towers, "main")
	SaveConfig(config)

	// *** KEY: Run rebase from the worktree directory, not the main repo ***
	os.Chdir(wt2Dir)

	// Run rebase (provide "y" for rebase confirm)
	restoreStdin := mockInput("y")
	defer restoreStdin()

	rebaseCmd := &RebaseDoCmd{}
	err = rebaseCmd.Run(nil)
	require.NoError(t, err, "Rebase should succeed")

	// Verify main repo is still on "main" after rebase (not detached HEAD)
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = tempDir
	out, err = cmd.Output()
	require.NoError(t, err)
	mainRepoBranch := strings.TrimSpace(string(out))
	assert.Equal(t, "main", mainRepoBranch,
		"Main repo should be back on 'main' after rebase, but got '%s' (likely detached HEAD)", mainRepoBranch)
}

// TestRebaseConflictPauseContinueWithWorktree covers the pause/continue path
// with a rebased branch that lives in a worktree. Existing worktree tests use
// the empty-commit/--skip scenario; existing conflict tests never involve a
// worktree. This one:
//   - pauses on a real conflict while rebasing feat2 (which is in a worktree),
//   - asserts that MainRepoBranch and RebasedBranches are persisted in state,
//   - resolves the conflict in the main repo,
//   - runs continue and confirms the worktree reset prompt fires (via the
//     continue path's filterWorktreesByNames + state.RebasedBranches),
//   - confirms the main repo is restored to its original branch.
func TestRebaseConflictPauseContinueWithWorktree(t *testing.T) {
	tempDir, repo := setupTestRepo(t)
	defer os.RemoveAll(tempDir)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Shared baseline commit that both branches will later modify (source of conflict).
	baseCommit := addSingleCommit(t, tempDir, wt, "shared.txt", "Line 1\nLine 2\nLine 3\n", "Base shared")

	// Create feat1 at the baseline.
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   baseCommit,
		Branch: plumbing.NewBranchReferenceName("feat1"),
		Create: true,
	})
	require.NoError(t, err)

	// feat2 forks from feat1's tip and modifies shared.txt.
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feat2"),
		Create: true,
	})
	require.NoError(t, err)
	addSingleCommit(t, tempDir, wt, "shared.txt", "Line 1\nLine 2 FROM FEAT2\nLine 3\n", "feat2 change")

	// Back on feat1, add a conflicting modification to the same line so that
	// cherry-picking feat2 onto the new feat1 tip will conflict.
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("feat1")})
	require.NoError(t, err)
	addSingleCommit(t, tempDir, wt, "shared.txt", "Line 1\nLine 2 FROM FEAT1\nLine 3\n", "feat1 diverge")

	// Back to main before creating the worktree (feat2 must not be checked out here).
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	wt2Dir, err := filepath.Abs(filepath.Join(tempDir, "..", "wt-feat2-conflict-"+filepath.Base(tempDir)))
	require.NoError(t, err)
	cmd := exec.Command("git", "worktree", "add", wt2Dir, "feat2")
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree for feat2: %s", string(output))
	defer os.RemoveAll(wt2Dir)

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	configTempDir, _ := os.MkdirTemp("", "ghenga-test-conflict-worktree-config")
	defer os.RemoveAll(configTempDir)
	configFile := filepath.Join(configTempDir, "config.toml")
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()
	ConfigPath = mockedConfigPath(configFile)

	towers := []*Tower{{
		Name:     "test-tower",
		Branches: []Branch{{Name: "feat1"}, {Name: "feat2"}},
	}}
	config := createTestConfig(t, tempDir, "test-tower", towers, "main")
	SaveConfig(config)

	err = os.Chdir(tempDir)
	require.NoError(t, err)

	// Run rebase — should pause on the feat2 cherry-pick conflict.
	restoreStdin := mockInput("y")
	rebaseOutput, err := CaptureOutput(func() error {
		return (&RebaseDoCmd{}).Run(nil)
	})
	restoreStdin()
	require.NoError(t, err, "rebase should return nil on pause")
	require.Contains(t, rebaseOutput, "Cherry-pick failed", "Should have paused on conflict")
	require.True(t, checkHasConflicts(t, tempDir), "Main repo should show conflicts while paused")

	// Paused state should carry both new worktree-aware fields.
	pausedConfig, err := LoadConfig()
	require.NoError(t, err)
	pausedTower := findTowerByName(findRepoByPath(pausedConfig, tempDir), "test-tower")
	require.NotNil(t, pausedTower.RebaseState, "paused RebaseState should exist")
	assert.Equal(t, "main", pausedTower.RebaseState.MainRepoBranch,
		"MainRepoBranch should be saved in paused state")
	assert.Contains(t, pausedTower.RebaseState.RebasedBranches, "feat2",
		"RebasedBranches should include feat2")

	// Resolve in the main repo.
	resolveConflict(t, tempDir, "shared.txt", "Line 1\nLine 2 RESOLVED\nLine 3\n")

	// Continue — answer "y" to the worktree reset prompt so we cover the reset path too.
	restoreStdin = mockInput("y")
	continueOutput, err := CaptureOutput(func() error {
		return (&RebaseContinueCmd{}).Run(nil)
	})
	restoreStdin()
	require.NoError(t, err, "continue should succeed")
	t.Logf("Continue output:\n%s", continueOutput)

	assert.Contains(t, continueOutput, "rebase completed successfully",
		"continue should report success")
	assert.Contains(t, continueOutput, "Worktrees need syncing",
		"continue should offer reset prompt for rebased worktree branch")
	assert.Contains(t, continueOutput, "feat2",
		"reset prompt should mention feat2")
	assert.Contains(t, continueOutput, "Successfully reset",
		"reset should execute when user answers y")

	// State cleared.
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	finalTower := findTowerByName(findRepoByPath(finalConfig, tempDir), "test-tower")
	require.NotNil(t, finalTower)
	assert.Nil(t, finalTower.RebaseState, "RebaseState should be cleared after continue")

	// Main repo restored to its original branch (not left detached).
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = tempDir
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "main", strings.TrimSpace(string(out)),
		"Main repo should be back on 'main' after continue")
}

// TestRebaseCancelAfterPauseRestoresState covers RebaseCancelCmd, which had no
// test coverage at all before this PR's worktree changes. It verifies that
// cancel after a paused rebase clears the state, restores the MainRepoBranch,
// and deletes the temporary cherry-pick branch.
func TestRebaseCancelAfterPauseRestoresState(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()
	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Single-branch conflict setup (mirrors TestRebaseConflictSingleBranch).
	baseC1 := addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2\nLine 3\n", "Base C1")
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   baseC1,
		Branch: plumbing.NewBranchReferenceName("base"),
		Create: true,
	})
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{Hash: baseC1})
	require.NoError(t, err)
	featC1 := addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2 FEAT\nLine 3\n", "Feature C1")
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   featC1,
		Branch: plumbing.NewBranchReferenceName("feature1"),
		Create: true,
	})
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base")})
	require.NoError(t, err)
	addSingleCommit(t, repoPath, wt, "file.txt", "Line 1\nLine 2 BASE\nLine 3\n", "Base C2")

	towerName := "cancel-tower"
	towers := []*Tower{{
		Name:     towerName,
		Base:     "base",
		Branches: []Branch{{Name: "base"}, {Name: "feature1"}},
	}}
	config := createTestConfig(t, repoPath, towerName, towers, "base")
	require.NoError(t, SaveConfig(config))

	// Trigger pause.
	restoreStdin := mockInput("y")
	err = (&RebaseDoCmd{}).Run(nil)
	restoreStdin()
	require.NoError(t, err, "rebase should return nil on pause")

	// Capture paused state details for post-cancel assertions.
	pausedConfig, err := LoadConfig()
	require.NoError(t, err)
	pausedTower := findTowerByName(findRepoByPath(pausedConfig, repoPath), towerName)
	require.NotNil(t, pausedTower.RebaseState)
	require.True(t, pausedTower.RebaseState.IsInProgress)
	tempBranchName := pausedTower.RebaseState.TemporaryBranch
	require.NotEmpty(t, tempBranchName)
	assert.Equal(t, "base", pausedTower.RebaseState.MainRepoBranch,
		"MainRepoBranch should be 'base' in paused state")

	// Cancel.
	cancelOutput, err := CaptureOutput(func() error {
		return (&RebaseCancelCmd{}).Run(nil)
	})
	require.NoError(t, err)
	t.Logf("Cancel output:\n%s", cancelOutput)
	assert.Contains(t, cancelOutput, "Rebase operation cancelled")

	// State cleared.
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	finalTower := findTowerByName(findRepoByPath(finalConfig, repoPath), towerName)
	require.NotNil(t, finalTower)
	assert.Nil(t, finalTower.RebaseState, "RebaseState should be cleared after cancel")

	// Main repo returned to 'base' via MainRepoBranch restoration.
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	require.NoError(t, err)
	assert.Equal(t, "base", strings.TrimSpace(string(out)),
		"Main repo should be back on 'base' after cancel")

	// Temp branch deleted.
	verifyCmd := exec.Command("git", "rev-parse", "--verify", tempBranchName)
	verifyCmd.Dir = repoPath
	assert.Error(t, verifyCmd.Run(),
		"Temporary branch %q should have been deleted by cancel", tempBranchName)
}
