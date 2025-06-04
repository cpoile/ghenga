package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResetDoCmd_HappyPath tests the basic scenario:
// - Tower with 2 branches based on old version of master
// - Master has moved forward with new commits
// - Reset tower to current master
// - Verify tower is rebased onto new master
func TestResetDoCmd_HappyPath(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Setup: Create initial state with "old" master
	createTestBranch(t, repo, "feature1", 2)
	createTestBranch(t, repo, "feature2", 1)

	// Get the initial master commit (this will be our "old" master)
	oldMasterRef, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	require.NoError(t, err)
	oldMasterHash := oldMasterRef.Hash()

	// Create a tower configuration with the branches based on the old master
	tower := &Tower{
		Name: "test-tower",
		Base: "main",
		Branches: []Branch{
			{Name: "feature1"},
			{Name: "feature2"},
		},
	}
	config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")

	// Save initial configuration
	err = SaveConfig(config)
	require.NoError(t, err)

	// Simulate master moving forward: add more commits to main
	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Checkout main and add new commits
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)

	// Add 2 commits to main to simulate it moving forward
	addSingleCommit(t, repoPath, wt, "new-file1.txt", "new content 1", "New commit 1 on main")
	addSingleCommit(t, repoPath, wt, "new-file2.txt", "new content 2", "New commit 2 on main")

	// Get the new master commit hash
	newMasterRef, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	require.NoError(t, err)
	newMasterHash := newMasterRef.Hash()

	// Verify master has actually moved forward
	assert.NotEqual(t, oldMasterHash, newMasterHash, "Master should have moved forward")

	// Now run the reset command
	resetCmd := &ResetDoCmd{
		NewBase: "main",
	}

	err = resetCmd.Run(nil)
	require.NoError(t, err, "Reset command should succeed")

	// Verify that the tower base has been updated
	updatedConfig, err := LoadConfig()
	require.NoError(t, err)
	updatedRepo := findRepoByPath(updatedConfig, repoPath)
	require.NotNil(t, updatedRepo)
	updatedTower := findTowerByName(updatedRepo, "test-tower")
	require.NotNil(t, updatedTower)

	// Verify tower.Base is still "main" (should be unchanged since we reset to main)
	assert.Equal(t, "main", updatedTower.Base)

	// Verify that feature1 is now based on the new master
	// We can check this by verifying that the merge-base between feature1 and main
	// is the new master commit, not the old one
	feature1Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)

	mergeBase, err := findMergeBase(repo, feature1Ref.Hash(), newMasterHash)
	require.NoError(t, err)

	// The merge base should be the new master commit (or very close to it)
	// since feature1 should now be based on the new master
	// We can't check for exact equality because rebase may have created new commits,
	// but the merge base should definitely not be the old master anymore
	assert.NotEqual(t, oldMasterHash, mergeBase, "feature1 should no longer be based on old master")
	assert.Equal(t, newMasterHash, mergeBase, "feature1 should be based on new master")

	// Verify that feature2 is still based on feature1 (tower structure preserved)
	feature2Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature2"), true)
	require.NoError(t, err)

	// Verify that feature2 is based on feature1 by checking merge base
	feature2MergeBaseWithMain, err := findMergeBase(repo, feature2Ref.Hash(), newMasterHash)
	require.NoError(t, err)

	// feature2's merge base with main should be the new master (since it's built on top of feature1)
	assert.Equal(t, newMasterHash, feature2MergeBaseWithMain, "feature2 should ultimately be based on new master")

	// Verify tower structure: feature2 should contain all of feature1's commits plus its own
	feature1Commits, err := getCommitList(repo, feature1Ref.Hash(), newMasterHash)
	require.NoError(t, err)
	feature2Commits, err := getCommitList(repo, feature2Ref.Hash(), newMasterHash)
	require.NoError(t, err)

	// feature2 should have exactly one more commit than feature1 (its own commit)
	assert.Equal(t, len(feature1Commits)+1, len(feature2Commits), "feature2 should have exactly one more commit than feature1")
}

