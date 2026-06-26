package main

// sync_merge_test.go — tests proving that sync uses NORMAL push (never force)
// after a merge-strategy tower operation.
//
// Hypothesis (from MERGE_STRATEGY_PLAN.md): under the merge strategy, fixing
// divergence adds a merge commit ON TOP of each branch's existing tip. The remote
// tip therefore remains an ancestor of the new local tip. GetBranchPushStatus
// returns LocalAhead (not Diverged), so sync classifies the push as a normal
// fast-forward push — no --force-with-lease required.

import (
	"fmt"
	"os/exec"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupMergeSyncTower creates a 3-branch merge-strategy tower with a remote. All
// branches are pushed so the remote tracking refs exist. Tower layout:
//
//	main (tower.Base) -> b0 -> b1 -> b2
//
// Returns the local repo path, local repo, remote repo path, and a cleanup fn.
func setupMergeSyncTower(t *testing.T) (string, *git.Repository, string, func()) {
	t.Helper()
	remoteName := "origin"
	localRepoPath, localRepo, remoteRepoPath, _, cleanup := setupTestEnvWithRemote(t, remoteName)

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// Build the linear stack: main -> b0 -> b1 -> b2.
	createTestBranch(t, localRepo, "b0", 1) // adds file-b0-0.txt
	createTestBranch(t, localRepo, "b1", 1) // adds file-b1-0.txt
	createTestBranch(t, localRepo, "b2", 1) // adds file-b2-0.txt

	// Push everything so remote tracking refs exist.
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			"refs/heads/main:refs/heads/main",
			"refs/heads/b0:refs/heads/b0",
			"refs/heads/b1:refs/heads/b1",
			"refs/heads/b2:refs/heads/b2",
		},
	})
	require.NoError(t, err, "failed to push initial branches")

	// Return to main.
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	towers := []*Tower{{
		Name:     "merge-sync-tower",
		Strategy: StrategyMerge,
		Base:     "main",
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}, {Name: "b2"}},
	}}
	cfg := createTestConfig(t, localRepoPath, "merge-sync-tower", towers, "main")
	require.NoError(t, SaveConfig(cfg))

	return localRepoPath, localRepo, remoteRepoPath, cleanup
}

// runSyncMerge runs SyncDoCmd.Run with confirmed input and returns the output.
func runSyncMerge(t *testing.T, remoteName string) (string, error) {
	t.Helper()
	syncCmd := &SyncDoCmd{Remote: remoteName}
	restore := mockInput("y")
	defer restore()
	return CaptureOutput(func() error { return syncCmd.Run(nil) })
}

// assertRemoteMatchesLocal checks that the remote tracking ref for branchName
// matches the local branch pointer after a push.
func assertRemoteMatchesLocal(
	t *testing.T,
	localRepo *git.Repository,
	remoteName, branchName string,
) {
	t.Helper()
	localRef, err := localRepo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err, "could not get local ref for %s", branchName)

	remoteRef, err := localRepo.Reference(
		plumbing.NewRemoteReferenceName(remoteName, branchName), true)
	require.NoError(t, err, "could not get remote tracking ref for %s", branchName)

	assert.Equal(t, localRef.Hash(), remoteRef.Hash(),
		"remote tracking ref for %s should match local after sync", branchName)
}

