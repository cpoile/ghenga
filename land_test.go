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

	// 3. Verify exact expected state: branch2's content should be preserved after rebase
	// Check that branch2's specific file exists with exact expected content
	expectedFileName := "file-feat-b-0.txt"
	expectedFileContent := "Content for file-feat-b-0.txt\n"
	assertBranchContainsContent(t, localRepo, branch2Name, expectedFileName, expectedFileContent)

	// Verify exact commit structure: branch2 should have exactly one parent (the base commit)
	// and contain both the branch1 file and branch2 file
	rebasedBranch2Commit, err := localRepo.CommitObject(branch2Ref.Hash())
	require.NoError(t, err)

	// Verify that both branch1's file and branch2's file exist in the rebased commit
	branch2Tree, err := rebasedBranch2Commit.Tree()
	require.NoError(t, err)

	// Should contain branch1's file (inherited from base)
	branch1File, err := branch2Tree.File("file-feat-a-0.txt")
	require.NoError(t, err, "Branch2 should contain branch1's file after rebase")
	branch1Content, err := branch1File.Contents()
	require.NoError(t, err)
	require.Equal(t, "Content for file-feat-a-0.txt\n", branch1Content, "Branch1's file content should be preserved")

	// Should contain branch2's own file
	branch2File, err := branch2Tree.File("file-feat-b-0.txt")
	require.NoError(t, err, "Branch2 should contain its own file after rebase")
	branch2Content, err := branch2File.Contents()
	require.NoError(t, err)
	require.Equal(t, expectedFileContent, branch2Content, "Branch2's file content should be preserved")

	// 4. Check local branch1 still exists (LandCmd doesn't delete it locally)
	_, err = localRepo.Reference(plumbing.NewBranchReferenceName(branch1Name), false)
	require.NoError(t, err, "Local branch1 should still exist")
}

func TestLandDirtyWorktree(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "my-feature"
	branch1Name := "feat-a"

	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	// Setup a basic tower config (doesn't need remote branches for this test)
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			Base: baseBranchName,
			Branches: []Branch{
				{Name: branch1Name},
			},
		},
	}, baseBranchName)
	err := SaveConfig(cfg)
	require.NoError(t, err)

	// Make the working directory dirty
	_, err = localRepo.Worktree()
	require.NoError(t, err)
	untrackedFilePath := filepath.Join(localRepoPath, "untracked.txt")
	err = os.WriteFile(untrackedFilePath, []byte("dirty"), 0644)
	require.NoError(t, err)

	// Run Land Command (expecting failure)
	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})

	err = landCmd.Run(kongCtx)
	require.Error(t, err, "Expected land command to fail with dirty worktree")
	require.Contains(t, err.Error(), "working directory is not clean", "Error message should mention unclean worktree")
}

func TestLandEmptyTower(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "empty-tower"

	localRepoPath, _, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	// Setup a config with an empty tower
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name:     towerName,
			Base:     baseBranchName,
			Branches: []Branch{}, // Empty branches list
		},
	}, baseBranchName)
	err := SaveConfig(cfg)
	require.NoError(t, err)

	// Run Land Command (expecting failure)
	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})

	err = landCmd.Run(kongCtx)
	require.Error(t, err, "Expected land command to fail with empty tower")
	require.Contains(t, err.Error(), "has no branches to land", "Error message should mention no branches to land")
}

func TestLandDivergedBranch(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "diverged-tower"
	branchName := "feat-diverged"

	localRepoPath, localRepo, remoteRepoPath, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// Create base branch commit on remote
	_, err = git.PlainOpen(remoteRepoPath)
	require.NoError(t, err)
	// Push main to remote
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName)},
	})
	require.NoError(t, err)

	// Create initial commit for the feature branch
	createTestBranch(t, localRepo, branchName, 1) // file-feat-diverged-0.txt
	initialCommitHash, err := localRepo.ResolveRevision(plumbing.Revision("refs/heads/" + branchName))
	require.NoError(t, err)

	// Push the initial state of the branch
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + branchName + ":refs/heads/" + branchName)},
	})
	require.NoError(t, err)

	// Add a commit locally
	addSingleCommit(t, localRepoPath, wt, "local-only.txt", "local", "Local commit")
	localCommitHash, err := localRepo.ResolveRevision(plumbing.Revision("HEAD"))
	require.NoError(t, err)

	// Add a *different* commit to the remote branch (based on the initial commit)
	// Checkout the initial commit hash on the remote (simulate branching from the same point)
	// This is tricky with bare repo. Easiest might be to checkout locally, commit, push force?
	// Alternative: Create commit object directly on remote? Let's try checking out locally.
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Hash: *initialCommitHash}))
	remoteCommitHash := addSingleCommit(t, localRepoPath, wt, "remote-only.txt", "remote", "Remote commit")
	// Force push this new commit to the remote branch, overwriting the previous remote state
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(remoteCommitHash.String() + ":refs/heads/" + branchName)},
		Force:      true,
	})
	require.NoError(t, err, "Failed to force push remote commit")

	// Checkout the local divergent commit again
	err = wt.Checkout(&git.CheckoutOptions{Hash: *localCommitHash})
	require.NoError(t, err)
	// Update local branch ref to point to local commit
	localBranchRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), *localCommitHash)
	err = localRepo.Storer.SetReference(localBranchRef)
	require.NoError(t, err)

	// Setup Config
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			Base: baseBranchName,
			Branches: []Branch{
				{Name: branchName},
			},
		},
	}, baseBranchName)
	err = SaveConfig(cfg)
	require.NoError(t, err)

	// Run Land Command (expecting failure)
	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})

	err = landCmd.Run(kongCtx)
	require.Error(t, err, "Expected land command to fail with diverged branch")
	require.Contains(t, err.Error(), "have diverged or are behind the remote", "Error message should mention diverged branch")
	require.Contains(t, err.Error(), "run 'ghenga rebase'", "Error message should suggest rebase")
}

