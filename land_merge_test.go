package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

// setupMergeLandTower creates a 3-branch merge-strategy tower with a remote. The
// tower layout is:
//
//	main (tower.Base) -> b0 -> b1 -> b2
//
// All branches are pushed to the remote. Returns repoPath, the local repo,
// remote repo path, and a cleanup function.
func setupMergeLandTower(t *testing.T) (string, *git.Repository, string, func()) {
	t.Helper()
	remoteName := "origin"
	localRepoPath, localRepo, remoteRepoPath, _, cleanup := setupTestEnvWithRemote(t, remoteName)

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	createTestBranch(t, localRepo, "b0", 1) // adds file-b0-0.txt
	createTestBranch(t, localRepo, "b1", 1) // adds file-b1-0.txt
	createTestBranch(t, localRepo, "b2", 1) // adds file-b2-0.txt

	// Push main, b0, b1, b2 to the remote.
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
		Name:     "merge-land-tower",
		Strategy: StrategyMerge,
		Base:     "main",
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}, {Name: "b2"}},
	}}
	cfg := createTestConfig(t, localRepoPath, "merge-land-tower", towers, "main")
	require.NoError(t, SaveConfig(cfg))

	return localRepoPath, localRepo, remoteRepoPath, cleanup
}

// squashMergeAndDeleteRemote simulates GitHub's "squash and merge" of branchName
// into base: creates a squash commit on the remote, then deletes the remote branch.
// Returns the hash of the squash commit. The local base branch is NOT updated here
// (that's what land does).
func squashMergeAndDeleteRemote(
	t *testing.T,
	localRepoPath string,
	localRepo *git.Repository,
	remoteName, baseName, branchName string,
) plumbing.Hash {
	t.Helper()
	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// Read the current tip of the base from the remote.
	baseRefName := plumbing.NewRemoteReferenceName(remoteName, baseName)
	baseRemoteRef, err := getReference(localRepo, baseRefName)
	require.NoError(t, err)

	// Check out the remote base commit detached so we can build the squash commit.
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseRemoteRef.Hash()})
	require.NoError(t, err)

	// The squash commit carries the files that were on branchName. Here we simply
	// copy all files from branchName's tree into the detached working tree.
	branchRef, err := getReference(localRepo, plumbing.NewBranchReferenceName(branchName))
	require.NoError(t, err)
	branchCommit, err := localRepo.CommitObject(branchRef.Hash())
	require.NoError(t, err)
	branchTree, err := branchCommit.Tree()
	require.NoError(t, err)
	err = branchTree.Files().ForEach(func(f *object.File) error {
		content, ferr := f.Contents()
		if ferr != nil {
			return ferr
		}
		fullPath := filepath.Join(localRepoPath, f.Name)
		if mkErr := os.MkdirAll(filepath.Dir(fullPath), 0755); mkErr != nil {
			return mkErr
		}
		return os.WriteFile(fullPath, []byte(content), 0644)
	})
	require.NoError(t, err)

	// Stage all changes and create the squash commit.
	_, err = wt.Add(".")
	require.NoError(t, err)

	// Check if there's actually anything to commit.
	status, err := wt.Status()
	require.NoError(t, err)

	var squashHash plumbing.Hash
	if len(status) == 0 {
		// No changes — the squash is a no-op; use base HEAD as the squash commit.
		squashHash = baseRemoteRef.Hash()
	} else {
		squashHash, err = wt.Commit("Squash merge "+branchName+" into "+baseName, &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@example.com"},
		})
		require.NoError(t, err)
	}

	// Advance origin/base to the squash commit.
	require.NoError(t, localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(squashHash.String() + ":refs/heads/" + baseName)},
		Force:      true,
	}))
	// Delete the remote branch.
	require.NoError(t, localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(":refs/heads/" + branchName)},
	}))

	// Update local base to point at squash commit too (land will fast-forward via
	// update-ref anyway, but this keeps the local repo consistent for assertions
	// that run before land fetches).
	require.NoError(t, localRepo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName(baseName), squashHash)))

	return squashHash
}