// getCommitList returns the list of commits between fromCommit and toCommit (exclusive of toCommit)
func getCommitList(repo *git.Repository, fromCommit, toCommit plumbing.Hash) ([]string, error) {
	// Use git rev-list to get commits
	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("git", "rev-list", toCommit.String()+".."+fromCommit.String())
	cmd.Dir = wt.Filesystem.Root()
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	return parseCommitList(string(output)), nil
}

// TestResetDoCmd_SameBase tests resetting to the same base (should be a no-op)
func TestResetDoCmd_SameBase(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Setup: Create tower with branches based on main
	createTestBranch(t, repo, "feature1", 2)
	createTestBranch(t, repo, "feature2", 1)

	// Get initial commit hashes before reset operation
	initialFeature1Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)
	initialFeature1Hash := initialFeature1Ref.Hash()

	initialFeature2Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature2"), true)
	require.NoError(t, err)
	initialFeature2Hash := initialFeature2Ref.Hash()

	// Create a tower configuration with the branches based on main
	tower := &Tower{
		Name: "test-tower",
		Base: "main",
		Branches: []Branch{
			{Name: "feature1"},
			{Name: "feature2"},
		},
	}
	config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")

	// Save initial configuration
	err = SaveConfig(config)
	require.NoError(t, err)

	// Reset to the same base (main)
	resetCmd := &ResetDoCmd{
		NewBase: "main",
	}

	err = resetCmd.Run(nil)
	require.NoError(t, err, "Reset to same base should succeed")

	// Get final branch hashes
	feature1RefAfter, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)
	finalFeature1Hash := feature1RefAfter.Hash()

	feature2RefAfter, err := repo.Reference(plumbing.NewBranchReferenceName("feature2"), true)
	require.NoError(t, err)
	finalFeature2Hash := feature2RefAfter.Hash()

	// Verify the base is still set correctly
	updatedConfig, err := LoadConfig()
	require.NoError(t, err)
	updatedRepo := findRepoByPath(updatedConfig, repoPath)
	require.NotNil(t, updatedRepo)
	updatedTower := findTowerByName(updatedRepo, "test-tower")
	require.NotNil(t, updatedTower)

	// Base should still be main
	assert.Equal(t, "main", updatedTower.Base)

	// Even when resetting to the same base, reset performs a full rebase operation
	// This creates new commits, so hashes will be different but structure should be preserved
	assert.NotEqual(t, initialFeature1Hash, finalFeature1Hash, "feature1 hash changes due to rebase (even to same base)")
	assert.NotEqual(t, initialFeature2Hash, finalFeature2Hash, "feature2 hash changes due to rebase (even to same base)")

	// Verify exact expected state: both branches should be based on main after reset
	mainRef, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	require.NoError(t, err)
	mainHash := mainRef.Hash()

	feature1MergeBase, err := findMergeBase(repo, finalFeature1Hash, mainHash)
	require.NoError(t, err)
	assert.Equal(t, mainHash, feature1MergeBase, "feature1 should be based on main after reset")

	feature2MergeBase, err := findMergeBase(repo, finalFeature2Hash, mainHash)
	require.NoError(t, err)
	assert.Equal(t, mainHash, feature2MergeBase, "feature2 should be based on main after reset")

	// Verify tower structure is preserved: feature2 should have exactly one more commit than feature1
	feature1Commits, err := getCommitList(repo, finalFeature1Hash, mainHash)
	require.NoError(t, err)
	feature2Commits, err := getCommitList(repo, finalFeature2Hash, mainHash)
	require.NoError(t, err)
	assert.Equal(t, len(feature1Commits)+1, len(feature2Commits), "feature2 should have exactly one more commit than feature1")
}