func TestLandRemoteAheadBranch(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "remote-ahead-tower"
	branchName := "feat-remote-ahead"

	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// Push main to remote
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName)},
	})
	require.NoError(t, err)

	// Create initial commit for the feature branch locally
	createTestBranch(t, localRepo, branchName, 1) // file-feat-remote-ahead-0.txt
	initialCommitHash, err := localRepo.ResolveRevision(plumbing.Revision("refs/heads/" + branchName))
	require.NoError(t, err)

	// Push the initial state of the branch
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + branchName + ":refs/heads/" + branchName)},
	})
	require.NoError(t, err)

	// Add a commit *only* to the remote branch (based on the initial commit)
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Hash: *initialCommitHash}))
	remoteCommitHash := addSingleCommit(t, localRepoPath, wt, "remote-only.txt", "remote", "Remote commit")
	// Force push this new commit to the remote branch
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(remoteCommitHash.String() + ":refs/heads/" + branchName)},
		Force:      true,
	})
	require.NoError(t, err, "Failed to force push remote commit")

	// Ensure local branch is back at the initial commit
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branchName)})
	require.NoError(t, err)
	err = wt.Reset(&git.ResetOptions{Commit: *initialCommitHash, Mode: git.HardReset})
	require.NoError(t, err)
	// Verify local head is correct
	localHead, err := localRepo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err)
	require.Equal(t, *initialCommitHash, localHead.Hash(), "Local branch head should be at initial commit")

	// Fetch remote changes so local repo knows remote is ahead
	err = localRepo.Fetch(&git.FetchOptions{RemoteName: remoteName})
	// Allow "already up-to-date" error, as it's not a failure in this context
	if err != nil && err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err, "Fetch should succeed")
	}

	// Setup Config
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			Base: baseBranchName,
			Branches: []Branch{
				{Name: branchName},
			},
		},
	}, baseBranchName)
	err = SaveConfig(cfg)
	require.NoError(t, err)

	// Run Land Command (expecting failure)
	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})

	// Capture output to check the printed message
	output, err := CaptureOutput(func() error {
		return landCmd.Run(kongCtx)
	})
	require.Error(t, err, "Expected land command to fail with remote ahead branch")
	// Check the generic returned error message
	require.Contains(t, err.Error(), "have diverged or are behind the remote", "Generic error message check")
	// Check the specific printed output message
	require.Contains(t, output, "is ahead of local branch", "Specific printed error message should mention remote ahead")
}

func TestLandRemoteBranchExists(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "remote-exists-tower"
	branchName := "feat-remote-exists"

	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	// Push main to remote
	err := localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName)},
	})
	require.NoError(t, err)

	// Create and push the feature branch
	createTestBranch(t, localRepo, branchName, 1)
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + branchName + ":refs/heads/" + branchName)},
	})
	require.NoError(t, err)

	// Setup Config
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			Base: baseBranchName,
			Branches: []Branch{
				{Name: branchName}, // This is the bottom branch
			},
		},
	}, baseBranchName)
	err = SaveConfig(cfg)
	require.NoError(t, err)

	// Run Land Command (expecting failure because remote branch exists)
	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})

	err = landCmd.Run(kongCtx)
	require.Error(t, err, "Expected land command to fail when remote branch exists")
	require.Contains(t, err.Error(), "remote branch", "Error message should mention remote branch")
	require.Contains(t, err.Error(), "still exists", "Error message should mention branch still exists")
}

