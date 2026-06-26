package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

// setupMergeTestTower creates a 3-branch merge-strategy tower on top of a
// base branch called "main". Tower layout:
//
//	main (tower.Base) -> b0 -> b1 -> b2
//
// Each branch has the given number of commits. Returns the repoPath, repo,
// and the cleanup function.
func setupMergeTestTower(t *testing.T, b0Commits, b1Commits, b2Commits int) (string, *git.Repository, func()) {
	t.Helper()
	repoPath, repo, cleanup := setupTestEnv(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	createTestBranch(t, repo, "b0", b0Commits)

	// b1 starts from tip of b0
	createTestBranch(t, repo, "b1", b1Commits)

	// b2 starts from tip of b1
	createTestBranch(t, repo, "b2", b2Commits)

	// Return to main
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	towerName := "merge-tower"
	towers := []*Tower{{
		Name:     towerName,
		Strategy: StrategyMerge,
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}, {Name: "b2"}},
	}}
	cfg := createTestConfig(t, repoPath, towerName, towers, "main")
	require.NoError(t, SaveConfig(cfg))

	return repoPath, repo, cleanup
}

// currentHash returns the commit hash that a branch currently points to.
func currentHash(t *testing.T, repoPath, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", branch)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	require.NoError(t, err, "rev-parse %s failed", branch)
	return strings.TrimSpace(string(out))
}

// mergeBase runs git merge-base and returns the common ancestor hash.
func mergeBase(t *testing.T, repoPath, ref1, ref2 string) string {
	t.Helper()
	cmd := exec.Command("git", "merge-base", ref1, ref2)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	require.NoError(t, err, "merge-base %s %s failed", ref1, ref2)
	return strings.TrimSpace(string(out))
}

// isAncestor returns true if ancestor is an ancestor of descendant.
func isAncestor(t *testing.T, repoPath, ancestor, descendant string) bool {
	t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = repoPath
	return cmd.Run() == nil
}

// commitCount returns how many commits are reachable from ref (exclusive from the common
// ancestor with base), i.e. the number of unique commits on ref after branching from base.
func commitsBetween(t *testing.T, repoPath, base, tip string) []string {
	t.Helper()
	cmd := exec.Command("git", "rev-list", "--reverse", base+".."+tip)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	require.NoError(t, err, "rev-list %s..%s failed", base, tip)
	return parseCommitList(string(out))
}

// createBranchWithConflict creates the named branch at the current HEAD (if it
// does not already exist) and adds a commit that modifies "conflict.txt" on the
// first line in a branch-specific way, guaranteeing a merge conflict with any
// other branch that also writes to that line.
func createBranchWithConflict(t *testing.T, repoPath string, wt *git.Worktree, branchName, content string) {
	t.Helper()
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branchName),
		Create: true,
	}))
	addSingleCommit(t, repoPath, wt, "conflict.txt",
		fmt.Sprintf("%s line\nshared\n", content),
		fmt.Sprintf("conflict commit on %s", branchName))
}

// switchBranchAndAddConflict checks out an existing branch and appends a
// conflicting modification to "conflict.txt". Use createBranchWithConflict for
// the first visit; use this helper when the branch already exists.
func switchBranchAndAddConflict(t *testing.T, repoPath string, wt *git.Worktree, branchName, content string) {
	t.Helper()
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branchName),
	}))
	addSingleCommit(t, repoPath, wt, "conflict.txt",
		fmt.Sprintf("%s line\nshared\n", content),
		fmt.Sprintf("conflict commit on %s", branchName))
}

// --- Tests ---

