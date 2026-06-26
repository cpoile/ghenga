package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- MergeOntoCmd tests ---

// TestMergeOntoCmd_HappyPath is the main happy-path test: create a tower based on
// "main", advance main with a new commit, then run merge onto main. Every branch in
// the tower should get a new merge commit that includes the new main commit.
func TestMergeOntoCmd_HappyPath(t *testing.T) {
	repoPath, repo, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Record original branch tips before advancing main.
	origB0 := currentHash(t, repoPath, "b0")
	origB1 := currentHash(t, repoPath, "b1")
	origB2 := currentHash(t, repoPath, "b2")

	// Advance main with an independent commit (no file overlap with tower branches).
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))
	addSingleCommit(t, repoPath, wt, "main-advance.txt", "advance\n", "advance main")
	newMain := currentHash(t, repoPath, "main")

	// Run merge onto main.
	restore := mockInput("y")
	out, err := CaptureOutput(func() error {
		return (&MergeOntoCmd{NewBase: "main"}).Run(&kong.Context{})
	})
	restore()
	require.NoError(t, err)
	assert.Contains(t, out, "completed successfully")

	newB0 := currentHash(t, repoPath, "b0")
	newB1 := currentHash(t, repoPath, "b1")
	newB2 := currentHash(t, repoPath, "b2")

	// Each branch should have a new tip.
	assert.NotEqual(t, origB0, newB0, "b0 should have a new tip")
	assert.NotEqual(t, origB1, newB1, "b1 should have a new tip")
	assert.NotEqual(t, origB2, newB2, "b2 should have a new tip")

	// Original commits must still be reachable (merge preserves commit hashes).
	assert.True(t, isAncestor(t, repoPath, origB0, newB0), "origB0 must still be in history")
	assert.True(t, isAncestor(t, repoPath, origB1, newB1), "origB1 must still be in history")
	assert.True(t, isAncestor(t, repoPath, origB2, newB2), "origB2 must still be in history")

	// The new main commit must be in every branch's history.
	assert.True(t, isAncestor(t, repoPath, newMain, newB0), "newMain must be ancestor of newB0")
	assert.True(t, isAncestor(t, repoPath, newMain, newB1), "newMain must be ancestor of newB1")
	assert.True(t, isAncestor(t, repoPath, newMain, newB2), "newMain must be ancestor of newB2")

	// merge-base relationships: each branch fully includes its updated predecessor.
	assert.Equal(t, newMain, mergeBase(t, repoPath, "b0", "main"),
		"merge-base(b0, main) should equal tip(main)")
	assert.Equal(t, newB0, mergeBase(t, repoPath, "b1", "b0"),
		"merge-base(b1, b0) should equal tip(b0)")
	assert.Equal(t, newB1, mergeBase(t, repoPath, "b2", "b1"),
		"merge-base(b2, b1) should equal tip(b1)")

	// tower.Base must stay "main".
	cfg, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(cfg.Repos[0], "merge-tower")
	require.NotNil(t, tower)
	assert.Equal(t, "main", tower.Base)
	assert.Nil(t, tower.MergeState)
}

// TestMergeOntoCmd_NewBase tests switching the tower to a brand-new base branch
// ("release") that is ahead of the current base, then verifying tower.Base is
// updated in the config.
func TestMergeOntoCmd_NewBase(t *testing.T) {
	repoPath, repo, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create a "release" branch from main with one extra commit (it is ahead of main).
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))
	createCmd := exec.Command("git", "checkout", "-b", "release")
	createCmd.Dir = repoPath
	require.NoError(t, createCmd.Run())
	addSingleCommit(t, repoPath, wt, "release.txt", "release content\n", "release commit")
	releaseHash := currentHash(t, repoPath, "release")

	// Return to main.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	origB0 := currentHash(t, repoPath, "b0")

	// Run merge onto release.
	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return (&MergeOntoCmd{NewBase: "release"}).Run(&kong.Context{})
	})
	restore()
	require.NoError(t, err)

	// tower.Base must be updated to "release".
	cfg, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(cfg.Repos[0], "merge-tower")
	require.NotNil(t, tower)
	assert.Equal(t, "release", tower.Base, "tower.Base should be updated to 'release'")

	newB0 := currentHash(t, repoPath, "b0")
	assert.NotEqual(t, origB0, newB0, "b0 should have a new tip")
	assert.True(t, isAncestor(t, repoPath, origB0, newB0), "origB0 must still be in history")

	// release commit must be in b0's history.
	assert.True(t, isAncestor(t, repoPath, releaseHash, newB0),
		"release tip must be an ancestor of new b0")
}