func TestLandNoBaseBranch(t *testing.T) {
	remoteName := "origin"
	towerName := "no-base-tower"
	branchName := "feat-no-base"

	localRepoPath, _, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	// Setup Config with no base branch set for the tower
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			// Base: baseBranchName, // Intentionally omitted
			Branches: []Branch{
				{Name: branchName},
			},
		},
	}, "") // Pass empty baseBranch to createTestConfig helper
	err := SaveConfig(cfg)
	require.NoError(t, err)

	// Need to setup the branch locally/remotely and delete remote for check to pass
	localRepo, err := git.PlainOpen(localRepoPath)
	require.NoError(t, err)
	createTestBranch(t, localRepo, branchName, 1)
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + branchName + ":refs/heads/" + branchName)},
	})
	require.NoError(t, err)
	// Delete remote branch so that check passes
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(":refs/heads/" + branchName)},
	})
	require.NoError(t, err)

	// Run Land Command (expecting failure)
	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})

	err = landCmd.Run(kongCtx)
	require.Error(t, err, "Expected land command to fail when tower has no base branch")
	require.Contains(t, err.Error(), "has no base branch set", "Error message should mention missing base branch")
}

func TestLandBaseUpdateSuccess(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "base-update-tower"
	branch1Name := "feat-bu-a"
	branch2Name := "feat-bu-b"

	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// Create initial main commit ref
	initialMainHash, err := localRepo.ResolveRevision(plumbing.Revision("refs/heads/" + baseBranchName))
	require.NoError(t, err)

	// Create and push branches
	createTestBranch(t, localRepo, branch1Name, 1)
	_, err = localRepo.Reference(plumbing.NewBranchReferenceName(branch1Name), true) // We need the ref for the hash later, but ignore here
	require.NoError(t, err)
	createTestBranch(t, localRepo, branch2Name, 1)

	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName),
			config.RefSpec("refs/heads/" + branch1Name + ":refs/heads/" + branch1Name),
			config.RefSpec("refs/heads/" + branch2Name + ":refs/heads/" + branch2Name),
		},
	})
	require.NoError(t, err)

	// Add a NEW commit to the BASE branch on the REMOTE ONLY
	// Check out the current remote main commit locally
	err = wt.Checkout(&git.CheckoutOptions{Hash: *initialMainHash})
	require.NoError(t, err)
	// Add a commit
	newBaseCommitHash := addSingleCommit(t, localRepoPath, wt, "new-base-commit.txt", "base update", "New commit on base")
	// Force push this new commit to the remote base branch
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(newBaseCommitHash.String() + ":refs/heads/" + baseBranchName)},
		Force:      true,
	})
	require.NoError(t, err, "Failed to force push new base commit to remote")

	// Simulate merge and delete of branch1 on remote
	// We ONLY delete the remote branch. The key is that origin/main has advanced independently (to newBaseCommitHash)
	// before feat-bu-a was "landed" (deleted).
	// The land command should fetch the updated origin/main first, then rebase feat-bu-b onto it.
	// --- Old Incorrect Simulation ---
	// remoteMainUpdatedRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(baseBranchName), branch1Head.Hash())
	// require.NoError(t, localRepo.Storer.SetReference(remoteMainUpdatedRef))
	// err = localRepo.Push(&git.PushOptions{
	// 	RemoteName: remoteName,
	// 	RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName)},
	// 	Force:      true,
	// })
	// if err != nil && !strings.Contains(err.Error(), "already up-to-date") {
	// 	require.NoError(t, err, "Failed to push merged branch1 head to remote main")
	// }
	// --- End Old Incorrect Simulation ---
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(":refs/heads/" + branch1Name)},
	})
	require.NoError(t, err, "Failed to delete remote branch1")

	// Setup Config
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			Base: baseBranchName,
			Branches: []Branch{
				{Name: branch1Name},
				{Name: branch2Name},
			},
		},
	}, baseBranchName)
	err = SaveConfig(cfg)
	require.NoError(t, err)

	// Checkout a known branch before running land
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(baseBranchName)})
	require.NoError(t, err)
	// Reset local main to initial state to ensure land fetches the update
	err = wt.Reset(&git.ResetOptions{Commit: *initialMainHash, Mode: git.HardReset})
	require.NoError(t, err)

	// Run Land Command
	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})
	output, err := CaptureOutput(func() error { return landCmd.Run(kongCtx) })
	t.Log("Land command output:\n", output)
	require.NoError(t, err, "land command failed")

	// --- Assertions ---
	// 1. Config updated
	loadedConfig, err := LoadConfig()
	require.NoError(t, err)
	currentTower, err := getCurrentTower(loadedConfig.Repos[0])
	require.NoError(t, err)
	require.Len(t, currentTower.Branches, 1)
	require.Equal(t, branch2Name, currentTower.Branches[0].Name)

	// 2. Branch2 rebased onto the NEWLY FETCHED base head (newBaseCommitHash)
	// The base head should now be the hash of the independent update.
	finalBaseCommitHash := newBaseCommitHash // Expect rebase onto the fetched N commit
	branch2Ref, err := localRepo.Reference(plumbing.NewBranchReferenceName(branch2Name), true)
	require.NoError(t, err)
	branch2Commit, err := localRepo.CommitObject(branch2Ref.Hash())
	require.NoError(t, err)
	require.Len(t, branch2Commit.ParentHashes, 1)
	require.Equal(t, finalBaseCommitHash, branch2Commit.ParentHashes[0], "Branch2 should be rebased onto the fetched base head (newBaseCommitHash)")

	// 3. Check that the original base update commit is now an ancestor of branch2 using git command
	// Get the hash of the rebased branch2 head
	branch2HeadHash := branch2Ref.Hash()
	// Use git command for check, as go-git object might be stale after external rebase
	mergeBaseCmd := exec.Command("git", "merge-base", "--is-ancestor", newBaseCommitHash.String(), branch2HeadHash.String())
	mergeBaseCmd.Dir = localRepoPath // Run in the repo directory
	err = mergeBaseCmd.Run()         // Returns exit code 0 if true, 1 if false
	require.NoError(t, err, "git merge-base --is-ancestor check failed; newBaseCommit should be an ancestor of rebased branch2")
}

