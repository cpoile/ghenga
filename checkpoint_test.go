package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

// setupCheckpointTower creates a 3-branch tower on top of "main", each branch
// with one commit. Returns repoPath, the git.Repository, and a cleanup func.
func setupCheckpointTower(t *testing.T) (string, *git.Repository, func()) {
	t.Helper()
	repoPath, repo, cleanup := setupTestEnv(t)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	createTestBranch(t, repo, "b0", 1)
	createTestBranch(t, repo, "b1", 1)
	createTestBranch(t, repo, "b2", 1)

	// Return to main.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))

	towerName := "cp-tower"
	towers := []*Tower{{
		Name:     towerName,
		Base:     "main",
		Branches: []Branch{{Name: "b0"}, {Name: "b1"}, {Name: "b2"}},
	}}
	cfg := createTestConfig(t, repoPath, towerName, towers, "main")
	require.NoError(t, SaveConfig(cfg))

	return repoPath, repo, cleanup
}

// runCheckpoint calls CheckpointCmd.Run with the given name.
func runCheckpoint(name string) error {
	cmd := &CheckpointCmd{Name: name}
	return cmd.Run(&kong.Context{})
}

// runRestore calls RestoreCmd.Run.
func runRestore() error {
	cmd := &RestoreCmd{}
	return cmd.Run(&kong.Context{})
}

// reloadCurrentTower reloads the config from disk and returns the current tower.
func reloadCurrentTower(t *testing.T) *Tower {
	t.Helper()
	cfg, err := LoadConfig()
	require.NoError(t, err)
	require.NotEmpty(t, cfg.Repos)
	tower, err := getCurrentTower(cfg.Repos[0])
	require.NoError(t, err)
	return tower
}

// gitRevParse returns the commit hash of the named ref via git CLI.
func gitRevParse(t *testing.T, repoPath, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	require.NoError(t, err, "rev-parse %s failed", ref)
	return strings.TrimSpace(string(out))
}

// advanceBranchCallCount tracks unique filenames across advanceBranch calls so
// repeated calls never try to commit the same file twice.
var advanceBranchCallCount int

// advanceBranch checks out branchName, adds one commit with a unique filename,
// and returns to main. Returns the new commit hash.
func advanceBranch(t *testing.T, repoPath string, repo *git.Repository, branchName string) string {
	t.Helper()
	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branchName),
	}))
	advanceBranchCallCount++
	filename := fmt.Sprintf("extra-%s-%d.txt", branchName, advanceBranchCallCount)
	hash := addSingleCommit(t, repoPath, wt, filename, "extra\n",
		"extra commit on "+branchName)
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
	}))
	return hash.String()
}

// touchFile writes content to dir/name (creates or truncates).
func touchFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
}

// --- Checkpoint tests ---

// TestCheckpoint_CapturesBranchHashesAndBase verifies that checkpoint stores
// every branch's current hash and the tower base under the given name.
func TestCheckpoint_CapturesBranchHashesAndBase(t *testing.T) {
	repoPath, _, cleanup := setupCheckpointTower(t)
	defer cleanup()

	h0 := gitRevParse(t, repoPath, "b0")
	h1 := gitRevParse(t, repoPath, "b1")
	h2 := gitRevParse(t, repoPath, "b2")

	_, err := CaptureOutput(func() error { return runCheckpoint("snap1") })
	require.NoError(t, err)

	tower := reloadCurrentTower(t)
	require.Len(t, tower.Checkpoints, 1)
	cp := tower.Checkpoints[0]
	assert.Equal(t, "snap1", cp.Name)
	assert.Equal(t, "main", cp.Base)
	require.Len(t, cp.Branches, 3)
	assert.Equal(t, "b0", cp.Branches[0].Name)
	assert.Equal(t, h0, cp.Branches[0].Hash)
	assert.Equal(t, "b1", cp.Branches[1].Name)
	assert.Equal(t, h1, cp.Branches[1].Hash)
	assert.Equal(t, "b2", cp.Branches[2].Name)
	assert.Equal(t, h2, cp.Branches[2].Hash)
}