// TestMergeOntoCmd_CommitHash tests that merge onto works when NewBase is a raw
// commit hash (not a branch name). tower.Base is updated to the hash string.
func TestMergeOntoCmd_CommitHash(t *testing.T) {
	repoPath, repo, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Advance main and capture the hash before it moves further.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))
	addSingleCommit(t, repoPath, wt, "hash-target.txt", "target\n", "target commit")
	targetHash := currentHash(t, repoPath, "main")

	// Add another commit to main so the hash is not the current tip.
	addSingleCommit(t, repoPath, wt, "later.txt", "later\n", "later commit on main")

	origB0 := currentHash(t, repoPath, "b0")

	// Run merge onto the specific commit hash (short form).
	shortHash := targetHash[:7]
	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return (&MergeOntoCmd{NewBase: shortHash}).Run(&kong.Context{})
	})
	restore()
	require.NoError(t, err)

	newB0 := currentHash(t, repoPath, "b0")
	assert.NotEqual(t, origB0, newB0, "b0 should have a new tip")
	assert.True(t, isAncestor(t, repoPath, origB0, newB0), "origB0 must still be in history")

	// tower.Base must be updated to the short hash string.
	cfg, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(cfg.Repos[0], "merge-tower")
	require.NotNil(t, tower)
	assert.Equal(t, shortHash, tower.Base, "tower.Base should be updated to the commit hash")
}

// TestMergeOntoCmd_ErrorCases is a table-driven test covering error paths for
// merge onto. It mirrors the structure of TestRebaseOntoCmd_ErrorCases.
func TestMergeOntoCmd_ErrorCases(t *testing.T) {
	type testCase struct {
		name                 string
		setup                func(t *testing.T, repo *git.Repository, repoPath string) *MergeOntoCmd
		expectError          string
		assertBaseNotChanged bool // if true, check that tower.Base was NOT corrupted
	}

	tests := []testCase{
		{
			name: "empty new base argument",
			setup: func(t *testing.T, repo *git.Repository, repoPath string) *MergeOntoCmd {
				createTestBranch(t, repo, "b0", 1)
				tower := &Tower{
					Name:     "merge-tower",
					Strategy: StrategyMerge,
					Base:     "main",
					Branches: []Branch{{Name: "b0"}},
				}
				require.NoError(t, SaveConfig(createTestConfig(t, repoPath, "merge-tower", []*Tower{tower}, "main")))
				return &MergeOntoCmd{NewBase: ""}
			},
			expectError: "new base argument is required",
		},
		{
			name: "nonexistent base does not corrupt tower.Base",
			setup: func(t *testing.T, repo *git.Repository, repoPath string) *MergeOntoCmd {
				createTestBranch(t, repo, "b0", 1)
				createTestBranch(t, repo, "b1", 1)
				tower := &Tower{
					Name:     "merge-tower",
					Strategy: StrategyMerge,
					Base:     "main",
					Branches: []Branch{{Name: "b0"}, {Name: "b1"}},
				}
				require.NoError(t, SaveConfig(createTestConfig(t, repoPath, "merge-tower", []*Tower{tower}, "main")))
				return &MergeOntoCmd{NewBase: "nonexistent-branch-xyz"}
			},
			expectError:          "nonexistent-branch-xyz",
			assertBaseNotChanged: true,
		},
		{
			name: "dirty working tree blocks merge onto",
			setup: func(t *testing.T, repo *git.Repository, repoPath string) *MergeOntoCmd {
				createTestBranch(t, repo, "b0", 1)
				createTestBranch(t, repo, "b1", 1)
				tower := &Tower{
					Name:     "merge-tower",
					Strategy: StrategyMerge,
					Base:     "main",
					Branches: []Branch{{Name: "b0"}, {Name: "b1"}},
				}
				require.NoError(t, SaveConfig(createTestConfig(t, repoPath, "merge-tower", []*Tower{tower}, "main")))
				// Leave an uncommitted file.
				require.NoError(t, os.WriteFile(filepath.Join(repoPath, "dirty.txt"), []byte("dirty"), 0644))
				return &MergeOntoCmd{NewBase: "main"}
			},
			expectError: "uncommitted changes detected",
		},
		{
			name: "empty tower is a no-op",
			setup: func(t *testing.T, repo *git.Repository, repoPath string) *MergeOntoCmd {
				tower := &Tower{
					Name:     "merge-tower",
					Strategy: StrategyMerge,
					Base:     "main",
					Branches: []Branch{},
				}
				require.NoError(t, SaveConfig(createTestConfig(t, repoPath, "merge-tower", []*Tower{tower}, "main")))
				return &MergeOntoCmd{NewBase: "main"}
			},
			// Empty tower: engine returns a "no-op" message rather than an error.
			expectError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoPath, repo, cleanup := setupTestEnv(t)
			defer cleanup()

			cmd := tt.setup(t, repo, repoPath)

			var originalBase string
			if tt.assertBaseNotChanged {
				cfg, err := LoadConfig()
				require.NoError(t, err)
				tower := findTowerByName(cfg.Repos[0], "merge-tower")
				require.NotNil(t, tower)
				originalBase = tower.Base
			}

			restore := mockInput("y")
			_, err := CaptureOutput(func() error {
				return cmd.Run(&kong.Context{})
			})
			restore()

			if tt.expectError == "" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectError)
			}

			if tt.assertBaseNotChanged {
				cfg, err := LoadConfig()
				require.NoError(t, err)
				tower := findTowerByName(cfg.Repos[0], "merge-tower")
				require.NotNil(t, tower)
				assert.Equal(t, originalBase, tower.Base,
					"tower.Base must not be corrupted after a failed merge onto")
			}
		})
	}
}