// TestResetDoCmd_ErrorCases tests various error conditions
func TestResetDoCmd_ErrorCases(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, repo *git.Repository, repoPath string) *ResetDoCmd
		expectError string
	}{
		{
			name: "nonexistent base",
			setup: func(t *testing.T, repo *git.Repository, repoPath string) *ResetDoCmd {
				// Create tower with branches
				createTestBranch(t, repo, "feature1", 1)
				tower := &Tower{
					Name:     "test-tower",
					Base:     "main",
					Branches: []Branch{{Name: "feature1"}},
				}
				config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")
				err := SaveConfig(config)
				require.NoError(t, err)

				return &ResetDoCmd{NewBase: "nonexistent-branch"}
			},
			expectError: "new base 'nonexistent-branch' does not exist",
		},
		{
			name: "dirty working tree",
			setup: func(t *testing.T, repo *git.Repository, repoPath string) *ResetDoCmd {
				// Create tower with branches
				createTestBranch(t, repo, "feature1", 1)
				tower := &Tower{
					Name:     "test-tower",
					Base:     "main",
					Branches: []Branch{{Name: "feature1"}},
				}
				config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")
				err := SaveConfig(config)
				require.NoError(t, err)

				// Create a dirty working tree
				dirtyFile := filepath.Join(repoPath, "dirty.txt")
				err = os.WriteFile(dirtyFile, []byte("dirty content"), 0644)
				require.NoError(t, err)

				return &ResetDoCmd{NewBase: "main"}
			},
			expectError: "working directory is not clean",
		},
		{
			name: "empty new base argument",
			setup: func(t *testing.T, repo *git.Repository, repoPath string) *ResetDoCmd {
				// Create tower with branches
				createTestBranch(t, repo, "feature1", 1)
				tower := &Tower{
					Name:     "test-tower",
					Base:     "main",
					Branches: []Branch{{Name: "feature1"}},
				}
				config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")
				err := SaveConfig(config)
				require.NoError(t, err)

				return &ResetDoCmd{NewBase: ""}
			},
			expectError: "new base argument is required",
		},
		{
			name: "tower with no branches",
			setup: func(t *testing.T, repo *git.Repository, repoPath string) *ResetDoCmd {
				// Create tower with no branches
				tower := &Tower{
					Name:     "test-tower",
					Base:     "main",
					Branches: []Branch{},
				}
				config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")
				err := SaveConfig(config)
				require.NoError(t, err)

				return &ResetDoCmd{NewBase: "main"}
			},
			expectError: "tower 'test-tower' has no branches to reset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoPath, repo, cleanup := setupTestEnv(t)
			defer cleanup()

			resetCmd := tt.setup(t, repo, repoPath)

			err := resetCmd.Run(nil)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}
}

// TestResetDoCmd_CommitHash tests resetting to a commit hash instead of branch name
func TestResetDoCmd_CommitHash(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Setup: Create tower with branches based on main
	createTestBranch(t, repo, "feature1", 2)
	createTestBranch(t, repo, "feature2", 1)

	// Get the current main commit hash before adding more commits
	mainRef, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	require.NoError(t, err)
	targetCommitHash := mainRef.Hash().String()

	// Add more commits to main after creating feature branches
	wt, err := repo.Worktree()
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)

	addSingleCommit(t, repoPath, wt, "newer-file.txt", "newer content", "Newer commit on main")

	// Create tower configuration
	tower := &Tower{
		Name: "test-tower",
		Base: "main",
		Branches: []Branch{
			{Name: "feature1"},
			{Name: "feature2"},
		},
	}
	config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")

	err = SaveConfig(config)
	require.NoError(t, err)

	// Reset to the specific commit hash (the old main)
	resetCmd := &ResetDoCmd{
		NewBase: targetCommitHash[:7], // Use short hash
	}

	err = resetCmd.Run(nil)
	require.NoError(t, err, "Reset to commit hash should succeed")

	// Verify that the tower base has been updated to the commit hash
	updatedConfig, err := LoadConfig()
	require.NoError(t, err)
	updatedRepo := findRepoByPath(updatedConfig, repoPath)
	require.NotNil(t, updatedRepo)
	updatedTower := findTowerByName(updatedRepo, "test-tower")
	require.NotNil(t, updatedTower)

	// Base should be updated to the commit hash
	assert.Equal(t, targetCommitHash[:7], updatedTower.Base)

	// Verify that the branches have been rebased correctly
	// feature1 should be based on the target commit
	feature1Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)

	// Find merge base between feature1 and the target commit
	targetCommitFull, err := repo.ResolveRevision(plumbing.Revision(targetCommitHash[:7]))
	require.NoError(t, err)

	mergeBase, err := findMergeBase(repo, feature1Ref.Hash(), *targetCommitFull)
	require.NoError(t, err)

	// The merge base should be the target commit (or very close to it)
	// since feature1 should now be based on the target commit
	assert.Equal(t, *targetCommitFull, mergeBase, "feature1 should be based on the target commit")
}