// assertHasMergeCommit verifies that the current tip of branchName has more than
// one parent (i.e. it is a merge commit), and that its first-parent is on baseName.
func assertHasMergeCommit(t *testing.T, repo *git.Repository, branchName, baseName string) {
	t.Helper()
	branchRef, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err, "could not get ref for %s", branchName)
	commit, err := repo.CommitObject(branchRef.Hash())
	require.NoError(t, err)
	require.Greater(t, len(commit.ParentHashes), 1,
		"branch %s should have a merge commit (>1 parents) after merge-down land", branchName)
	// Check that the base tip is one of the parents.
	baseRef, err := repo.Reference(plumbing.NewBranchReferenceName(baseName), true)
	require.NoError(t, err, "could not get ref for base %s", baseName)
	found := false
	for _, p := range commit.ParentHashes {
		if p == baseRef.Hash() {
			found = true
			break
		}
	}
	require.True(t, found,
		"branch %s merge commit should have %s (%s) as a parent", branchName, baseName, baseRef.Hash().String()[:7])
}

// assertOriginalCommitPresent checks that originalHash is still reachable from
// the tip of branchName (i.e. it was not rewritten by the land).
func assertOriginalCommitPresent(t *testing.T, repoPath, branchName, originalHash string) {
	t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", originalHash, branchName)
	cmd.Dir = repoPath
	require.NoError(t, cmd.Run(),
		"original commit %s should still be an ancestor of %s after merge-down land (commit hashes preserved)",
		originalHash[:7], branchName)
}

// assertMergeBaseEquals checks that git merge-base(ref1, ref2) == expectedHash.
func assertMergeBaseEquals(t *testing.T, repoPath, ref1, ref2, expectedHash string) {
	t.Helper()
	cmd := exec.Command("git", "merge-base", ref1, ref2)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	require.NoError(t, err, "merge-base %s %s failed", ref1, ref2)
	got := strings.TrimSpace(string(out))
	require.Equal(t, expectedHash, got,
		"merge-base(%s, %s) should be %s (no double-diff)", ref1, ref2, expectedHash[:7])
}

// runLandMerge is a convenience wrapper that runs LandCmd.Run with confirmed
// input (mocks "y" at the prompt) and returns any error.
func runLandMerge(t *testing.T, remoteName string, skipSync bool) (string, error) {
	t.Helper()
	landCmd := &LandCmd{Remote: remoteName, SkipSyncCheck: skipSync}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})
	restore := mockInput("y")
	defer restore()
	return CaptureOutput(func() error { return landCmd.Run(kongCtx) })
}

// --- Tests ---