// TestCheckpoint_DefaultTimestampName verifies that omitting the name produces a
// timestamp in the form "2006-01-02T15-04-05".
func TestCheckpoint_DefaultTimestampName(t *testing.T) {
	_, _, cleanup := setupCheckpointTower(t)
	defer cleanup()

	before := time.Now().Format("2006-01-02T15-04-05")
	_, err := CaptureOutput(func() error { return runCheckpoint("") })
	require.NoError(t, err)
	after := time.Now().Format("2006-01-02T15-04-05")

	tower := reloadCurrentTower(t)
	require.Len(t, tower.Checkpoints, 1)
	name := tower.Checkpoints[0].Name

	// The name must sort between before and after (inclusive).
	assert.True(t, name >= before && name <= after,
		"expected timestamp between %s and %s, got %s", before, after, name)
}

// TestCheckpoint_NameCollision_OverwriteYes verifies that "y" to the overwrite
// prompt replaces the checkpoint in place (exactly one entry, updated hashes).
func TestCheckpoint_NameCollision_OverwriteYes(t *testing.T) {
	repoPath, repo, cleanup := setupCheckpointTower(t)
	defer cleanup()

	_, err := CaptureOutput(func() error { return runCheckpoint("mysnap") })
	require.NoError(t, err)

	newHash := advanceBranch(t, repoPath, repo, "b0")

	restore := mockInput("y")
	_, overwriteErr := CaptureOutput(func() error { return runCheckpoint("mysnap") })
	restore()
	require.NoError(t, overwriteErr)

	tower := reloadCurrentTower(t)
	// Exactly one checkpoint with that name.
	count := 0
	for _, cp := range tower.Checkpoints {
		if cp.Name == "mysnap" {
			count++
		}
	}
	assert.Equal(t, 1, count)
	require.Len(t, tower.Checkpoints, 1)
	assert.Equal(t, newHash, tower.Checkpoints[0].Branches[0].Hash)
}

// TestCheckpoint_NameCollision_OverwriteNo verifies that "n" leaves the original
// checkpoint untouched and adds nothing new.
func TestCheckpoint_NameCollision_OverwriteNo(t *testing.T) {
	repoPath, repo, cleanup := setupCheckpointTower(t)
	defer cleanup()

	_, err := CaptureOutput(func() error { return runCheckpoint("mysnap") })
	require.NoError(t, err)

	originalHash := reloadCurrentTower(t).Checkpoints[0].Branches[0].Hash

	advanceBranch(t, repoPath, repo, "b0")

	restore := mockInput("n")
	_, overwriteErr := CaptureOutput(func() error { return runCheckpoint("mysnap") })
	restore()
	require.NoError(t, overwriteErr)

	tower := reloadCurrentTower(t)
	require.Len(t, tower.Checkpoints, 1)
	assert.Equal(t, originalHash, tower.Checkpoints[0].Branches[0].Hash)
}

// TestCheckpoint_EmptyTowerError verifies that checkpointing a tower with no
// branches returns a clear error.
func TestCheckpoint_EmptyTowerError(t *testing.T) {
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	towerName := "empty-tower"
	cfg := createTestConfig(t, repoPath, towerName, []*Tower{{
		Name:     towerName,
		Base:     "main",
		Branches: []Branch{},
	}}, "main")
	require.NoError(t, SaveConfig(cfg))

	err := runCheckpoint("snap")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no branches to checkpoint")
}

// --- Restore tests ---

// TestRestore_BranchesReturnToCheckpointHash verifies that after advancing all
// three branches, restore moves each back to its saved hash.
func TestRestore_BranchesReturnToCheckpointHash(t *testing.T) {
	repoPath, repo, cleanup := setupCheckpointTower(t)
	defer cleanup()

	h0 := gitRevParse(t, repoPath, "b0")
	h1 := gitRevParse(t, repoPath, "b1")
	h2 := gitRevParse(t, repoPath, "b2")

	_, err := CaptureOutput(func() error { return runCheckpoint("before") })
	require.NoError(t, err)

	advanceBranch(t, repoPath, repo, "b0")
	advanceBranch(t, repoPath, repo, "b1")
	advanceBranch(t, repoPath, repo, "b2")

	restore := mockInput("1\ny")
	_, restoreErr := CaptureOutput(func() error { return runRestore() })
	restore()
	require.NoError(t, restoreErr)

	assert.Equal(t, h0, gitRevParse(t, repoPath, "b0"))
	assert.Equal(t, h1, gitRevParse(t, repoPath, "b1"))
	assert.Equal(t, h2, gitRevParse(t, repoPath, "b2"))
}