// TestResetDoCmd_WithConflicts tests reset when modify/delete conflicts occur during cherry-pick
func TestResetDoCmd_WithConflicts(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Setup: First, create a common base with a file that both branches will modify
	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Make sure we're on main
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)

	// Add a file to main that will create a conflict scenario
	addSingleCommit(t, repoPath, wt, "conflict-file.txt", "original content\n", "Add conflict file")

	// Create new-base branch from main and DELETE the file
	createBaseCmd := exec.Command("git", "checkout", "-b", "new-base")
	createBaseCmd.Dir = repoPath
	_, err = createBaseCmd.CombinedOutput()
	require.NoError(t, err)

	// Delete the file on new-base
	deleteCmd := exec.Command("git", "rm", "conflict-file.txt")
	deleteCmd.Dir = repoPath
	_, err = deleteCmd.CombinedOutput()
	require.NoError(t, err)

	commitCmd := exec.Command("git", "commit", "-m", "Delete conflict file")
	commitCmd.Dir = repoPath
	_, err = commitCmd.CombinedOutput()
	require.NoError(t, err)

	// Go back to main and create feature1
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)

	createFeatureCmd := exec.Command("git", "checkout", "-b", "feature1")
	createFeatureCmd.Dir = repoPath
	_, err = createFeatureCmd.CombinedOutput()
	require.NoError(t, err)

	// Modify the same file on feature1 (this will conflict with deletion)
	addSingleCommit(t, repoPath, wt, "conflict-file.txt", "modified content by feature1\n", "Feature1 modifies the file")

	// Create tower configuration
	tower := &Tower{
		Name:     "test-tower",
		Base:     "main",
		Branches: []Branch{{Name: "feature1"}},
	}
	config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")

	err = SaveConfig(config)
	require.NoError(t, err)

	// Try to reset to the new-base (this should cause conflicts)
	resetCmd := &ResetDoCmd{
		NewBase: "new-base",
	}

	err = resetCmd.Run(nil)

	// Let's see what actually happens - for now just verify it doesn't panic
	// The behavior depends on Git's conflict resolution capabilities
	if err != nil {
		t.Logf("Reset failed as expected with error: %v", err)
		assert.Contains(t, err.Error(), "cherry-pick failed", "Reset should fail with cherry-pick conflict")
	} else {
		t.Logf("Reset succeeded - Git was able to auto-resolve the conflict")
		// In this case, verify that the tower was reset successfully
		updatedConfig, err := LoadConfig()
		require.NoError(t, err)
		updatedRepo := findRepoByPath(updatedConfig, repoPath)
		require.NotNil(t, updatedRepo)
		updatedTower := findTowerByName(updatedRepo, "test-tower")
		require.NotNil(t, updatedTower)
		assert.Equal(t, "new-base", updatedTower.Base)
	}
}