// TestMergeOntoCmd_Conflict verifies that a merge conflict during onto causes the
// operation to pause with MergeState saved and a message pointing to merge continue.
func TestMergeOntoCmd_Conflict(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// b0 has a file that will conflict with the new-base.
	createBranchWithConflict(t, repoPath, wt, "b0", "b0 content")

	// Return to main.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	// Create "new-base" with a conflicting write to the same file.
	createCmd := exec.Command("git", "checkout", "-b", "new-base")
	createCmd.Dir = repoPath
	require.NoError(t, createCmd.Run())
	addSingleCommit(t, repoPath, wt, "conflict.txt",
		"new-base conflict line\nshared\n", "conflict commit on new-base")

	// Return to main.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	towerName := "onto-conflict-tower"
	towers := []*Tower{{
		Name:     towerName,
		Strategy: StrategyMerge,
		Branches: []Branch{{Name: "b0"}},
	}}
	require.NoError(t, SaveConfig(createTestConfig(t, repoPath, towerName, towers, "main")))

	restore := mockInput("y")
	out, err := CaptureOutput(func() error {
		return (&MergeOntoCmd{NewBase: "new-base"}).Run(&kong.Context{})
	})
	restore()

	// MergeOntoCmd swallows errMergePaused and returns nil with a guidance message.
	require.NoError(t, err, "MergeOntoCmd should return nil on conflict (paused)")
	assert.Contains(t, out, "ghenga merge continue",
		"output should mention 'ghenga merge continue'")

	// MergeState must be persisted.
	cfg, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(cfg.Repos[0], towerName)
	require.NotNil(t, tower)
	require.NotNil(t, tower.MergeState, "MergeState must be saved when paused")
	assert.True(t, tower.MergeState.IsInProgress)
}