// TestMerge_LowerBranchEdited_HeadlineCase is the headline test: add a commit
// to b0 (but not to b1/b2), run merge, and verify:
//  1. b1 and b2 each received a merge commit.
//  2. Their original commits are still present in history (commit-hash preservation).
//  3. merge-base(b1, b0) == tip(b0) and merge-base(b2, b1) == tip(b1).
func TestMerge_LowerBranchEdited_HeadlineCase(t *testing.T) {
	repoPath, repo, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Record the original tips of b1 and b2 before anything is added to b0.
	origB1 := currentHash(t, repoPath, "b1")
	origB2 := currentHash(t, repoPath, "b2")

	// Add a new commit on b0, simulating a lower branch being edited.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("b0"),
	}))
	addSingleCommit(t, repoPath, wt, "new-b0.txt", "b0 update\n", "extra commit on b0")
	newB0 := currentHash(t, repoPath, "b0")

	// Return to main so merge can detach HEAD freely.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	// Run merge (skip confirmation).
	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	require.NoError(t, err)

	// --- Assert b1 ---
	newB1 := currentHash(t, repoPath, "b1")
	assert.NotEqual(t, origB1, newB1, "b1 should have a new tip after merge")

	// origB1 must still be reachable from newB1 (original commit preserved).
	assert.True(t, isAncestor(t, repoPath, origB1, newB1),
		"original b1 commit must be an ancestor of the new b1 tip")

	// merge-base(b1, b0) must equal tip(b0).
	assert.Equal(t, newB0, mergeBase(t, repoPath, "b1", "b0"),
		"merge-base(b1, b0) should equal tip(b0)")

	// --- Assert b2 ---
	newB2 := currentHash(t, repoPath, "b2")
	assert.NotEqual(t, origB2, newB2, "b2 should have a new tip after merge")

	assert.True(t, isAncestor(t, repoPath, origB2, newB2),
		"original b2 commit must be an ancestor of the new b2 tip")

	// merge-base(b2, b1) must equal tip(b1) (the updated b1).
	assert.Equal(t, newB1, mergeBase(t, repoPath, "b2", "b1"),
		"merge-base(b2, b1) should equal tip(b1)")

	// Verify config state is clean.
	cfg, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(cfg.Repos[0], "merge-tower")
	require.NotNil(t, tower)
	assert.Nil(t, tower.MergeState, "MergeState should be nil after successful merge")

	// Verify repo is on main (original branch restored).
	curBranch, err := getCurrentBranchName(repo)
	require.NoError(t, err)
	assert.Equal(t, "main", curBranch)
}

// TestMerge_MultiBranchPropagation verifies that a 3-branch tower propagates
// the merge upward end-to-end.
func TestMerge_MultiBranchPropagation(t *testing.T) {
	repoPath, repo, cleanup := setupMergeTestTower(t, 2, 2, 2)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("b0"),
	}))
	addSingleCommit(t, repoPath, wt, "propagate.txt", "propagate\n", "extra on b0")
	newB0 := currentHash(t, repoPath, "b0")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	require.NoError(t, err)

	newB1 := currentHash(t, repoPath, "b1")

	assert.Equal(t, newB0, mergeBase(t, repoPath, "b1", "b0"),
		"merge-base(b1, b0) should equal tip(b0)")
	assert.Equal(t, newB1, mergeBase(t, repoPath, "b2", "b1"),
		"merge-base(b2, b1) should equal tip(b1)")
}

// TestMerge_NoDivergence verifies that when no branch has diverged, merge is a
// no-op (branch tips unchanged, no new commits).
func TestMerge_NoDivergence(t *testing.T) {
	repoPath, repo, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	// Capture tips before.
	b0Before := currentHash(t, repoPath, "b0")
	b1Before := currentHash(t, repoPath, "b1")
	b2Before := currentHash(t, repoPath, "b2")

	out, err := CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, true, "", "")
	})
	require.NoError(t, err)
	assert.Contains(t, out, "All branches are up to date")

	// Tips must be unchanged.
	assert.Equal(t, b0Before, currentHash(t, repoPath, "b0"))
	assert.Equal(t, b1Before, currentHash(t, repoPath, "b1"))
	assert.Equal(t, b2Before, currentHash(t, repoPath, "b2"))
}