// TestLandMerge_HappyPath is the headline case: 3-branch merge-strategy tower,
// bottom squash-merged + deleted on remote. After land:
//   - b0 is dropped from the tower.
//   - b1 and b2 each have a new merge commit.
//   - Their original commits are still ancestors (commit hashes preserved).
//   - merge-base(b1, main) == tip(main) so the double-diff is gone.
//   - Files are intact on both branches.
func TestLandMerge_HappyPath(t *testing.T) {
	localRepoPath, localRepo, _, cleanup := setupMergeLandTower(t)
	defer cleanup()

	// Record pre-land original commit hashes for b1 and b2.
	b1OrigHash := currentHash(t, localRepoPath, "b1")
	b2OrigHash := currentHash(t, localRepoPath, "b2")

	// Squash-merge b0 into main on the remote and delete its remote branch.
	squashHash := squashMergeAndDeleteRemote(t, localRepoPath, localRepo, "origin", "main", "b0")

	output, err := runLandMerge(t, "origin", false)
	t.Logf("Land output:\n%s", output)
	require.NoError(t, err, "merge-strategy land should succeed")

	// 1. b0 dropped from tower config.
	loadedCfg, err := LoadConfig()
	require.NoError(t, err)
	tower, err := getCurrentTower(loadedCfg.Repos[0])
	require.NoError(t, err)
	require.Len(t, tower.Branches, 2, "tower should have b1 and b2 after landing b0")
	require.Equal(t, "b1", tower.Branches[0].Name)
	require.Equal(t, "b2", tower.Branches[1].Name)

	// 2. b1 and b2 have merge commits; main is a parent.
	assertHasMergeCommit(t, localRepo, "b1", "main")
	assertHasMergeCommit(t, localRepo, "b2", "b1")

	// 3. Original commits are still present (hashes preserved).
	assertOriginalCommitPresent(t, localRepoPath, "b1", b1OrigHash)
	assertOriginalCommitPresent(t, localRepoPath, "b2", b2OrigHash)

	// 4. merge-base(b1, main) == tip(main) — double-diff gone.
	mainTip := currentHash(t, localRepoPath, "main")
	require.Equal(t, squashHash.String(), mainTip, "local main should be fast-forwarded to squash commit")
	assertMergeBaseEquals(t, localRepoPath, "b1", "main", mainTip)

	// 5. Files preserved on b1 and b2.
	assertBranchContainsContent(t, localRepo, "b1", "file-b1-0.txt", "Content for file-b1-0.txt\n")
	assertBranchContainsContent(t, localRepo, "b2", "file-b2-0.txt", "Content for file-b2-0.txt\n")
}

// TestLandMerge_MultipleSequentialLands verifies landing two branches in a row
// with --skip-sync-check (after the first land b1 is LocalAhead of its remote,
// which is normal and must not block the second land).
func TestLandMerge_MultipleSequentialLands(t *testing.T) {
	localRepoPath, localRepo, _, cleanup := setupMergeLandTower(t)
	defer cleanup()

	// --- First land: squash b0 ---
	squashMergeAndDeleteRemote(t, localRepoPath, localRepo, "origin", "main", "b0")
	output, err := runLandMerge(t, "origin", true)
	t.Logf("First land output:\n%s", output)
	require.NoError(t, err, "first land should succeed")

	loadedCfg, err := LoadConfig()
	require.NoError(t, err)
	tower, err := getCurrentTower(loadedCfg.Repos[0])
	require.NoError(t, err)
	require.Len(t, tower.Branches, 2, "tower should have b1 and b2 after first land")

	// --- Second land: squash b1 ---
	// Get the current b1 hash (now includes a merge commit from the first land).
	b1Hash, err := localRepo.Reference(plumbing.NewBranchReferenceName("b1"), true)
	require.NoError(t, err)
	squashMergeAndDeleteRemote(t, localRepoPath, localRepo, "origin", "main", "b1")
	// b2's original commits should be preserved after this land too.
	b2OrigHash := currentHash(t, localRepoPath, "b2")

	output, err = runLandMerge(t, "origin", true)
	t.Logf("Second land output:\n%s", output)
	require.NoError(t, err, "second land should succeed")

	loadedCfg, err = LoadConfig()
	require.NoError(t, err)
	tower, err = getCurrentTower(loadedCfg.Repos[0])
	require.NoError(t, err)
	require.Len(t, tower.Branches, 1, "tower should have only b2 after second land")
	require.Equal(t, "b2", tower.Branches[0].Name)

	// b2 must have a merge commit and its original commit must be preserved.
	assertHasMergeCommit(t, localRepo, "b2", "main")
	assertOriginalCommitPresent(t, localRepoPath, "b2", b2OrigHash)
	_ = b1Hash // used to assert first-land state; second land builds on top
}