// TestMergeOntoCmd_Undo verifies that after a successful merge onto, calling
// merge undo restores all branches to their pre-onto hashes.
func TestMergeOntoCmd_Undo(t *testing.T) {
	repoPath, repo, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Advance main.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))
	addSingleCommit(t, repoPath, wt, "undo-advance.txt", "advance\n", "advance main for undo test")

	// Record tips before onto.
	preB0 := currentHash(t, repoPath, "b0")
	preB1 := currentHash(t, repoPath, "b1")
	preB2 := currentHash(t, repoPath, "b2")

	// Run merge onto.
	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return (&MergeOntoCmd{NewBase: "main"}).Run(&kong.Context{})
	})
	restore()
	require.NoError(t, err)

	// Verify onto changed the branches.
	assert.NotEqual(t, preB0, currentHash(t, repoPath, "b0"), "b0 should be different after onto")
	assert.NotEqual(t, preB1, currentHash(t, repoPath, "b1"), "b1 should be different after onto")
	assert.NotEqual(t, preB2, currentHash(t, repoPath, "b2"), "b2 should be different after onto")

	// Run merge undo.
	_, undoErr := CaptureOutput(func() error {
		return (&MergeUndoCmd{}).Run(&kong.Context{})
	})
	require.NoError(t, undoErr, "merge undo should succeed")

	// Branches must be restored.
	assert.Equal(t, preB0, currentHash(t, repoPath, "b0"), "b0 should be restored")
	assert.Equal(t, preB1, currentHash(t, repoPath, "b1"), "b1 should be restored")
	assert.Equal(t, preB2, currentHash(t, repoPath, "b2"), "b2 should be restored")

	// Undo bookkeeping must be cleared.
	cfg, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(cfg.Repos[0], "merge-tower")
	require.NotNil(t, tower)
	assert.Empty(t, tower.LastRebased)
	for _, b := range tower.Branches {
		assert.Empty(t, b.LastReflogID, "LastReflogID should be cleared for %s", b.Name)
	}
}

// --- MergeFromCmd tests ---

// TestMergeFromCmd_Partial verifies that 'merge from <branch>' propagates the
// merge only from <branch> upward and leaves branches below it untouched.
//
// Setup: main -> b0 -> b1 -> b2. Edit b0 so b1 and b2 diverge. Then call
// 'merge from b1' — b1 should be merged but b0 should be left as-is
// (it didn't diverge from main anyway), and branches above b1 (b2) should also
// be updated.
func TestMergeFromCmd_Partial(t *testing.T) {
	repoPath, repo, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Advance b0 so b1 and b2 both diverge from it.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("b0"),
	}))
	addSingleCommit(t, repoPath, wt, "b0-new.txt", "b0 update\n", "advance b0")
	newB0 := currentHash(t, repoPath, "b0")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	origB1 := currentHash(t, repoPath, "b1")
	origB2 := currentHash(t, repoPath, "b2")

	// Run 'merge from b1': should merge b0 into b1, then updated b1 into b2.
	// b0 should remain untouched (merge from starts at b1, not at b0).
	restore := mockInput("y")
	out, err := CaptureOutput(func() error {
		return (&MergeFromCmd{Branch: "b1"}).Run(&kong.Context{})
	})
	restore()
	require.NoError(t, err)
	assert.Contains(t, out, "completed successfully")

	// b0 should be unchanged.
	assert.Equal(t, newB0, currentHash(t, repoPath, "b0"),
		"b0 must not be modified by 'merge from b1'")

	// b1 must have a new merge commit.
	newB1 := currentHash(t, repoPath, "b1")
	assert.NotEqual(t, origB1, newB1, "b1 should have a new tip after merge from")
	assert.True(t, isAncestor(t, repoPath, origB1, newB1), "origB1 must still be in history")
	assert.Equal(t, newB0, mergeBase(t, repoPath, "b1", "b0"),
		"merge-base(b1, b0) should equal tip(b0)")

	// b2 must also have a new merge commit (propagated upward).
	newB2 := currentHash(t, repoPath, "b2")
	assert.NotEqual(t, origB2, newB2, "b2 should have a new tip after merge from")
	assert.True(t, isAncestor(t, repoPath, origB2, newB2), "origB2 must still be in history")
	assert.Equal(t, newB1, mergeBase(t, repoPath, "b2", "b1"),
		"merge-base(b2, b1) should equal tip(b1)")
}