// TestMerge_SingleBranchTower verifies merge works on a tower with a single branch.
func TestMerge_SingleBranchTower(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// b0 builds on main, then main gets a new commit (divergence).
	createTestBranch(t, repo, "b0", 1)
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))
	addSingleCommit(t, repoPath, wt, "main-new.txt", "main advance\n", "advance main")
	newMain := currentHash(t, repoPath, "main")

	origB0 := currentHash(t, repoPath, "b0")

	towerName := "single-tower"
	towers := []*Tower{{
		Name:     towerName,
		Strategy: StrategyMerge,
		Branches: []Branch{{Name: "b0"}},
	}}
	cfg := createTestConfig(t, repoPath, towerName, towers, "main")
	require.NoError(t, SaveConfig(cfg))

	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	require.NoError(t, err)

	newB0 := currentHash(t, repoPath, "b0")
	assert.NotEqual(t, origB0, newB0, "b0 should have a new tip")
	assert.True(t, isAncestor(t, repoPath, origB0, newB0),
		"original b0 commit should still be in history")
	assert.Equal(t, newMain, mergeBase(t, repoPath, "b0", "main"),
		"merge-base(b0, main) should equal tip(main)")
}

// TestMerge_ConflictOnOneBranch verifies that a merge conflict pauses the
// operation, saves MergeState, and that merge continue resumes successfully.
func TestMerge_ConflictOnOneBranch(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create b0 from main with a commit that modifies conflict.txt.
	createBranchWithConflict(t, repoPath, wt, "b0", "b0 content")
	origB0 := currentHash(t, repoPath, "b0")

	// b1 builds on b0's tip and also modifies conflict.txt (same line — guaranteed conflict).
	createBranchWithConflict(t, repoPath, wt, "b1", "b1 content")
	origB1 := currentHash(t, repoPath, "b1")

	// Now advance b0 with a new conflicting change to trigger a merge conflict in b1.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("b0"),
	}))
	addSingleCommit(t, repoPath, wt, "conflict.txt",
		"b0 updated conflict line\nshared\n",
		"advance b0 to cause conflict in b1")
	newB0 := currentHash(t, repoPath, "b0")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	towerName := "conflict-tower"
	towers := []*Tower{{
		Name:     towerName,
		Strategy: StrategyMerge,
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}},
	}}
	cfg := createTestConfig(t, repoPath, towerName, towers, "main")
	require.NoError(t, SaveConfig(cfg))

	// --- Run merge (expect pause) ---
	restore := mockInput("y")
	mergeOut, mergeErr := CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	// MergeDoCmd swallows errMergePaused; direct call returns it.
	assert.Equal(t, errMergePaused, mergeErr, "merge should return errMergePaused on conflict")
	t.Logf("merge output:\n%s", mergeOut)

	// MergeState must be saved.
	pausedCfg, err := LoadConfig()
	require.NoError(t, err)
	pausedTower := findTowerByName(pausedCfg.Repos[0], towerName)
	require.NotNil(t, pausedTower)
	require.NotNil(t, pausedTower.MergeState, "MergeState should be set after conflict")
	assert.True(t, pausedTower.MergeState.IsInProgress)
	assert.Equal(t, "b1", pausedTower.MergeState.TargetBranch)

	// Original b0 tip must still be intact (b0 was merged successfully before conflict on b1).
	// (b0 had no divergence in this setup; it's b1 that conflicts with the updated b0.)
	_ = origB0
	_ = origB1

	// b0 was not in the merge plan (it had no divergence vs main), so it's unchanged.
	assert.Equal(t, newB0, currentHash(t, repoPath, "b0"), "b0 tip unchanged")

	// Resolve the conflict.
	resolveConflict(t, repoPath, "conflict.txt", "resolved content\nshared\n")

	// --- Run merge continue ---
	continueOut, continueErr := CaptureOutput(func() error {
		return (&MergeContinueCmd{}).Run(&kong.Context{})
	})
	require.NoError(t, continueErr, "merge continue should succeed after resolution")
	t.Logf("continue output:\n%s", continueOut)

	newB1 := currentHash(t, repoPath, "b1")
	assert.True(t, isAncestor(t, repoPath, origB1, newB1),
		"original b1 commits must still be in history")
	assert.Equal(t, newB0, mergeBase(t, repoPath, "b1", "b0"),
		"merge-base(b1, b0) should equal tip(b0) after continue")

	// MergeState must be cleared.
	finalCfg, err := LoadConfig()
	require.NoError(t, err)
	finalTower := findTowerByName(finalCfg.Repos[0], towerName)
	require.NotNil(t, finalTower)
	assert.Nil(t, finalTower.MergeState, "MergeState should be nil after successful continue")

	// Repo should be restored to "main".
	curBranch, err := getCurrentBranchName(repo)
	require.NoError(t, err)
	assert.Equal(t, "main", curBranch)
}