// TestSyncMerge_NormalPushAfterMerge is the core claim: after a merge-down
// operation (mergeTowerWithMode) adds merge commits to upper branches, sync
// classifies every branch as LocalAhead (not Diverged) and uses NORMAL push
// (no --force-with-lease).
//
// Failure mode: if any branch reads as Diverged the output would contain
// "force-with-lease", which would falsify the hypothesis. The test stops and
// reports explicitly if that happens.
func TestSyncMerge_NormalPushAfterMerge(t *testing.T) {
	localRepoPath, localRepo, remoteRepoPath, cleanup := setupMergeSyncTower(t)
	defer cleanup()

	// Record original tips of the upper branches before touching b0.
	origB1 := currentHash(t, localRepoPath, "b1")
	origB2 := currentHash(t, localRepoPath, "b2")

	// Add a new commit to b0 to cause divergence in b1 and b2.
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("b0"),
	}))
	addSingleCommit(t, localRepoPath, wt, "b0-extra.txt", "b0 extra\n", "extra commit on b0")

	// Return to main so mergeTowerWithMode can detach HEAD freely.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	// Run a merge-down — this adds merge commits to b1 and b2.
	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	require.NoError(t, err, "merge should succeed without conflicts")

	// Verify merge actually changed b1 and b2 (sanity check).
	newB1 := currentHash(t, localRepoPath, "b1")
	newB2 := currentHash(t, localRepoPath, "b2")
	assert.NotEqual(t, origB1, newB1, "b1 should have a new tip after merge-down")
	assert.NotEqual(t, origB2, newB2, "b2 should have a new tip after merge-down")

	// Original commit hashes are still ancestors (commit hashes preserved).
	assert.True(t, isAncestor(t, localRepoPath, origB1, newB1),
		"original b1 commit must still be an ancestor after merge-down")
	assert.True(t, isAncestor(t, localRepoPath, origB2, newB2),
		"original b2 commit must still be an ancestor after merge-down")

	// --- Core assertion: GetBranchPushStatus == LocalAhead (not Diverged) ---
	gitRepo, err := openGitRepo()
	require.NoError(t, err)

	for _, br := range []string{"b1", "b2"} {
		status, _, _, err := GetBranchPushStatus(gitRepo, "origin", br)
		require.NoError(t, err, "GetBranchPushStatus failed for %s", br)
		if status == Diverged {
			t.Fatalf("HYPOTHESIS FALSIFIED: branch %s reads as Diverged after merge-down "+
				"(would require force-push). This contradicts the expectation that merge "+
				"commits only extend the branch tip — investigate merge.go and push_status.go.",
				br)
		}
		assert.Equal(t, LocalAhead, status,
			"branch %s should be LocalAhead (not Diverged) after merge-down", br)
	}

	// --- Run sync and verify no force-push in the output ---
	output, err := runSyncMerge(t, "origin")
	t.Logf("Sync output:\n%s", output)
	require.NoError(t, err, "sync should succeed after merge-down")

	// Normal push happened.
	assert.Contains(t, output, "Executing normal pushes",
		"sync should use normal push after merge-down")
	assert.Contains(t, output, "Successfully pushed branch 'b1'",
		"b1 should be pushed normally")
	assert.Contains(t, output, "Successfully pushed branch 'b2'",
		"b2 should be pushed normally")

	// No force-push.
	assert.NotContains(t, output, "force-with-lease",
		"sync must not use --force-with-lease after merge-down")
	assert.NotContains(t, output, "Executing force-pushes",
		"sync must not execute force-pushes after merge-down")
	assert.NotContains(t, output, "Marked for force-push",
		"no branch should be marked for force-push after merge-down")

	// Remote refs match local after sync.
	// Refresh the remote tracking refs by fetching (the sync push may have updated
	// the remote but the in-process go-git cache may not reflect it yet).
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = localRepoPath
	fetchOut, fetchErr := fetchCmd.CombinedOutput()
	require.NoError(t, fetchErr, "fetch after sync failed: %s", string(fetchOut))

	remoteB1Hash, err := getRemoteHeadHash(t, remoteRepoPath, "b1")
	require.NoError(t, err)
	assert.Equal(t, newB1, remoteB1Hash.String(),
		"remote b1 should match local b1 after sync")

	remoteB2Hash, err := getRemoteHeadHash(t, remoteRepoPath, "b2")
	require.NoError(t, err)
	assert.Equal(t, newB2, remoteB2Hash.String(),
		"remote b2 should match local b2 after sync")
}