// TestRestore_MembershipRestore verifies that a branch dropped from the tower
// after checkpointing is re-added to tower.Branches on restore, and its ref
// is also restored.
func TestRestore_MembershipRestore(t *testing.T) {
	repoPath, _, cleanup := setupCheckpointTower(t)
	defer cleanup()

	h0 := gitRevParse(t, repoPath, "b0")

	_, err := CaptureOutput(func() error { return runCheckpoint("snap") })
	require.NoError(t, err)

	// Simulate a land: remove b0 from the tower's branch list.
	cfg, err := LoadConfig()
	require.NoError(t, err)
	tower, towerErr := getCurrentTower(cfg.Repos[0])
	require.NoError(t, towerErr)
	tower.Branches = []Branch{{Name: "b1"}, {Name: "b2"}}
	require.NoError(t, SaveConfig(cfg))

	restore := mockInput("1\ny")
	_, restoreErr := CaptureOutput(func() error { return runRestore() })
	restore()
	require.NoError(t, restoreErr)

	tower = reloadCurrentTower(t)
	require.Len(t, tower.Branches, 3, "expected b0 re-added to tower")
	assert.Equal(t, "b0", tower.Branches[0].Name)
	assert.Equal(t, h0, gitRevParse(t, repoPath, "b0"))
}

// TestRestore_BaseRestore verifies that tower.Base is reverted to the
// checkpoint's base after a restore.
func TestRestore_BaseRestore(t *testing.T) {
	_, _, cleanup := setupCheckpointTower(t)
	defer cleanup()

	_, err := CaptureOutput(func() error { return runCheckpoint("snap") })
	require.NoError(t, err)

	cfg, err := LoadConfig()
	require.NoError(t, err)
	tower, towerErr := getCurrentTower(cfg.Repos[0])
	require.NoError(t, towerErr)
	tower.Base = "some-other-base"
	require.NoError(t, SaveConfig(cfg))

	restore := mockInput("1\ny")
	_, restoreErr := CaptureOutput(func() error { return runRestore() })
	restore()
	require.NoError(t, restoreErr)

	assert.Equal(t, "main", reloadCurrentTower(t).Base)
}

// TestRestore_InteractiveSelection verifies that picking index 2 restores the
// second (older) checkpoint rather than the latest.
func TestRestore_InteractiveSelection(t *testing.T) {
	repoPath, repo, cleanup := setupCheckpointTower(t)
	defer cleanup()

	// First (older) checkpoint.
	h0old := gitRevParse(t, repoPath, "b0")
	_, err := CaptureOutput(func() error { return runCheckpoint("old") })
	require.NoError(t, err)

	// Advance b0 and take a newer checkpoint.
	advanceBranch(t, repoPath, repo, "b0")
	_, err = CaptureOutput(func() error { return runCheckpoint("new") })
	require.NoError(t, err)

	// Advance b0 again so current hash matches neither checkpoint.
	advanceBranch(t, repoPath, repo, "b0")

	// Pick index 2 (newest-first, so index 2 = older "old" checkpoint), confirm y.
	restore := mockInput("2\ny")
	_, restoreErr := CaptureOutput(func() error { return runRestore() })
	restore()
	require.NoError(t, restoreErr)

	assert.Equal(t, h0old, gitRevParse(t, repoPath, "b0"),
		"expected old checkpoint hash to be restored")
}

// TestRestore_EmptyInputDefaultsToLatest verifies that pressing Enter (empty
// input) defaults to index 1 (the latest checkpoint).
func TestRestore_EmptyInputDefaultsToLatest(t *testing.T) {
	repoPath, repo, cleanup := setupCheckpointTower(t)
	defer cleanup()

	_, err := CaptureOutput(func() error { return runCheckpoint("cp1") })
	require.NoError(t, err)

	advanceBranch(t, repoPath, repo, "b0")
	h0latest := gitRevParse(t, repoPath, "b0")
	_, err = CaptureOutput(func() error { return runCheckpoint("cp2") })
	require.NoError(t, err)

	// Advance once more so the current hash differs from both checkpoints.
	advanceBranch(t, repoPath, repo, "b0")

	// Empty input (defaults to 1 = latest "cp2"), confirm y.
	restore := mockInput("\ny")
	_, restoreErr := CaptureOutput(func() error { return runRestore() })
	restore()
	require.NoError(t, restoreErr)

	assert.Equal(t, h0latest, gitRevParse(t, repoPath, "b0"))
}

// TestRestore_NoCheckpointsError verifies that restore fails cleanly when no
// checkpoints exist for the tower.
func TestRestore_NoCheckpointsError(t *testing.T) {
	_, _, cleanup := setupCheckpointTower(t)
	defer cleanup()

	err := runRestore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no checkpoints found")
}