// TestMerge_MultiBranchConflict verifies pause/resolve/continue on multiple branches.
func TestMerge_MultiBranchConflict(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// b0: created from main with a conflict.txt commit.
	createBranchWithConflict(t, repoPath, wt, "b0", "b0 v1")
	// b1: built on b0, modifies conflict.txt too.
	createBranchWithConflict(t, repoPath, wt, "b1", "b1 v1")
	// b2: built on b1, modifies conflict.txt too.
	createBranchWithConflict(t, repoPath, wt, "b2", "b2 v1")

	// Advance b0 so both b1 and b2 will have conflicts when the base is merged in.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("b0")}))
	addSingleCommit(t, repoPath, wt, "conflict.txt", "b0 updated\nshared\n", "advance b0")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}))

	origB1 := currentHash(t, repoPath, "b1")
	origB2 := currentHash(t, repoPath, "b2")
	newB0 := currentHash(t, repoPath, "b0")

	towerName := "multi-conflict-tower"
	towers := []*Tower{{
		Name:     towerName,
		Strategy: StrategyMerge,
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}, {Name: "b2"}},
	}}
	cfg := createTestConfig(t, repoPath, towerName, towers, "main")
	require.NoError(t, SaveConfig(cfg))

	// --- First merge: pause on b1 ---
	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	assert.Equal(t, errMergePaused, err)

	pausedCfg, _ := LoadConfig()
	pausedTower := findTowerByName(pausedCfg.Repos[0], towerName)
	require.NotNil(t, pausedTower.MergeState)
	assert.True(t, pausedTower.MergeState.IsInProgress)
	assert.Equal(t, "b1", pausedTower.MergeState.TargetBranch)

	// Resolve b1 conflict.
	resolveConflict(t, repoPath, "conflict.txt", "b1 resolved\nshared\n")

	// --- Continue: should pause again on b2 ---
	continueOut, continueErr := CaptureOutput(func() error {
		return (&MergeContinueCmd{}).Run(&kong.Context{})
	})
	assert.Equal(t, errMergePaused, continueErr, "should pause again on b2 conflict")
	t.Logf("continue (b2 pause) output:\n%s", continueOut)

	pausedCfg2, _ := LoadConfig()
	pausedTower2 := findTowerByName(pausedCfg2.Repos[0], towerName)
	require.NotNil(t, pausedTower2.MergeState)
	assert.Equal(t, "b2", pausedTower2.MergeState.TargetBranch)

	// Resolve b2 conflict.
	resolveConflict(t, repoPath, "conflict.txt", "b2 resolved\nshared\n")

	// --- Final continue: should succeed ---
	_, finalErr := CaptureOutput(func() error {
		return (&MergeContinueCmd{}).Run(&kong.Context{})
	})
	require.NoError(t, finalErr, "final continue should succeed")

	newB1 := currentHash(t, repoPath, "b1")
	newB2 := currentHash(t, repoPath, "b2")

	assert.True(t, isAncestor(t, repoPath, origB1, newB1), "origB1 must still be in history")
	assert.True(t, isAncestor(t, repoPath, origB2, newB2), "origB2 must still be in history")

	assert.Equal(t, newB0, mergeBase(t, repoPath, "b1", "b0"))
	assert.Equal(t, newB1, mergeBase(t, repoPath, "b2", "b1"))

	finalCfg, _ := LoadConfig()
	finalTower := findTowerByName(finalCfg.Repos[0], towerName)
	assert.Nil(t, finalTower.MergeState)

	curBranch, err := getCurrentBranchName(repo)
	require.NoError(t, err)
	assert.Equal(t, "main", curBranch)
}