func TestLandRebaseOntoConflict(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "rebase-conflict-tower"
	branch1Name := "feat-rc-a" // Adds file-a
	branch2Name := "feat-rc-b" // Based on a, adds file-b

	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// Create branches
	createTestBranch(t, localRepo, branch1Name, 1) // Adds file-feat-rc-a-0.txt
	branch1Head, err := localRepo.Reference(plumbing.NewBranchReferenceName(branch1Name), true)
	require.NoError(t, err)
	createTestBranch(t, localRepo, branch2Name, 1) // Adds file-feat-rc-b-0.txt

	// Push initial state
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName),
			config.RefSpec("refs/heads/" + branch1Name + ":refs/heads/" + branch1Name),
			config.RefSpec("refs/heads/" + branch2Name + ":refs/heads/" + branch2Name),
		},
	})
	require.NoError(t, err)

	// Simulate merge of branch1 into main on remote (update main ref)
	remoteMainMergedRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(baseBranchName), branch1Head.Hash())
	require.NoError(t, localRepo.Storer.SetReference(remoteMainMergedRef))
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName)},
		Force:      true,
	})
	if err != nil && !strings.Contains(err.Error(), "already up-to-date") {
		require.NoError(t, err)
	}

	// Add CONFLICTING commit to remote main (modify file added by branch2)
	// Checkout the merged main head locally
	err = wt.Checkout(&git.CheckoutOptions{Hash: branch1Head.Hash()})
	require.NoError(t, err)
	// Modify the file that branch2 created
	conflictingFileName := "file-feat-rc-b-0.txt"
	conflictingFilePath := filepath.Join(localRepoPath, conflictingFileName)
	err = os.WriteFile(conflictingFilePath, []byte("Conflicting change from base"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(conflictingFileName)
	require.NoError(t, err)
	conflictingBaseCommitHash, err := wt.Commit("Base modifies file-b", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)
	// Push this conflicting commit to remote main
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(conflictingBaseCommitHash.String() + ":refs/heads/" + baseBranchName)},
		Force:      true,
	})
	require.NoError(t, err)

	// Delete remote branch1
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(":refs/heads/" + branch1Name)},
	})
	require.NoError(t, err)

	// Setup Config
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			Base: baseBranchName,
			Branches: []Branch{
				{Name: branch1Name},
				{Name: branch2Name},
			},
		},
	}, baseBranchName)
	err = SaveConfig(cfg)
	require.NoError(t, err)

	// Checkout main before running land, reset to ensure base is updated
	mainRef, err := localRepo.Reference(plumbing.NewBranchReferenceName(baseBranchName), true)
	require.NoError(t, err)
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(baseBranchName)})
	require.NoError(t, err)
	err = wt.Reset(&git.ResetOptions{Commit: mainRef.Hash(), Mode: git.HardReset}) // Reset to whatever local main was before update
	require.NoError(t, err)

	// Run Land Command (expecting rebase conflict)
	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})

	output, err := CaptureOutput(func() error { return landCmd.Run(kongCtx) })
	t.Log("Land command output (conflict expected):\n", output)
	require.Error(t, err, "Expected land command to fail due to rebase conflict")
	require.Contains(t, err.Error(), "failed to rebase", "Error message should mention rebase failure")
	require.Contains(t, err.Error(), branch2Name, "Error message should mention conflicting branch")
	require.Contains(t, err.Error(), baseBranchName, "Error message should mention base branch")

	// Verify rebase was aborted (check git status)
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusOutput, statusErr := statusCmd.Output()
	require.NoError(t, statusErr, "Failed to run git status after conflict")
	require.Empty(t, strings.TrimSpace(string(statusOutput)), "Worktree should be clean after aborted rebase")

	// Verify original branch was restored (should be baseBranchName)
	currentBranch, err := getCurrentBranchName(localRepo)
	require.NoError(t, err)
	require.Equal(t, baseBranchName, currentBranch, "Should have checked out original branch (main) after failed rebase")
}