// TestLandMerge_ConflictPausesSavesState ensures that when a merge conflict
// occurs during land, MergeState is saved with IsInProgress=true and the error
// message points the user at 'ghenga merge continue'.
func TestLandMerge_ConflictPausesSavesState(t *testing.T) {
	remoteName := "origin"
	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// Build a 2-branch merge-strategy tower where b1 will conflict with the squash commit.
	// shared.txt: main has "base", b0 adds it as "b0 version", b1 sets it to "b1 version".
	// The squash commit on main will contain "b0 version" — no conflict.
	// But we add a conflicting change to main so that merging main into b1 will fail.

	sharedFile := "shared.txt"
	sharedFilePath := filepath.Join(localRepoPath, sharedFile)

	// Add shared.txt to main.
	err = os.WriteFile(sharedFilePath, []byte("base\n"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(sharedFile)
	require.NoError(t, err)
	_, err = wt.Commit("Add shared.txt", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)

	// b0: modifies shared.txt to "b0 version".
	createBranchFromHead(t, localRepo, "b0")
	err = os.WriteFile(sharedFilePath, []byte("b0 version\n"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(sharedFile)
	require.NoError(t, err)
	_, err = wt.Commit("b0 sets shared.txt", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)
	b0Head, err := localRepo.Reference(plumbing.NewBranchReferenceName("b0"), true)
	require.NoError(t, err)

	// b1: modifies shared.txt to "b1 version".
	createBranchFromHead(t, localRepo, "b1")
	err = os.WriteFile(sharedFilePath, []byte("b1 version\n"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(sharedFile)
	require.NoError(t, err)
	_, err = wt.Commit("b1 sets shared.txt", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)

	// Push everything.
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			"refs/heads/main:refs/heads/main",
			"refs/heads/b0:refs/heads/b0",
			"refs/heads/b1:refs/heads/b1",
		},
	})
	require.NoError(t, err)

	// Simulate the squash commit: main advances to b0Head, but ALSO adds a conflicting
	// change to shared.txt that will conflict with b1. We do this by pushing a new commit
	// on top of b0Head that modifies shared.txt to "main conflict version".
	err = wt.Checkout(&git.CheckoutOptions{Hash: b0Head.Hash()})
	require.NoError(t, err)
	err = os.WriteFile(sharedFilePath, []byte("main conflict version\n"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(sharedFile)
	require.NoError(t, err)
	conflictBaseHash, err := wt.Commit("Squash+conflict on main", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(conflictBaseHash.String() + ":refs/heads/main")},
		Force:      true,
	})
	require.NoError(t, err)
	// Update local main ref.
	require.NoError(t, localRepo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), conflictBaseHash)))
	// Delete remote b0.
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{":refs/heads/b0"},
	})
	require.NoError(t, err)

	// Setup tower config.
	towers := []*Tower{{
		Name:     "conflict-tower",
		Strategy: StrategyMerge,
		Base:     "main",
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}},
	}}
	cfg := createTestConfig(t, localRepoPath, "conflict-tower", towers, "main")
	require.NoError(t, SaveConfig(cfg))

	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	output, err := runLandMerge(t, remoteName, false)
	t.Logf("Land output (conflict expected):\n%s", output)
	require.Error(t, err, "land should fail due to merge conflict")
	require.Contains(t, err.Error(), "failed during merge of updated base into remaining tower branches",
		"error should describe merge failure")
	require.Contains(t, output, "ghenga merge continue",
		"output should direct user to 'ghenga merge continue'")

	// MergeState must be saved and in progress.
	_, _, tower, _, loadErr := loadRepoInfoAndCurrentTower()
	require.NoError(t, loadErr)
	require.NotNil(t, tower.MergeState, "MergeState should be saved after conflict")
	require.True(t, tower.MergeState.IsInProgress, "MergeState.IsInProgress should be true")
	require.Equal(t, "b1", tower.MergeState.TargetBranch)
}