// TestMerge_ContinueWithUnresolvedConflicts verifies that merge continue rejects
// the call when conflicts still exist, and the state is preserved unchanged.
func TestMerge_ContinueWithUnresolvedConflicts(t *testing.T) {
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	repo, err := openGitRepo()
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	createBranchWithConflict(t, repoPath, wt, "b0", "b0 v1")
	createBranchWithConflict(t, repoPath, wt, "b1", "b1 v1")
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("b0")}))
	addSingleCommit(t, repoPath, wt, "conflict.txt", "b0 updated\nshared\n", "advance b0")
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}))

	towerName := "noresolve-tower"
	towers := []*Tower{{
		Name:     towerName,
		Strategy: StrategyMerge,
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}},
	}}
	require.NoError(t, SaveConfig(createTestConfig(t, repoPath, towerName, towers, "main")))

	restore := mockInput("y")
	_, _ = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()

	// Capture state before (still conflicting, don't resolve).
	pausedCfg, _ := LoadConfig()
	pausedTower := findTowerByName(pausedCfg.Repos[0], towerName)
	require.NotNil(t, pausedTower.MergeState)
	savedState := *pausedTower.MergeState

	// Run continue without resolving.
	out, err := CaptureOutput(func() error {
		return (&MergeContinueCmd{}).Run(&kong.Context{})
	})
	require.NoError(t, err, "continue should return nil (informational) when conflicts remain")
	assert.Contains(t, out, "Conflicts still detected")

	// State should be unchanged.
	afterCfg, _ := LoadConfig()
	afterTower := findTowerByName(afterCfg.Repos[0], towerName)
	require.NotNil(t, afterTower.MergeState)
	assert.Equal(t, savedState.TargetBranch, afterTower.MergeState.TargetBranch)
	assert.True(t, afterTower.MergeState.IsInProgress)
}

// TestMerge_ContinueAfterManualCommit verifies that if the user committed the
// merge manually, merge continue detects this and proceeds.
func TestMerge_ContinueAfterManualCommit(t *testing.T) {
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	repo, err := openGitRepo()
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	createBranchWithConflict(t, repoPath, wt, "b0", "b0 v1")
	createBranchWithConflict(t, repoPath, wt, "b1", "b1 v1")
	origB1 := currentHash(t, repoPath, "b1")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("b0")}))
	addSingleCommit(t, repoPath, wt, "conflict.txt", "b0 updated\nshared\n", "advance b0")
	newB0 := currentHash(t, repoPath, "b0")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}))

	towerName := "manual-commit-tower"
	towers := []*Tower{{
		Name:     towerName,
		Strategy: StrategyMerge,
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}},
	}}
	require.NoError(t, SaveConfig(createTestConfig(t, repoPath, towerName, towers, "main")))

	restore := mockInput("y")
	_, _ = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()

	// Resolve and commit manually (instead of just staging).
	resolveConflict(t, repoPath, "conflict.txt", "manual resolved\nshared\n")
	commitCmd := exec.Command("git", "commit", "-m", "Manual merge commit")
	commitCmd.Dir = repoPath
	out, err := commitCmd.CombinedOutput()
	require.NoError(t, err, "manual commit failed: %s", string(out))

	// Run continue — should detect manual commit and succeed.
	continueOut, continueErr := CaptureOutput(func() error {
		return (&MergeContinueCmd{}).Run(&kong.Context{})
	})
	require.NoError(t, continueErr, "continue should succeed after manual commit")
	assert.Contains(t, continueOut, "manually", "should mention manual commit detection")

	newB1 := currentHash(t, repoPath, "b1")
	assert.True(t, isAncestor(t, repoPath, origB1, newB1))
	assert.Equal(t, newB0, mergeBase(t, repoPath, "b1", "b0"))

	finalCfg, _ := LoadConfig()
	finalTower := findTowerByName(finalCfg.Repos[0], towerName)
	assert.Nil(t, finalTower.MergeState)
}