func TestLandSequentialRebaseConflict(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "seq-rebase-conflict-tower"
	branch1Name := "feat-src-a" // Adds file-a
	branch2Name := "feat-src-b" // Based on a, adds shared.txt
	branch3Name := "feat-src-c" // Based on b, modifies shared.txt
	sharedFileName := "shared-seq.txt"

	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// --- Create Branches ---
	// Branch 1
	createTestBranch(t, localRepo, branch1Name, 1) // Adds file-feat-src-a-0.txt
	branch1Head, err := localRepo.Reference(plumbing.NewBranchReferenceName(branch1Name), true)
	require.NoError(t, err)

	// Branch 2 (Adds shared file based on branch 1)
	createBranchFromHead(t, localRepo, branch2Name)
	sharedFilePath := filepath.Join(localRepoPath, sharedFileName)
	err = os.WriteFile(sharedFilePath, []byte("Content from B"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(sharedFileName)
	require.NoError(t, err)
	branch2OriginalHeadHash, err := wt.Commit("Branch B adds shared file", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)

	// Branch 3 (Modifies shared file based on original B)
	createBranchFromHead(t, localRepo, branch3Name)
	err = os.WriteFile(sharedFilePath, []byte("Content from C"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(sharedFileName)
	require.NoError(t, err)
	_, err = wt.Commit("Branch C modifies shared file", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)

	// Go back to Branch 2 and add a conflicting change AFTER branch 3 was created
	err = wt.Checkout(&git.CheckoutOptions{Hash: branch2OriginalHeadHash}) // Checkout original B head
	require.NoError(t, err)
	// Reset the branch ref to ensure the new commit extends this specific branch
	branch2Ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branch2Name), branch2OriginalHeadHash)
	err = localRepo.Storer.SetReference(branch2Ref)
	require.NoError(t, err)
	// Make the conflicting change
	err = os.WriteFile(sharedFilePath, []byte("Content from B v2"), 0644) // Conflicting change
	require.NoError(t, err)
	_, err = wt.Add(sharedFileName)
	require.NoError(t, err)
	branch2UpdatedHeadHash, err := wt.Commit("Branch B modifies shared file v2 (CONFLICT)", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)
	// Ensure the local branch 2 ref points to the updated commit
	branch2UpdatedRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branch2Name), branch2UpdatedHeadHash)
	err = localRepo.Storer.SetReference(branch2UpdatedRef)
	require.NoError(t, err)

	// --- Push Initial State ---
	// Make sure to push the *updated* branch 2 head
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName),
			config.RefSpec("refs/heads/" + branch1Name + ":refs/heads/" + branch1Name),
			config.RefSpec("refs/heads/" + branch2Name + ":refs/heads/" + branch2Name), // Push updated branch2
			config.RefSpec("refs/heads/" + branch3Name + ":refs/heads/" + branch3Name),
		},
		Force: true, // Force push potentially updated branches
	})
	require.NoError(t, err)

	// --- Simulate merge of branch1 into main on remote ---
	remoteMainMergedRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(baseBranchName), branch1Head.Hash())
	require.NoError(t, localRepo.Storer.SetReference(remoteMainMergedRef))
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName)},
		Force:      true,
	})
	if err != nil && !strings.Contains(err.Error(), "already up-to-date") {
		require.NoError(t, err)
	}

	// --- Delete remote branch1 ---
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(":refs/heads/" + branch1Name)},
	})
	require.NoError(t, err)

	// --- Setup Config ---
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			Base: baseBranchName,
			Branches: []Branch{
				{Name: branch1Name},
				{Name: branch2Name},
				{Name: branch3Name},
			},
		},
	}, baseBranchName)
	err = SaveConfig(cfg)
	require.NoError(t, err)

	// --- Run Land Command ---
	// Checkout main before running land
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(baseBranchName)})
	require.NoError(t, err)

	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})

	output, err := CaptureOutput(func() error { return landCmd.Run(kongCtx) })
	t.Log("Land command output (seq conflict expected):\n", output)
	require.Error(t, err, "Expected land command to fail due to sequential rebase conflict")
	// Check for rebaseTower's specific error message wrapper
	require.Contains(t, err.Error(), "failed during sequential rebase of remaining tower branches", "Error message should mention sequential rebase failure")

	// --- Verify State ---
	// Verify rebase was aborted (check git status)
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = localRepoPath // Make sure it runs in the repo dir
	statusOutput, statusErr := statusCmd.Output()
	require.NoError(t, statusErr, "Failed to run git status after conflict")
	require.Empty(t, strings.TrimSpace(string(statusOutput)), "Worktree should be clean after aborted rebase")

	// Verify a branch was restored and the repo is in a clean state
	currentBranch, err := getCurrentBranchName(localRepo)
	require.NoError(t, err)
	// In this case, we expect branch2Name as that's what was checked out during the land sequence
	require.Equal(t, branch2Name, currentBranch, "Should have restored to branch2 after failed sequential rebase")

	// Verify that branch2 has a valid reference after the rebase
	branch2RefPostLand, err := localRepo.Reference(plumbing.NewBranchReferenceName(branch2Name), true)
	require.NoError(t, err, "Branch2 should still exist after the rebase")
	branch2CommitPostLand, err := localRepo.CommitObject(branch2RefPostLand.Hash())
	require.NoError(t, err)
	require.Len(t, branch2CommitPostLand.ParentHashes, 1, "Branch2 should have exactly one parent after rebase")

	// We don't need to check the exact hash, just verify we can get the parent commit
	_, err = localRepo.CommitObject(branch2CommitPostLand.ParentHashes[0])
	require.NoError(t, err, "Branch2 should have a valid parent commit after rebase")

	// And check that the rebased branch2 still contains the 'v2' content
	assertBranchContainsContent(t, localRepo, branch2Name, sharedFileName, "Content from B v2")
}