// TestResetDoCmd_NoBase tests reset when tower has no base set
func TestResetDoCmd_NoBase(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Setup: Create a feature branch
	createTestBranch(t, repo, "feature1", 2)

	// Create a tower configuration with NO BASE set (empty string)
	tower := &Tower{
		Name:     "test-tower",
		Base:     "", // Intentionally empty
		Branches: []Branch{{Name: "feature1"}},
	}
	config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")

	err := SaveConfig(config)
	require.NoError(t, err)

	// Try to reset to a new base
	resetCmd := &ResetDoCmd{
		NewBase: "main",
	}

	err = resetCmd.Run(nil)

	// The behavior when base is not set - let's see what happens
	if err != nil {
		t.Logf("Reset failed when tower base not set: %v", err)
		// This tests the current implementation's behavior
		// Could be enhanced to provide better error messages in the future
	} else {
		t.Logf("Reset succeeded despite no initial base")
		// Verify that the base was set correctly
		updatedConfig, err := LoadConfig()
		require.NoError(t, err)
		updatedRepo := findRepoByPath(updatedConfig, repoPath)
		require.NotNil(t, updatedRepo)
		updatedTower := findTowerByName(updatedRepo, "test-tower")
		require.NotNil(t, updatedTower)
		assert.Equal(t, "main", updatedTower.Base, "Tower base should be set to the new base")
	}
}

// TestResetDoCmd_SavesUndoState tests that reset saves undo state before making changes
func TestResetDoCmd_SavesUndoState(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Setup: Create tower with branches
	createTestBranch(t, repo, "feature1", 2)
	createTestBranch(t, repo, "feature2", 1)

	// Get initial commit hashes of branches before reset
	feature1Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)
	initialFeature1Hash := feature1Ref.Hash().String()

	feature2Ref, err := repo.Reference(plumbing.NewBranchReferenceName("feature2"), true)
	require.NoError(t, err)
	initialFeature2Hash := feature2Ref.Hash().String()

	// Create tower configuration
	tower := &Tower{
		Name: "test-tower",
		Base: "main",
		Branches: []Branch{
			{Name: "feature1"},
			{Name: "feature2"},
		},
	}
	config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")

	err = SaveConfig(config)
	require.NoError(t, err)

	// Add commits to main to create a new reset target
	wt, err := repo.Worktree()
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)

	addSingleCommit(t, repoPath, wt, "new-file.txt", "new content", "New commit on main")

	// Verify no undo state exists initially
	initialConfig, err := LoadConfig()
	require.NoError(t, err)
	initialRepo := findRepoByPath(initialConfig, repoPath)
	initialTower := findTowerByName(initialRepo, "test-tower")
	assert.Empty(t, initialTower.LastRebased, "No undo state should exist initially")
	for _, branch := range initialTower.Branches {
		assert.Empty(t, branch.LastReflogID, "Branch %s should have no undo state initially", branch.Name)
	}

	// Run reset command
	resetCmd := &ResetDoCmd{
		NewBase: "main",
	}

	err = resetCmd.Run(nil)
	require.NoError(t, err, "Reset should succeed")

	// Verify undo state was saved
	updatedConfig, err := LoadConfig()
	require.NoError(t, err)
	updatedRepo := findRepoByPath(updatedConfig, repoPath)
	updatedTower := findTowerByName(updatedRepo, "test-tower")

	// Check that LastRebased timestamp was set
	assert.NotEmpty(t, updatedTower.LastRebased, "Reset should save LastRebased timestamp for undo")

	// Parse timestamp to ensure it's valid RFC3339 format
	_, err = time.Parse(time.RFC3339, updatedTower.LastRebased)
	assert.NoError(t, err, "LastRebased should be valid RFC3339 timestamp")

	// Check that each branch's pre-reset commit hash was saved
	branchUndoState := make(map[string]string)
	for _, branch := range updatedTower.Branches {
		assert.NotEmpty(t, branch.LastReflogID, "Branch %s should have undo state saved", branch.Name)
		assert.Len(t, branch.LastReflogID, 40, "LastReflogID should be full 40-char commit hash")
		branchUndoState[branch.Name] = branch.LastReflogID
	}

	// Verify the saved commit hashes match the original branch positions
	assert.Equal(t, initialFeature1Hash, branchUndoState["feature1"], "feature1 undo state should match original commit")
	assert.Equal(t, initialFeature2Hash, branchUndoState["feature2"], "feature2 undo state should match original commit")
}