// TestMerge_Cancel verifies that merge cancel aborts the merge, clears state,
// and restores the original branch with a clean working tree.
func TestMerge_Cancel(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	createBranchWithConflict(t, repoPath, wt, "b0", "b0 v1")
	createBranchWithConflict(t, repoPath, wt, "b1", "b1 v1")
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("b0")}))
	addSingleCommit(t, repoPath, wt, "conflict.txt", "b0 updated\nshared\n", "advance b0")
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}))

	towerName := "cancel-tower"
	towers := []*Tower{{
		Name:     towerName,
		Strategy: StrategyMerge,
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}},
	}}
	require.NoError(t, SaveConfig(createTestConfig(t, repoPath, towerName, towers, "main")))

	// Trigger pause.
	restore := mockInput("y")
	_, _ = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()

	require.True(t, checkHasConflicts(t, repoPath), "should have conflicts after pause")

	// Cancel.
	_, cancelErr := CaptureOutput(func() error {
		return (&MergeCancelCmd{}).Run(&kong.Context{})
	})
	require.NoError(t, cancelErr)

	// No conflicts, clean working tree.
	require.False(t, checkHasConflicts(t, repoPath), "no conflicts after cancel")
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repoPath
	statusOut, _ := statusCmd.Output()
	assert.Empty(t, strings.TrimSpace(string(statusOut)), "working tree should be clean after cancel")

	// MergeState cleared.
	finalCfg, _ := LoadConfig()
	finalTower := findTowerByName(finalCfg.Repos[0], towerName)
	assert.Nil(t, finalTower.MergeState)

	// Restore to main.
	curBranch, err := getCurrentBranchName(repo)
	require.NoError(t, err)
	assert.Equal(t, "main", curBranch)
}

// TestMerge_Undo verifies that merge undo restores branches to their pre-merge
// hashes. It also covers recreating a deleted branch.
func TestMerge_Undo(t *testing.T) {
	repoPath, repo, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("b0")}))
	addSingleCommit(t, repoPath, wt, "undo-test.txt", "undo\n", "advance b0 for undo test")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}))

	// Record tips before merge.
	preB0 := currentHash(t, repoPath, "b0")
	preB1 := currentHash(t, repoPath, "b1")
	preB2 := currentHash(t, repoPath, "b2")

	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	require.NoError(t, err)

	// Verify merge changed b1 and b2.
	assert.NotEqual(t, preB1, currentHash(t, repoPath, "b1"))
	assert.NotEqual(t, preB2, currentHash(t, repoPath, "b2"))

	// Delete b2 to test recreation.
	deleteCmd := exec.Command("git", "branch", "-D", "b2")
	deleteCmd.Dir = repoPath
	require.NoError(t, deleteCmd.Run())

	// Undo.
	_, undoErr := CaptureOutput(func() error {
		return (&MergeUndoCmd{}).Run(&kong.Context{})
	})
	require.NoError(t, undoErr)

	// Branches restored.
	assert.Equal(t, preB0, currentHash(t, repoPath, "b0"), "b0 should be unchanged")
	assert.Equal(t, preB1, currentHash(t, repoPath, "b1"), "b1 should be restored")
	// b2 was deleted; undo should have recreated it.
	assert.Equal(t, preB2, currentHash(t, repoPath, "b2"), "b2 should be recreated at pre-merge hash")

	// Undo bookkeeping cleared.
	finalCfg, _ := LoadConfig()
	finalTower := findTowerByName(finalCfg.Repos[0], "merge-tower")
	require.NotNil(t, finalTower)
	assert.Empty(t, finalTower.LastRebased)
	for _, b := range finalTower.Branches {
		assert.Empty(t, b.LastReflogID, "LastReflogID should be cleared for %s", b.Name)
	}

	// Repo should be on main.
	curBranch, err := getCurrentBranchName(repo)
	require.NoError(t, err)
	assert.Equal(t, "main", curBranch)
}