func TestLandLastBranch(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "last-branch-tower"
	branchName := "feat-last"

	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// Create and push branches
	createTestBranch(t, localRepo, branchName, 1)
	branchHead, err := localRepo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err)
	// createTestBranch(t, localRepo, branch2Name, 1) // Erroneously added line

	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName),
			config.RefSpec("refs/heads/" + branchName + ":refs/heads/" + branchName),
		},
	})
	require.NoError(t, err)

	// Simulate merge and delete of branch on remote
	remoteMainMergedRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(baseBranchName), branchHead.Hash())
	require.NoError(t, localRepo.Storer.SetReference(remoteMainMergedRef))
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName)},
		Force:      true,
	})
	if err != nil && !strings.Contains(err.Error(), "already up-to-date") {
		require.NoError(t, err)
	}
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(":refs/heads/" + branchName)},
	})
	require.NoError(t, err)

	// Setup Config with only one branch
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			Base: baseBranchName,
			Branches: []Branch{
				{Name: branchName},
			},
		},
	}, baseBranchName)
	err = SaveConfig(cfg)
	require.NoError(t, err)

	// Checkout main before running land
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(baseBranchName)})
	require.NoError(t, err)

	// Run Land Command
	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})
	output, err := CaptureOutput(func() error { return landCmd.Run(kongCtx) })
	t.Log("Land command output (last branch):\n", output)
	require.NoError(t, err, "land command failed for last branch")

	// --- Assertions ---
	// 1. Config updated: tower should be empty
	loadedConfig, err := LoadConfig()
	require.NoError(t, err)
	currentTower, err := getCurrentTower(loadedConfig.Repos[0])
	require.NoError(t, err)
	require.Len(t, currentTower.Branches, 0, "Tower should be empty after landing the last branch")

	// 2. Base branch should be updated to the landed branch's head
	mainRef, err := localRepo.Reference(plumbing.NewBranchReferenceName(baseBranchName), true)
	require.NoError(t, err)
	require.Equal(t, branchHead.Hash(), mainRef.Hash(), "Main branch head should be the head of the landed branch")
}