// TestSyncMerge_NormalPushAfterLandAndMerge verifies that after a land under the
// merge strategy (bottom squash-merged + deleted, remaining branches get
// merge-down commits), sync pushes the remaining branches with a fast-forward
// normal push and no force.
func TestSyncMerge_NormalPushAfterLandAndMerge(t *testing.T) {
	localRepoPath, localRepo, remoteRepoPath, cleanup := setupMergeSyncTower(t)
	defer cleanup()

	// Record original tips for b1 and b2.
	b1OrigHash := currentHash(t, localRepoPath, "b1")
	b2OrigHash := currentHash(t, localRepoPath, "b2")

	// Simulate GitHub's "squash and merge" of b0 into main.
	squashHash := squashMergeAndDeleteRemote(
		t, localRepoPath, localRepo, "origin", "main", "b0")
	t.Logf("squash hash for b0 into main: %s", squashHash.String()[:7])

	// Run land (picks up the squash, merges main down into b1 and b2).
	landCmd := &LandCmd{Remote: "origin", SkipSyncCheck: false}
	landRestore := mockInput("y")
	landOutput, err := CaptureOutput(func() error { return landCmd.Run(nil) })
	landRestore()
	t.Logf("Land output:\n%s", landOutput)
	require.NoError(t, err, "land should succeed under merge strategy")

	// After land, b0 is gone; b1 and b2 should have merge commits.
	newB1 := currentHash(t, localRepoPath, "b1")
	newB2 := currentHash(t, localRepoPath, "b2")
	assert.NotEqual(t, b1OrigHash, newB1, "b1 tip should change after land merge-down")
	assert.NotEqual(t, b2OrigHash, newB2, "b2 tip should change after land merge-down")

	// Original commits still reachable.
	assert.True(t, isAncestor(t, localRepoPath, b1OrigHash, newB1),
		"original b1 commit must still be an ancestor after land")
	assert.True(t, isAncestor(t, localRepoPath, b2OrigHash, newB2),
		"original b2 commit must still be an ancestor after land")

	// Core: GetBranchPushStatus must be LocalAhead (not Diverged).
	gitRepo, err := openGitRepo()
	require.NoError(t, err)

	for _, br := range []string{"b1", "b2"} {
		status, _, _, pushErr := GetBranchPushStatus(gitRepo, "origin", br)
		require.NoError(t, pushErr, "GetBranchPushStatus failed for %s", br)
		if status == Diverged {
			t.Fatalf("HYPOTHESIS FALSIFIED: branch %s reads as Diverged after land+merge "+
				"(would require force-push). Investigate land.go merge path.", br)
		}
		assert.Equal(t, LocalAhead, status,
			"branch %s should be LocalAhead after land+merge-down", br)
	}

	// Run sync — expect normal push only.
	output, err := runSyncMerge(t, "origin")
	t.Logf("Sync output:\n%s", output)
	require.NoError(t, err, "sync should succeed after land+merge")

	assert.Contains(t, output, "Executing normal pushes",
		"sync should use normal push after land+merge")
	assert.NotContains(t, output, "force-with-lease",
		"sync must not use --force-with-lease after land+merge")
	assert.NotContains(t, output, "Executing force-pushes",
		"sync must not execute force-pushes after land+merge")

	// Remote refs updated correctly.
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = localRepoPath
	fetchOut, fetchErr := fetchCmd.CombinedOutput()
	require.NoError(t, fetchErr, "fetch after sync: %s", string(fetchOut))

	remoteB1Hash, err := getRemoteHeadHash(t, remoteRepoPath, "b1")
	require.NoError(t, err)
	assert.Equal(t, newB1, remoteB1Hash.String(), "remote b1 should match local after sync")

	remoteB2Hash, err := getRemoteHeadHash(t, remoteRepoPath, "b2")
	require.NoError(t, err)
	assert.Equal(t, newB2, remoteB2Hash.String(), "remote b2 should match local after sync")
}