// TestLandMerge_EmptyTower verifies the standard "no branches to land" guard.
func TestLandMerge_EmptyTower(t *testing.T) {
	localRepoPath, _, _, _, cleanup := setupTestEnvWithRemote(t, "origin")
	defer cleanup()

	towers := []*Tower{{
		Name:     "empty",
		Strategy: StrategyMerge,
		Base:     "main",
		Branches: []Branch{},
	}}
	cfg := createTestConfig(t, localRepoPath, "empty", towers, "main")
	require.NoError(t, SaveConfig(cfg))

	landCmd := &LandCmd{Remote: "origin"}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})
	err := landCmd.Run(kongCtx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "has no branches to land")
}

// TestLandMerge_LastBranch verifies that landing the sole branch in a
// merge-strategy tower empties the tower without calling mergeTowerWithMode.
func TestLandMerge_LastBranch(t *testing.T) {
	remoteName := "origin"
	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	createTestBranch(t, localRepo, "solo", 1)
	soloHead, err := localRepo.Reference(plumbing.NewBranchReferenceName("solo"), true)
	require.NoError(t, err)

	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			"refs/heads/main:refs/heads/main",
			"refs/heads/solo:refs/heads/solo",
		},
	})
	require.NoError(t, err)

	// Simulate merge+delete.
	simulateMergeAndDelete(t, localRepo, remoteName, "main", "solo", soloHead.Hash())

	towers := []*Tower{{
		Name:     "last-tower",
		Strategy: StrategyMerge,
		Base:     "main",
		Branches: []Branch{{Name: "solo"}},
	}}
	cfg := createTestConfig(t, localRepoPath, "last-tower", towers, "main")
	require.NoError(t, SaveConfig(cfg))

	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	output, err := runLandMerge(t, remoteName, false)
	t.Logf("Last-branch land output:\n%s", output)
	require.NoError(t, err, "landing last branch in merge-strategy tower should succeed")

	loadedCfg, err := LoadConfig()
	require.NoError(t, err)
	tower, err := getCurrentTower(loadedCfg.Repos[0])
	require.NoError(t, err)
	require.Len(t, tower.Branches, 0, "tower should be empty after landing the last branch")
}

// TestLandMerge_NoBaseBranch verifies the "no base branch set" guard works
// the same as under rebase strategy.
func TestLandMerge_NoBaseBranch(t *testing.T) {
	remoteName := "origin"
	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	createTestBranch(t, localRepo, "feat", 1)
	err := localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{"refs/heads/feat:refs/heads/feat"},
	})
	require.NoError(t, err)
	// Delete remote so the "still exists" check passes.
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{":refs/heads/feat"},
	})
	require.NoError(t, err)

	towers := []*Tower{{
		Name:     "no-base",
		Strategy: StrategyMerge,
		// Base intentionally omitted.
		Branches: []Branch{{Name: "feat"}},
	}}
	cfg := createTestConfig(t, localRepoPath, "no-base", towers, "")
	require.NoError(t, SaveConfig(cfg))

	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})
	err = landCmd.Run(kongCtx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "has no base branch set")
}

// TestLandMerge_DivergedBranchBlocked ensures the pre-land sync check fires for a
// merge-strategy tower when a branch is diverged (same as rebase strategy).
func TestLandMerge_DivergedBranchBlocked(t *testing.T) {
	remoteName := "origin"
	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	createTestBranch(t, localRepo, "feat", 1)
	initialHash, err := localRepo.ResolveRevision(plumbing.Revision("refs/heads/feat"))
	require.NoError(t, err)

	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			"refs/heads/main:refs/heads/main",
			"refs/heads/feat:refs/heads/feat",
		},
	})
	require.NoError(t, err)

	// Add a local-only commit on feat.
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("feat")})
	require.NoError(t, err)
	localOnlyHash := addSingleCommit(t, localRepoPath, wt, "local.txt", "local", "Local-only commit")

	// Push a different commit to origin/feat so they diverge.
	err = wt.Checkout(&git.CheckoutOptions{Hash: *initialHash})
	require.NoError(t, err)
	remoteOnlyHash := addSingleCommit(t, localRepoPath, wt, "remote.txt", "remote", "Remote-only commit")
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(remoteOnlyHash.String() + ":refs/heads/feat")},
		Force:      true,
	})
	require.NoError(t, err)
	require.NoError(t, localRepo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("feat"), localOnlyHash)))
	require.NoError(t, localRepo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewRemoteReferenceName(remoteName, "feat"), remoteOnlyHash)))

	towers := []*Tower{{
		Name:     "diverged-tower",
		Strategy: StrategyMerge,
		Base:     "main",
		Branches: []Branch{{Name: "feat"}},
	}}
	cfg := createTestConfig(t, localRepoPath, "diverged-tower", towers, "main")
	require.NoError(t, SaveConfig(cfg))

	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})
	err = landCmd.Run(kongCtx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "have diverged or are behind the remote")
}