// TestLandSequenceWithSharedFile simulates landing multiple branches in sequence
// where each branch modifies the same line of the same file.
func TestLandSequenceWithSharedFile(t *testing.T) {
	remoteName := "origin"
	baseBranchName := "main"
	towerName := "shared-file-tower"
	branchAName := "feat-sfs-a"
	branchBName := "feat-sfs-b"
	branchCName := "feat-sfs-c"
	sharedFileName := "shared.txt"

	localRepoPath, localRepo, _, _, cleanup := setupTestEnvWithRemote(t, remoteName)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// --- Initial Setup ---
	// Add base file to main
	initialContent := "Version 0"
	sharedFilePath := filepath.Join(localRepoPath, sharedFileName)
	err = os.WriteFile(sharedFilePath, []byte(initialContent), 0644)
	require.NoError(t, err)
	_, err = wt.Add(sharedFileName)
	require.NoError(t, err)
	mainHeadHash, err := wt.Commit("Add shared.txt", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)

	// --- Create Branches ---
	// Branch A
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Hash: mainHeadHash}))
	createBranchFromHead(t, localRepo, branchAName)
	err = os.WriteFile(sharedFilePath, []byte("Version A"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(sharedFileName)
	require.NoError(t, err)
	branchAHeadHash, err := wt.Commit("Update shared.txt to A", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)

	// Branch B
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Hash: branchAHeadHash}))
	createBranchFromHead(t, localRepo, branchBName)
	err = os.WriteFile(sharedFilePath, []byte("Version B"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(sharedFileName)
	require.NoError(t, err)
	branchBHeadHash, err := wt.Commit("Update shared.txt to B", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)

	// Branch C
	require.NoError(t, wt.Checkout(&git.CheckoutOptions{Hash: branchBHeadHash}))
	createBranchFromHead(t, localRepo, branchCName)
	err = os.WriteFile(sharedFilePath, []byte("Version C"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(sharedFileName)
	require.NoError(t, err)
	_, err = wt.Commit("Update shared.txt to C", &git.CommitOptions{Author: &object.Signature{Name: "Test"}})
	require.NoError(t, err)

	// --- Push Initial State ---
	err = localRepo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs: []config.RefSpec{
			config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName),
			config.RefSpec("refs/heads/" + branchAName + ":refs/heads/" + branchAName),
			config.RefSpec("refs/heads/" + branchBName + ":refs/heads/" + branchBName),
			config.RefSpec("refs/heads/" + branchCName + ":refs/heads/" + branchCName),
		},
	})
	require.NoError(t, err)

	// --- Setup Config ---
	cfg := createTestConfig(t, localRepoPath, towerName, []*Tower{
		{
			Name: towerName,
			Base: baseBranchName,
			Branches: []Branch{
				{Name: branchAName},
				{Name: branchBName},
				{Name: branchCName},
			},
		},
	}, baseBranchName)
	err = SaveConfig(cfg)
	require.NoError(t, err)

	// --- Land Branch A ---
	t.Log("--- Landing Branch A ---")
	// Simulate merge & delete on remote
	simulateMergeAndDelete(t, localRepo, remoteName, baseBranchName, branchAName, branchAHeadHash)
	// Run land
	landCmd := &LandCmd{Remote: remoteName}
	parser := kong.Must(&CLI{})
	kongCtx, _ := parser.Parse([]string{"land"})
	// Checkout base branch first
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(baseBranchName)})
	require.NoError(t, err)
	output, err := CaptureOutput(func() error { return landCmd.Run(kongCtx) })
	t.Log(output)
	require.NoError(t, err, "Land A failed")
	// Assert state after landing A
	loadedConfig, err := LoadConfig()
	require.NoError(t, err)
	currentTower, err := getCurrentTower(loadedConfig.Repos[0])
	require.NoError(t, err)
	require.Len(t, currentTower.Branches, 2, "Tower should have B and C left")
	require.Equal(t, branchBName, currentTower.Branches[0].Name)
	require.Equal(t, branchCName, currentTower.Branches[1].Name)
	assertBranchRebasedOnto(t, localRepo, branchBName, branchAHeadHash) // B should be based on A's commit (which is now main)
	assertBranchContainsContent(t, localRepo, branchCName, sharedFileName, "Version C")
	assertBranchParent(t, localRepo, branchCName, branchBName) // C should still be based on B (after B was rebased)

	// --- Land Branch B ---
	t.Log("--- Landing Branch B ---")
	// Simulate merge & delete on remote
	branchBRef, err := localRepo.Reference(plumbing.NewBranchReferenceName(branchBName), true)
	require.NoError(t, err)
	simulateMergeAndDelete(t, localRepo, remoteName, baseBranchName, branchBName, branchBRef.Hash())
	// Run land
	landCmd = &LandCmd{Remote: remoteName}
	parser = kong.Must(&CLI{})
	kongCtx, _ = parser.Parse([]string{"land"})
	// Checkout base branch first
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(baseBranchName)})
	require.NoError(t, err)
	output, err = CaptureOutput(func() error { return landCmd.Run(kongCtx) })
	t.Log(output)
	require.NoError(t, err, "Land B failed")
	// Assert state after landing B
	loadedConfig, err = LoadConfig()
	require.NoError(t, err)
	currentTower, err = getCurrentTower(loadedConfig.Repos[0])
	require.NoError(t, err)
	require.Len(t, currentTower.Branches, 1, "Tower should have C left")
	require.Equal(t, branchCName, currentTower.Branches[0].Name)
	assertBranchRebasedOnto(t, localRepo, branchCName, branchBRef.Hash()) // C should be based on B's commit (which is now main)
	assertBranchContainsContent(t, localRepo, branchCName, sharedFileName, "Version C")

	// --- Land Branch C ---
	t.Log("--- Landing Branch C ---")
	// Simulate merge & delete on remote
	branchCRef, err := localRepo.Reference(plumbing.NewBranchReferenceName(branchCName), true)
	require.NoError(t, err)
	simulateMergeAndDelete(t, localRepo, remoteName, baseBranchName, branchCName, branchCRef.Hash())
	// Run land
	landCmd = &LandCmd{Remote: remoteName}
	parser = kong.Must(&CLI{})
	kongCtx, _ = parser.Parse([]string{"land"})
	// Checkout base branch first
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(baseBranchName)})
	require.NoError(t, err)
	output, err = CaptureOutput(func() error { return landCmd.Run(kongCtx) })
	t.Log(output)
	require.NoError(t, err, "Land C failed")
	// Assert final state
	loadedConfig, err = LoadConfig()
	require.NoError(t, err)
	currentTower, err = getCurrentTower(loadedConfig.Repos[0])
	require.NoError(t, err)
	require.Len(t, currentTower.Branches, 0, "Tower should be empty")
	assertBranchContainsContent(t, localRepo, baseBranchName, sharedFileName, "Version C") // Main should have final version
}