// TestMerge_BranchesInWorktrees verifies that merge succeeds via update-ref
// even when branches are checked out in worktrees.
func TestMerge_BranchesInWorktrees(t *testing.T) {
	repoPath, repo, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Advance b0 to trigger divergence in b1.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("b0")}))
	addSingleCommit(t, repoPath, wt, "worktree-base.txt", "advance\n", "advance b0")
	newB0 := currentHash(t, repoPath, "b0")

	// Return to main before creating worktrees.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}))

	// Create a worktree for b1.
	wt1Dir, err := filepath.Abs(filepath.Join(repoPath, "..", "wt-b1-"+filepath.Base(repoPath)))
	require.NoError(t, err)
	addWtCmd := exec.Command("git", "worktree", "add", wt1Dir, "b1")
	addWtCmd.Dir = repoPath
	wtOut, err := addWtCmd.CombinedOutput()
	require.NoError(t, err, "Failed to create worktree: %s", string(wtOut))
	defer os.RemoveAll(wt1Dir)

	origB1 := currentHash(t, repoPath, "b1")
	origB2 := currentHash(t, repoPath, "b2")

	// Run merge — should use update-ref, not git branch -f.
	restore := mockInput("y\n") // "y" for proceed, then "n" for worktree reset
	_, mergeErr := CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	require.NoError(t, mergeErr, "merge should succeed with worktree branches")

	// Branch pointers must be advanced.
	newB1 := currentHash(t, repoPath, "b1")
	newB2 := currentHash(t, repoPath, "b2")
	assert.NotEqual(t, origB1, newB1, "b1 should have new tip")
	assert.NotEqual(t, origB2, newB2, "b2 should have new tip")

	// Original commits still reachable.
	assert.True(t, isAncestor(t, repoPath, origB1, newB1))
	assert.True(t, isAncestor(t, repoPath, origB2, newB2))

	assert.Equal(t, newB0, mergeBase(t, repoPath, "b1", "b0"))

	// Verify repo is restored to main.
	curBranch, err := getCurrentBranchName(repo)
	require.NoError(t, err)
	assert.Equal(t, "main", curBranch)
}

// TestMerge_DirtyWorktreeBlocks verifies that a dirty main worktree (or tower
// worktree) prevents merge from starting and names the dirty path.
func TestMerge_DirtyWorktreeBlocks(t *testing.T) {
	repoPath, _, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	// Write an uncommitted file to the main repo directory.
	dirty := filepath.Join(repoPath, "dirty.txt")
	require.NoError(t, os.WriteFile(dirty, []byte("dirty\n"), 0644))
	defer os.Remove(dirty)

	_, err := CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, true, "", "")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), repoPath, "error should name the dirty directory")
}