// TestLandMerge_RemoteBranchStillExists ensures that the "remote branch still
// exists" guard fires for a merge-strategy tower.
func TestLandMerge_RemoteBranchStillExists(t *testing.T) {
	remoteName := "origin"
	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	createTestBranch(t, localRepo, "feat", 1)
	err := localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			"refs/heads/main:refs/heads/main",
			"refs/heads/feat:refs/heads/feat",
		},
	})
	require.NoError(t, err)

	towers := []*Tower{{
		Name:     "exists-tower",
		Strategy: StrategyMerge,
		Base:     "main",
		Branches: []Branch{{Name: "feat"}},
	}}
	cfg := createTestConfig(t, localRepoPath, "exists-tower", towers, "main")
	require.NoError(t, SaveConfig(cfg))

	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})
	err = landCmd.Run(kongCtx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "still exists")
}

// TestLandMerge_DirtyMainWorktreeBlocked ensures the cwd dirty-tree check blocks
// land before any destructive step under the merge strategy.
func TestLandMerge_DirtyMainWorktreeBlocked(t *testing.T) {
	remoteName := "origin"
	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	createTestBranch(t, localRepo, "feat", 1)
	err := localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{"refs/heads/feat:refs/heads/feat"},
	})
	require.NoError(t, err)

	towers := []*Tower{{
		Name:     "dirty-main",
		Strategy: StrategyMerge,
		Base:     "main",
		Branches: []Branch{{Name: "feat"}},
	}}
	cfg := createTestConfig(t, localRepoPath, "dirty-main", towers, "main")
	require.NoError(t, SaveConfig(cfg))

	// Dirty the main worktree.
	require.NoError(t, os.WriteFile(filepath.Join(localRepoPath, "untracked.txt"), []byte("dirty"), 0644))

	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})
	err = landCmd.Run(kongCtx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is not clean")

	// Tower must be unchanged — no branch removed.
	loadedCfg, err := LoadConfig()
	require.NoError(t, err)
	tower, err := getCurrentTower(loadedCfg.Repos[0])
	require.NoError(t, err)
	require.Len(t, tower.Branches, 1, "branch must not be removed when land aborts on dirty worktree")
}

// TestLandMerge_DirtyTowerWorktreeBlocked ensures the tower-worktree dirty check
// blocks land before any destructive step under the merge strategy.
func TestLandMerge_DirtyTowerWorktreeBlocked(t *testing.T) {
	remoteName := "origin"
	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	createTestBranch(t, localRepo, "b0", 1)
	createTestBranch(t, localRepo, "b1", 1)

	// Stand on main to keep it clean and check out b1 in a separate worktree.
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)

	wt2Dir, err := filepath.Abs(filepath.Join(localRepoPath, "..", "wt-b1-"+filepath.Base(localRepoPath)))
	require.NoError(t, err)
	addWt := exec.Command("git", "worktree", "add", wt2Dir, "b1")
	addWt.Dir = localRepoPath
	out, err := addWt.CombinedOutput()
	require.NoError(t, err, "failed to create worktree for b1: %s", string(out))
	defer os.RemoveAll(wt2Dir)

	// Dirty the b1 worktree.
	require.NoError(t, os.WriteFile(filepath.Join(wt2Dir, "untracked.txt"), []byte("dirty"), 0644))

	towers := []*Tower{{
		Name:     "dirty-wt-tower",
		Strategy: StrategyMerge,
		Base:     "main",
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}},
	}}
	cfg := createTestConfig(t, localRepoPath, "dirty-wt-tower", towers, "main")
	require.NoError(t, SaveConfig(cfg))

	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})
	err = landCmd.Run(kongCtx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "uncommitted changes detected")
	require.Contains(t, err.Error(), wt2Dir)

	// Tower unchanged.
	loadedCfg, err := LoadConfig()
	require.NoError(t, err)
	tower, err := getCurrentTower(loadedCfg.Repos[0])
	require.NoError(t, err)
	require.Len(t, tower.Branches, 2, "land must not remove branches when it aborts on a dirty tower worktree")
	require.Nil(t, tower.MergeState, "no merge should have been started")
}