// TestRestore_BlockedWhenOperationInProgress verifies that restore returns an
// error if a rebase or merge is paused.
func TestRestore_BlockedWhenOperationInProgress(t *testing.T) {
	_, _, cleanup := setupCheckpointTower(t)
	defer cleanup()

	cfg, err := LoadConfig()
	require.NoError(t, err)
	tower, towerErr := getCurrentTower(cfg.Repos[0])
	require.NoError(t, towerErr)
	// Inject a paused rebase state and a dummy checkpoint so we pass the
	// "no checkpoints" guard and hit the operation-in-progress check first.
	tower.RebaseState = &RebaseState{IsInProgress: true}
	tower.Checkpoints = []Checkpoint{{Name: "dummy", Created: time.Now().Format(time.RFC3339)}}
	require.NoError(t, SaveConfig(cfg))

	err = runRestore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "operation in progress")
}

// TestRestore_DirtyWorkingTreeError verifies that restore fails when the working
// tree has uncommitted changes.
func TestRestore_DirtyWorkingTreeError(t *testing.T) {
	repoPath, _, cleanup := setupCheckpointTower(t)
	defer cleanup()

	_, err := CaptureOutput(func() error { return runCheckpoint("snap") })
	require.NoError(t, err)

	// Make the working tree dirty.
	touchFile(t, repoPath, "dirty.txt", "uncommitted\n")

	err = runRestore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not clean")
}

// TestRestore_RecreatesDeletedBranch verifies that a branch hard-deleted from
// git after checkpointing is recreated by restore.
func TestRestore_RecreatesDeletedBranch(t *testing.T) {
	repoPath, _, cleanup := setupCheckpointTower(t)
	defer cleanup()

	h0 := gitRevParse(t, repoPath, "b0")

	_, err := CaptureOutput(func() error { return runCheckpoint("snap") })
	require.NoError(t, err)

	// Hard-delete b0 from git.
	delCmd := exec.Command("git", "branch", "-D", "b0")
	delCmd.Dir = repoPath
	require.NoError(t, delCmd.Run())

	restore := mockInput("1\ny")
	_, restoreErr := CaptureOutput(func() error { return runRestore() })
	restore()
	require.NoError(t, restoreErr)

	assert.Equal(t, h0, gitRevParse(t, repoPath, "b0"),
		"expected b0 recreated at checkpoint hash")
}

// TestRestore_WorktreeSafe verifies that a branch checked out in a worktree is
// restored via update-ref (rather than git checkout, which would fail for a
// branch that is live in another worktree).
func TestRestore_WorktreeSafe(t *testing.T) {
	repoPath, _, cleanup := setupCheckpointTower(t)
	defer cleanup()

	h0 := gitRevParse(t, repoPath, "b0")

	_, err := CaptureOutput(func() error { return runCheckpoint("snap") })
	require.NoError(t, err)

	// Create a linked worktree checked out on b0.
	worktreeDir := repoPath + "-wt-b0"
	addWtCmd := exec.Command("git", "worktree", "add", worktreeDir, "b0")
	addWtCmd.Dir = repoPath
	require.NoError(t, addWtCmd.Run())
	defer exec.Command("git", "worktree", "remove", "--force", worktreeDir).Run()

	// Add a commit inside the worktree so b0 moves forward.
	touchFile(t, worktreeDir, "wt-file.txt", "wt\n")
	stageCmd := exec.Command("git", "add", "wt-file.txt")
	stageCmd.Dir = worktreeDir
	require.NoError(t, stageCmd.Run())
	commitCmd := exec.Command("git", "commit", "-m", "worktree commit")
	commitCmd.Dir = worktreeDir
	commitCmd.Env = append(commitCmd.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=t@t.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=t@t.com")
	require.NoError(t, commitCmd.Run())

	require.NotEqual(t, h0, gitRevParse(t, repoPath, "b0"),
		"b0 should have moved forward")

	// Restore: pick 1, confirm main restore, and accept the worktree reset prompt.
	restore := mockInput("1\ny\ny")
	_, restoreErr := CaptureOutput(func() error { return runRestore() })
	restore()
	require.NoError(t, restoreErr)

	assert.Equal(t, h0, gitRevParse(t, repoPath, "b0"),
		"expected b0 restored to checkpoint hash via update-ref")
}