// TestMerge_RunFromWorktree verifies that when ghenga is run from a worktree,
// the main repo branch is restored to its pre-merge state afterward.
func TestMerge_RunFromWorktree(t *testing.T) {
	repoPath, _, cleanup := setupMergeTestTower(t, 1, 1, 1)
	defer cleanup()

	repo, err := openGitRepo()
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Add a commit on b0 to cause divergence.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("b0")}))
	addSingleCommit(t, repoPath, wt, "from-worktree.txt", "wt\n", "advance b0")

	// Create a "side" branch to use as the worktree branch (main is already
	// checked out in the main repo, so we cannot create another worktree for it).
	createCmd := exec.Command("git", "checkout", "-b", "side-wt", "main")
	createCmd.Dir = repoPath
	require.NoError(t, createCmd.Run())

	// Go back to main in the main repo so the branch is free to use in a worktree.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}))

	// Create a worktree on the "side-wt" branch.
	wtDir, err := filepath.Abs(filepath.Join(repoPath, "..", "wt-side-"+filepath.Base(repoPath)))
	require.NoError(t, err)
	addWtCmd := exec.Command("git", "worktree", "add", wtDir, "side-wt")
	addWtCmd.Dir = repoPath
	wtOut, err := addWtCmd.CombinedOutput()
	require.NoError(t, err, "create worktree: %s", string(wtOut))
	defer os.RemoveAll(wtDir)

	// Change CWD to the worktree directory to simulate running ghenga from there.
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(wtDir))
	defer os.Chdir(oldWd)

	restore := mockInput("y\n")
	_, mergeErr := CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	require.NoError(t, mergeErr)

	// Chdir back to main repo to check its branch.
	require.NoError(t, os.Chdir(repoPath))
	mainRepoBranch := strings.TrimSpace(func() string {
		c := exec.Command("git", "branch", "--show-current")
		c.Dir = repoPath
		o, _ := c.Output()
		return string(o)
	}())
	assert.Equal(t, "main", mainRepoBranch, "main repo should be on 'main' after merge from worktree")
}

// TestMerge_GuardRebaseOnMergeTower verifies that running 'ghenga merge' on a
// rebase tower is rejected with a helpful message.
func TestMerge_GuardMergeOnRebaseTower(t *testing.T) {
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	towerName := "rebase-tower"
	tower := &Tower{Name: towerName, Base: "main"} // no Strategy set == rebase
	cfg := createTestConfig(t, repoPath, towerName, []*Tower{tower}, "main")
	require.NoError(t, SaveConfig(cfg))

	err := (&MergeDoCmd{}).Run(&kong.Context{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Use 'ghenga rebase'")
}

// TestMerge_MergeResetMode exercises MergeModeReset, which merges a specific
// ref (resetNewBase) into the first branch and chains upward — the behavior
// used by 'land' and 'merge onto'.
func TestMerge_MergeResetMode(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create b0 and b1 with distinct files.
	createTestBranch(t, repo, "b0", 1)
	createTestBranch(t, repo, "b1", 1)
	origB0 := currentHash(t, repoPath, "b0")
	origB1 := currentHash(t, repoPath, "b1")

	// Advance main (the resetNewBase).
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}))
	addSingleCommit(t, repoPath, wt, "main-new.txt", "main advanced\n", "advance main")
	newMain := currentHash(t, repoPath, "main")

	towerName := "reset-tower"
	towers := []*Tower{{
		Name:     towerName,
		Strategy: StrategyMerge,
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}},
	}}
	require.NoError(t, SaveConfig(createTestConfig(t, repoPath, towerName, towers, "main")))

	// Reset mode: merge newMain into b0, then updated b0 into b1.
	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeReset, false, "", newMain)
	})
	restore()
	require.NoError(t, err)

	newB0 := currentHash(t, repoPath, "b0")
	newB1 := currentHash(t, repoPath, "b1")

	// Original commits still present.
	assert.True(t, isAncestor(t, repoPath, origB0, newB0))
	assert.True(t, isAncestor(t, repoPath, origB1, newB1))

	// merge-base(b0, main) should be tip(main).
	assert.Equal(t, newMain, mergeBase(t, repoPath, "b0", "main"))
	// merge-base(b1, b0) should be tip(b0).
	assert.Equal(t, newB0, mergeBase(t, repoPath, "b1", "b0"))
}