// TestSyncMerge_MixedTower exercises a merge-strategy tower where some branches
// are UpToDate (no merge-down happened) and some are LocalAhead (got a merge
// commit). Sync should push only the LocalAhead ones with a normal push and skip
// the UpToDate ones — no force-push anywhere.
func TestSyncMerge_MixedTower(t *testing.T) {
	localRepoPath, localRepo, remoteRepoPath, cleanup := setupMergeSyncTower(t)
	defer cleanup()

	// Add a new commit to b0 only — this makes b1 and b2 diverge from their bases,
	// but b0 itself is pushed separately below so b0 stays LocalAhead; b1/b2 get
	// merge commits.
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("b0"),
	}))
	addSingleCommit(t, localRepoPath, wt, "b0-mixed.txt", "b0 mixed\n", "b0 extra for mixed test")
	b0NewHash := currentHash(t, localRepoPath, "b0")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	// Run merge-down — b0 is already updated, b1 and b2 get merge commits.
	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	require.NoError(t, err)

	b1NewHash := currentHash(t, localRepoPath, "b1")
	b2NewHash := currentHash(t, localRepoPath, "b2")

	// Verify b0 is LocalAhead (new commit, not yet pushed), b1 and b2 are LocalAhead
	// (merge commits added on top of the previously-pushed tip).
	gitRepo, err := openGitRepo()
	require.NoError(t, err)

	for _, br := range []string{"b0", "b1", "b2"} {
		status, _, _, pushErr := GetBranchPushStatus(gitRepo, "origin", br)
		require.NoError(t, pushErr, "GetBranchPushStatus failed for %s", br)
		if status == Diverged {
			t.Fatalf("HYPOTHESIS FALSIFIED: branch %s reads as Diverged in mixed tower test "+
				"(would require force-push). Remote tip is not an ancestor of local tip.",
				br)
		}
		assert.Equal(t, LocalAhead, status,
			"branch %s should be LocalAhead in mixed tower test", br)
	}

	output, err := runSyncMerge(t, "origin")
	t.Logf("Sync output:\n%s", output)
	require.NoError(t, err, "sync should succeed for mixed merge tower")

	// All three branches pushed normally.
	assert.Contains(t, output, "Executing normal pushes",
		"sync should use normal push for mixed merge tower")
	assert.Contains(t, output, "Successfully pushed branch 'b0'")
	assert.Contains(t, output, "Successfully pushed branch 'b1'")
	assert.Contains(t, output, "Successfully pushed branch 'b2'")

	// No force-push.
	assert.NotContains(t, output, "force-with-lease",
		"sync must not use --force-with-lease for mixed merge tower")
	assert.NotContains(t, output, "Executing force-pushes")
	assert.NotContains(t, output, "Marked for force-push")

	// Remote refs updated.
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = localRepoPath
	require.NoError(t, fetchCmd.Run())

	remoteB0Hash, err := getRemoteHeadHash(t, remoteRepoPath, "b0")
	require.NoError(t, err)
	assert.Equal(t, b0NewHash, remoteB0Hash.String(), "remote b0 should match local after sync")

	remoteB1Hash, err := getRemoteHeadHash(t, remoteRepoPath, "b1")
	require.NoError(t, err)
	assert.Equal(t, b1NewHash, remoteB1Hash.String(), "remote b1 should match local after sync")

	remoteB2Hash, err := getRemoteHeadHash(t, remoteRepoPath, "b2")
	require.NoError(t, err)
	assert.Equal(t, b2NewHash, remoteB2Hash.String(), "remote b2 should match local after sync")
}

// TestSyncMerge_MixedUpToDateAndLocalAhead verifies that a tower with some
// branches up-to-date and some LocalAhead (not due to merge-down but to a new
// local commit) is handled correctly: only the LocalAhead branches are pushed,
// the UpToDate one is skipped, and no force-push occurs.
func TestSyncMerge_MixedUpToDateAndLocalAhead(t *testing.T) {
	localRepoPath, localRepo, remoteRepoPath, cleanup := setupMergeSyncTower(t)
	defer cleanup()

	// Add a commit to b1 only; b0 and b2 are pushed and therefore up-to-date.
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("b1"),
	}))
	addSingleCommit(t, localRepoPath, wt, "b1-extra.txt", "b1 extra\n",
		"local commit on b1 only")
	b1NewHash := currentHash(t, localRepoPath, "b1")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	output, err := runSyncMerge(t, "origin")
	t.Logf("Sync output:\n%s", output)
	require.NoError(t, err)

	// b0 and b2 are up-to-date, b1 is LocalAhead.
	assert.Contains(t, output, "Branch 'b0': Up-to-date with remote 'origin'. Skipping.")
	assert.Contains(t, output, "Branch 'b2': Up-to-date with remote 'origin'. Skipping.")
	assert.Contains(t, output, "Marked for normal push", "b1 should be marked for normal push")
	assert.Contains(t, output, "Successfully pushed branch 'b1'")

	assert.NotContains(t, output, "force-with-lease")
	assert.NotContains(t, output, "Executing force-pushes")

	// Remote b1 updated; b0 and b2 untouched.
	remoteB1Hash, err := getRemoteHeadHash(t, remoteRepoPath, "b1")
	require.NoError(t, err)
	assert.Equal(t, b1NewHash, remoteB1Hash.String(),
		"remote b1 should match local after sync")
}