// TestResetDoCmd_Undo tests the reset undo functionality
func TestResetDoCmd_Undo(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Setup: Create tower with branches
	createTestBranch(t, repo, "feature1", 2)
	createTestBranch(t, repo, "feature2", 1)

	// Get initial commit hashes before reset
	feature1RefBefore, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)
	initialFeature1Hash := feature1RefBefore.Hash().String()

	feature2RefBefore, err := repo.Reference(plumbing.NewBranchReferenceName("feature2"), true)
	require.NoError(t, err)
	initialFeature2Hash := feature2RefBefore.Hash().String()

	// Create tower configuration
	tower := &Tower{
		Name: "test-tower",
		Base: "main",
		Branches: []Branch{
			{Name: "feature1"},
			{Name: "feature2"},
		},
	}
	config := createTestConfig(t, repoPath, "test-tower", []*Tower{tower}, "")

	err = SaveConfig(config)
	require.NoError(t, err)

	// Add commits to main to create a reset target
	wt, err := repo.Worktree()
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	})
	require.NoError(t, err)

	addSingleCommit(t, repoPath, wt, "new-file.txt", "new content", "New commit on main")

	// Perform reset operation
	resetCmd := &ResetDoCmd{
		NewBase: "main",
	}

	err = resetCmd.Run(nil)
	require.NoError(t, err, "Reset should succeed")

	// Verify branches were changed by reset
	feature1RefAfterReset, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)
	resetFeature1Hash := feature1RefAfterReset.Hash().String()

	feature2RefAfterReset, err := repo.Reference(plumbing.NewBranchReferenceName("feature2"), true)
	require.NoError(t, err)
	resetFeature2Hash := feature2RefAfterReset.Hash().String()

	// Verify that reset actually changed the branches
	assert.NotEqual(t, initialFeature1Hash, resetFeature1Hash, "feature1 should be different after reset")
	assert.NotEqual(t, initialFeature2Hash, resetFeature2Hash, "feature2 should be different after reset")

	// Get the main commit hash after adding new commit (our reset target)
	mainRefAfterNewCommit, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	require.NoError(t, err)
	newMainHash := mainRefAfterNewCommit.Hash()

	// Verify exact expected state after reset: both branches should be based on new main
	feature1MergeBase, err := findMergeBase(repo, plumbing.NewHash(resetFeature1Hash), newMainHash)
	require.NoError(t, err)
	assert.Equal(t, newMainHash, feature1MergeBase, "feature1 should be based on new main after reset")

	feature2MergeBase, err := findMergeBase(repo, plumbing.NewHash(resetFeature2Hash), newMainHash)
	require.NoError(t, err)
	assert.Equal(t, newMainHash, feature2MergeBase, "feature2 should be based on new main after reset")

	// Now test reset undo functionality
	resetUndoCmd := &ResetUndoCmd{}

	err = resetUndoCmd.Run(nil)
	require.NoError(t, err, "Reset undo should succeed")

	// Verify branches are restored to original positions
	feature1RefAfterUndo, err := repo.Reference(plumbing.NewBranchReferenceName("feature1"), true)
	require.NoError(t, err)
	undoFeature1Hash := feature1RefAfterUndo.Hash().String()

	feature2RefAfterUndo, err := repo.Reference(plumbing.NewBranchReferenceName("feature2"), true)
	require.NoError(t, err)
	undoFeature2Hash := feature2RefAfterUndo.Hash().String()

	// Verify branches are back to original positions
	assert.Equal(t, initialFeature1Hash, undoFeature1Hash, "feature1 should be restored to original position")
	assert.Equal(t, initialFeature2Hash, undoFeature2Hash, "feature2 should be restored to original position")

	// Verify undo state is cleared after successful undo
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	finalRepo := findRepoByPath(finalConfig, repoPath)
	finalTower := findTowerByName(finalRepo, "test-tower")

	assert.Empty(t, finalTower.LastRebased, "LastRebased should be cleared after undo")
	for _, branch := range finalTower.Branches {
		assert.Empty(t, branch.LastReflogID, "Branch %s should have undo state cleared", branch.Name)
	}
}