// TestLandMerge_BaseAdvancedIndependently verifies that when origin/main advanced
// with unrelated commits (not just the squash of b0), the land fast-forwards the
// local base to the new tip and then merges that tip into the remaining branches.
func TestLandMerge_BaseAdvancedIndependently(t *testing.T) {
	remoteName := "origin"
	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// Build a simple 2-branch tower.
	createTestBranch(t, localRepo, "b0", 1)
	b0Head, err := localRepo.Reference(plumbing.NewBranchReferenceName("b0"), true)
	require.NoError(t, err)
	createTestBranch(t, localRepo, "b1", 1)

	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			"refs/heads/main:refs/heads/main",
			"refs/heads/b0:refs/heads/b0",
			"refs/heads/b1:refs/heads/b1",
		},
	})
	require.NoError(t, err)

	// Advance origin/main with two extra commits: the squash of b0 PLUS an independent commit.
	err = wt.Checkout(&git.CheckoutOptions{Hash: b0Head.Hash()})
	require.NoError(t, err)
	extraHash := addSingleCommit(t, localRepoPath, wt, "extra.txt", "extra", "Independent commit on main")
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(extraHash.String() + ":refs/heads/main")},
		Force:      true,
	})
	require.NoError(t, err)
	require.NoError(t, localRepo.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), extraHash)))
	// Delete remote b0.
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{":refs/heads/b0"},
	})
	require.NoError(t, err)

	towers := []*Tower{{
		Name:     "adv-base-tower",
		Strategy: StrategyMerge,
		Base:     "main",
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}},
	}}
	cfg := createTestConfig(t, localRepoPath, "adv-base-tower", towers, "main")
	require.NoError(t, SaveConfig(cfg))

	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")})
	require.NoError(t, err)
	// Reset local main to its initial state so land has to fast-forward it.
	initialMainHash, err := localRepo.ResolveRevision(plumbing.Revision("refs/heads/main"))
	require.NoError(t, err)
	err = wt.Reset(&git.ResetOptions{Commit: *initialMainHash, Mode: git.HardReset})
	require.NoError(t, err)

	b1OrigHash := currentHash(t, localRepoPath, "b1")

	output, err := runLandMerge(t, remoteName, false)
	t.Logf("Land output:\n%s", output)
	require.NoError(t, err, "land with independently advanced base should succeed")

	// Local main must now point at the new remote tip (extraHash).
	mainTip := currentHash(t, localRepoPath, "main")
	require.Equal(t, extraHash.String(), mainTip,
		"local main should be fast-forwarded to the independently advanced remote tip")

	// b1 must have a merge commit with the new main as a parent.
	assertHasMergeCommit(t, localRepo, "b1", "main")
	// b1's original commit is preserved.
	assertOriginalCommitPresent(t, localRepoPath, "b1", b1OrigHash)
	// merge-base(b1, main) == tip(main) so double-diff is gone.
	assertMergeBaseEquals(t, localRepoPath, "b1", "main", mainTip)
}