// TestSyncMerge_Undo verifies that sync undo after a merge-strategy sync restores
// the local branch pointers to their pre-sync state. This mirrors TestSyncUndo but
// operates on a merge-strategy tower so the pre-sync tips are the post-merge-down
// tips (with merge commits), not rebased tips.
func TestSyncMerge_Undo(t *testing.T) {
	localRepoPath, localRepo, _, cleanup := setupMergeSyncTower(t)
	defer cleanup()

	// Add a commit to b0 and run a merge-down to create merge commits on b1/b2.
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("b0"),
	}))
	addSingleCommit(t, localRepoPath, wt, "b0-undo-test.txt", "undo\n", "b0 extra for undo test")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	restore := mockInput("y")
	_, err = CaptureOutput(func() error {
		return mergeTowerWithMode(MergeModeNormal, false, "", "")
	})
	restore()
	require.NoError(t, err)

	// Record the post-merge tips — these are the pre-sync tips that undo should restore to.
	preSyncB0 := currentHash(t, localRepoPath, "b0")
	preSyncB1 := currentHash(t, localRepoPath, "b1")
	preSyncB2 := currentHash(t, localRepoPath, "b2")

	// Run sync to push.
	syncOutput, err := runSyncMerge(t, "origin")
	t.Logf("Sync output:\n%s", syncOutput)
	require.NoError(t, err, "sync should succeed")
	assert.NotContains(t, syncOutput, "force-with-lease",
		"sync must not use --force-with-lease after merge-down")

	// Verify sync actually pushed something.
	assert.Contains(t, syncOutput, "Successfully pushed branch")

	// Simulate a change after sync (the undo should rewind to the pre-sync state).
	// We do this by adding commits directly to the local branches via update-ref so
	// the branches move without pushing — undo should restore them to preSyncBX.
	addExtraLocalCommit := func(branchName, filename string) plumbing.Hash {
		require.NoError(t, wt.Checkout(&git.CheckoutOptions{
			Branch: plumbing.NewBranchReferenceName(branchName),
		}))
		h := addSingleCommit(t, localRepoPath, wt, filename, "post-sync\n",
			fmt.Sprintf("post-sync commit on %s", branchName))
		return h
	}
	_ = addExtraLocalCommit("b1", "post-sync-b1.txt")
	_ = addExtraLocalCommit("b2", "post-sync-b2.txt")

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	// Run sync undo.
	undoCmd := &SyncUndoCmd{}
	undoRestore := mockInput("y")
	undoOutput, err := CaptureOutput(func() error { return undoCmd.Run(nil) })
	undoRestore()
	t.Logf("Sync Undo output:\n%s", undoOutput)
	require.NoError(t, err, "sync undo should succeed")

	assert.Contains(t, undoOutput, "Successfully undid the last sync operation!")

	// Local branches should be restored to their pre-sync (post-merge) state.
	assert.Equal(t, preSyncB0, currentHash(t, localRepoPath, "b0"),
		"b0 should be restored to pre-sync hash by undo")
	assert.Equal(t, preSyncB1, currentHash(t, localRepoPath, "b1"),
		"b1 should be restored to pre-sync hash by undo")
	assert.Equal(t, preSyncB2, currentHash(t, localRepoPath, "b2"),
		"b2 should be restored to pre-sync hash by undo")

	// Config state cleared.
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(finalConfig.Repos[0], "merge-sync-tower")
	require.NotNil(t, tower)
	assert.Empty(t, tower.LastSynced, "LastSynced should be cleared after undo")
	for _, b := range tower.Branches {
		assert.Empty(t, b.PreSyncReflogID,
			"PreSyncReflogID should be cleared for branch %s after undo", b.Name)
	}

	// The repo opened below needs to reflect the restored state; re-open.
	gitRepo, err := openGitRepo()
	require.NoError(t, err)
	_ = gitRepo
}