// --- Helper Functions for Shared File Test ---

// createBranchFromHead creates a new branch pointing to the current HEAD
func createBranchFromHead(t *testing.T, repo *git.Repository, branchName string) {
	t.Helper()
	headRef, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), headRef.Hash())
	err = repo.Storer.SetReference(branchRef)
	require.NoError(t, err)
	// Checkout the new branch
	wt, err := repo.Worktree()
	require.NoError(t, err)
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branchName)})
	require.NoError(t, err)
}

// simulateMergeAndDelete simulates the remote operation of merging a branch and deleting it.
func simulateMergeAndDelete(t *testing.T, repo *git.Repository, remoteName, baseBranchName, branchToLandName string, branchToLandHead plumbing.Hash) {
	t.Helper()
	// Update remote base branch ref to the landed branch head
	remoteBaseRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(baseBranchName), branchToLandHead)
	require.NoError(t, repo.Storer.SetReference(remoteBaseRef))
	err := repo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec("refs/heads/" + baseBranchName + ":refs/heads/" + baseBranchName)},
		Force:      true,
	})
	if err != nil && !strings.Contains(err.Error(), "already up-to-date") {
		require.NoError(t, err, "Failed to push merged head to remote base: %s", baseBranchName)
	}

	// Delete remote landed branch
	err = repo.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   []config.RefSpec{config.RefSpec(":refs/heads/" + branchToLandName)},
	})
	require.NoError(t, err, "Failed to delete remote branch: %s", branchToLandName)
}

// assertBranchRebasedOnto checks if the parent of the branch head matches the expected base hash.
func assertBranchRebasedOnto(t *testing.T, repo *git.Repository, branchName string, expectedBaseHash plumbing.Hash) {
	t.Helper()
	branchRef, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err)
	branchCommit, err := repo.CommitObject(branchRef.Hash())
	require.NoError(t, err)
	require.Len(t, branchCommit.ParentHashes, 1, "Branch %s should have exactly one parent after rebase", branchName)
	require.Equal(t, expectedBaseHash, branchCommit.ParentHashes[0], "Branch %s should be rebased onto %s", branchName, expectedBaseHash.String()[:8])
}

// assertBranchContainsContent checks the content of a specific file on a specific branch head.
func assertBranchContainsContent(t *testing.T, repo *git.Repository, branchName, filePath, expectedContent string) {
	t.Helper()
	branchRef, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err)
	commit, err := repo.CommitObject(branchRef.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	file, err := tree.File(filePath) // Use relative path within repo
	require.NoError(t, err, "File %s not found on branch %s", filePath, branchName)
	content, err := file.Contents()
	require.NoError(t, err)
	require.Equal(t, expectedContent, content, "File %s content mismatch on branch %s", filePath, branchName)
}

// assertBranchParent checks if the parent of branch A is the head of branch B.
func assertBranchParent(t *testing.T, repo *git.Repository, branchAName, expectedParentBranchName string) {
	t.Helper()
	parentRef, err := repo.Reference(plumbing.NewBranchReferenceName(expectedParentBranchName), true)
	require.NoError(t, err, "Could not get ref for expected parent branch %s", expectedParentBranchName)
	assertBranchRebasedOnto(t, repo, branchAName, parentRef.Hash())
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
