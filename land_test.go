package main

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/stretchr/testify/require"
)

func TestLand_HappyPath(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "my-feature"
	branch1Name := "feat-a"
	branch2Name := "feat-b"

	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	// --- Setup Branches ---
	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// Create branch1
	createTestBranch(t, localRepo, branch1Name, 1) // Adds file-feat-a-0.txt
	branch1Head, err := localRepo.Reference(plumbing.NewBranchReferenceName(branch1Name), true)
	require.NoError(t, err)

	// Create branch2 based on branch1
	createTestBranch(t, localRepo, branch2Name, 1) // Adds file-feat-b-0.txt

	// --- Push to Remote ---
	// Push main, branch1, branch2
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName),
			config.RefSpec("refs/heads/" + branch1Name + ":refs/heads/" + branch1Name),
			config.RefSpec("refs/heads/" + branch2Name + ":refs/heads/" + branch2Name),
		},
	})
	require.NoError(t, err, "Failed to push initial branches")

	// --- Setup Config ---
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			Base: baseBranchName,
			Branches: []Branch{
				{Name: branch1Name},
				{Name: branch2Name},
			},
		},
	}, baseBranchName) // baseBranchName here is redundant due to explicit Tower.Base setting, but consistent
	err = SaveConfig(cfg)
	require.NoError(t, err)

	// --- Simulate Merge and Delete on Remote ---
	// "Merge" branch1 into main on remote by directly updating the remote's main ref
	// In a bare repo, we can just force-update the ref
	remoteMainBranchRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(baseBranchName), branch1Head.Hash())
	err = localRepo.Storer.SetReference(remoteMainBranchRef) // Update local ref first
	require.NoError(t, err)
	// Now push this updated main to the actual remote
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName)},
		Force:      true, // Force push the updated main ref
	})
	// Ignore "already up-to-date" error which might happen if branch1 had no new commits vs main initially
	if err != nil && !strings.Contains(err.Error(), "already up-to-date") {
		require.NoError(t, err, "Failed to push updated main branch head to remote")
	}

	// Delete remote branch1
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(":refs/heads/" + branch1Name)}, // Empty local side means delete remote
	})
	require.NoError(t, err, "Failed to delete remote branch "+branch1Name)

	// --- Run Land Command ---
	landCmd := &LandCmd{Remote: remoteName}
	// We need to be on a valid branch that's not involved in the rebase initially
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(baseBranchName)})
	require.NoError(t, err)

	output, err := CaptureOutput(func() error {
		// Use a mock context
		parser := kong.Must(&CLI{})
		kongCtx, _ := parser.Parse([]string{"land"}) // Minimal parse to get a context
		return landCmd.Run(kongCtx)
	})
	t.Log("Land command output:\n", output) // Log output for debugging
	require.NoError(t, err, "land command failed")

	// --- Assertions ---
	// 1. Config updated: branch1 removed, branch2 is now the only branch
	loadedConfig, err := LoadConfig()
	require.NoError(t, err, "Failed to reload config after land")
	currentTower, err := getCurrentTower(loadedConfig.Repos[0])
	require.NoError(t, err)
	require.Len(t, currentTower.Branches, 1, "Tower should have only one branch left")
	require.Equal(t, branch2Name, currentTower.Branches[0].Name, "Remaining branch should be branch2")

	// 2. Branch2 rebased onto the new main head (which was branch1's head)
	// Get the new base commit hash (which should be the commit from branch1)
	baseCommitHash := branch1Head.Hash()

	// Get the current head of branch2
	branch2Ref, err := localRepo.Reference(plumbing.NewBranchReferenceName(branch2Name), true)
	require.NoError(t, err)
	branch2Commit, err := localRepo.CommitObject(branch2Ref.Hash())
	require.NoError(t, err)

	// Check that the parent of branch2's head commit is now the base commit hash
	require.Len(t, branch2Commit.ParentHashes, 1, "Branch2 should have exactly one parent after rebase")
	require.Equal(t, baseCommitHash, branch2Commit.ParentHashes[0], "Branch2 should be rebased onto the new base head")

	// 3. Check that the commit introduced in branch2 is still present
	commitIter, err := localRepo.Log(&git.LogOptions{From: branch2Ref.Hash()})
	require.NoError(t, err)
	foundBranch2Commit := false
	err = commitIter.ForEach(func(c *object.Commit) error {
		if strings.Contains(c.Message, "Add file-feat-b-0.txt") {
			foundBranch2Commit = true
			return storer.ErrStop // Found it, stop iterating
		}
		// Check if we've reached the base commit, don't go past it
		if c.Hash == baseCommitHash {
			return storer.ErrStop
		}
		return nil
	})
	require.NoError(t, err)
	require.True(t, foundBranch2Commit, "Commit specific to branch2 not found after rebase")

	// 4. Check local branch1 still exists (LandCmd doesn't delete it locally)
	_, err = localRepo.Reference(plumbing.NewBranchReferenceName(branch1Name), false)
	require.NoError(t, err, "Local branch1 should still exist")
}

// Helper to run git commands for operations not easily done with go-git (like merge --ff-only on remote bare repo)
// Note: This might be less reliable than pure go-git if possible.
func runGitCommand(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git command failed: git %s Output: %s", strings.Join(args, " "), string(output))
	return output
}
