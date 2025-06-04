package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

// setupSyncTestEnv creates a bare "remote" repo and a local clone.
// It mocks the config path and initializes a basic ghenga config pointing to the local repo.
// It changes the CWD to the local repo dir.
func setupSyncTestEnv(t *testing.T) (remoteRepoPath, localRepoPath string, localRepo *git.Repository, cleanup func()) {
	t.Helper()

	// 1. Create top-level temp dir
	baseTempDir, err := os.MkdirTemp("", "ghenga-sync-test-*")
	require.NoError(t, err)

	// 2. Create bare remote repo
	remoteRepoPath = filepath.Join(baseTempDir, "remote.git")
	cmd := exec.Command("git", "init", "--bare", remoteRepoPath)
	err = cmd.Run()
	require.NoError(t, err, "Failed to init bare remote repo")

	// 3. Clone remote to create local repo
	localRepoPath = filepath.Join(baseTempDir, "local_repo")
	cmd = exec.Command("git", "clone", remoteRepoPath, localRepoPath)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "Failed to clone remote repo: %s", string(output))

	// Change CWD to local repo
	originalWd, err := os.Getwd()
	require.NoError(t, err)
	err = os.Chdir(localRepoPath)
	require.NoError(t, err)

	// Open the local repo
	localRepo, err = git.PlainOpen(localRepoPath)
	require.NoError(t, err)
	wt, err := localRepo.Worktree()
	require.NoError(t, err)

	// 4. Add initial commit and push to establish main/master
	initialFileName := "initial.txt"
	err = os.WriteFile(filepath.Join(localRepoPath, initialFileName), []byte("initial content"), 0644)
	require.NoError(t, err)
	_, err = wt.Add(initialFileName)
	require.NoError(t, err)
	_, err = wt.Commit("Initial commit", &git.CommitOptions{
		Author: defaultSignatureForTest(),
	})
	require.NoError(t, err)

	// Determine default branch name (main or master)
	defaultBranchRef := plumbing.NewBranchReferenceName("main")
	_, err = localRepo.Reference(defaultBranchRef, false)
	if err != nil { // Assume master if main doesn't exist
		defaultBranchRef = plumbing.NewBranchReferenceName("master")
	}

	err = localRepo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec(defaultBranchRef.String() + ":" + defaultBranchRef.String())},
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err, "Failed to push initial commit")
	}

	// 5. Mock config path
	configTempDir, err := os.MkdirTemp(baseTempDir, "config-*")
	require.NoError(t, err)
	configFile := filepath.Join(configTempDir, "config.toml")
	oldConfigPath := ConfigPath
	ConfigPath = mockedConfigPathForTest(configFile)

	// 6. Create initial empty ghenga config
	cfg := &Config{
		Repos: []*RepoInfo{
			{
				Path:   localRepoPath,
				Towers: []*Tower{},
			},
		},
	}
	err = SaveConfig(cfg)
	require.NoError(t, err)

	cleanup = func() {
		ConfigPath = oldConfigPath
		os.Chdir(originalWd)
		os.RemoveAll(baseTempDir)
	}

	return remoteRepoPath, localRepoPath, localRepo, cleanup
}

// --- Test Helpers (adapted from rebase_test.go or new) ---

func defaultSignatureForTest() *object.Signature {
	return &object.Signature{
		Name:  "Test User",
		Email: "test@example.com",
		When:  time.Now(),
	}
}

func mockedConfigPathForTest(path string) ConfigPathFunc {
	return func() (string, error) {
		return path, nil
	}
}

func getRemoteHeadHash(t *testing.T, remoteRepoPath string, branchName string) (plumbing.Hash, error) {
	t.Helper()
	r, err := git.PlainOpen(remoteRepoPath)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to open bare remote repo: %w", err)
	}
	refName := plumbing.NewBranchReferenceName(branchName)
	ref, err := r.Reference(refName, true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get reference %s from remote: %w", refName, err)
	}
	return ref.Hash(), nil
}

// commitDirectlyToRemote simulates a commit pushed by someone else.
// It clones the bare remote to a temporary location, makes a commit, pushes, and cleans up.
func commitDirectlyToRemote(t *testing.T, remoteRepoPath string, branchName string, filename string, content string, message string) plumbing.Hash {
	t.Helper()

	// Clone the bare repo to a temporary directory
	tempCloneDir, err := os.MkdirTemp("", "ghenga-remote-clone-*")
	require.NoError(t, err)
	defer os.RemoveAll(tempCloneDir)

	cloneCmd := exec.Command("git", "clone", remoteRepoPath, tempCloneDir)
	output, err := cloneCmd.CombinedOutput()
	require.NoError(t, err, "Failed to clone remote for direct commit: %s", string(output))

	// Open the temporary clone
	tempRepo, err := git.PlainOpen(tempCloneDir)
	require.NoError(t, err)
	tempWt, err := tempRepo.Worktree()
	require.NoError(t, err)

	// Checkout the target branch (it must exist remotely)
	checkoutCmd := exec.Command("git", "checkout", branchName)
	checkoutCmd.Dir = tempCloneDir
	output, err = checkoutCmd.CombinedOutput()
	require.NoError(t, err, "Failed to checkout branch %s in temp remote clone: %s", branchName, string(output))

	// Add the commit in the temporary clone
	remoteCommitHash := addSingleCommit(t, tempCloneDir, tempWt, filename, content, message)

	// Push the commit back to the bare remote
	pushCmd := exec.Command("git", "push", "origin", branchName)
	pushCmd.Dir = tempCloneDir
	output, err = pushCmd.CombinedOutput()
	require.NoError(t, err, "Failed to push direct commit from temp clone: %s", string(output))

	return remoteCommitHash
}

func TestSyncNoRemote(t *testing.T) {
	remoteRepoPath, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	// 1. Create a local branch and add a commit
	branchName := "feature-a"
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   headRef.Hash(),
		Branch: plumbing.NewBranchReferenceName(branchName),
		Create: true,
	})
	require.NoError(t, err, "Failed to checkout new branch")

	_ = addSingleCommit(t, localRepoPath, wt, "feat-a.txt", "content a", "Commit for feature A")

	// 2. Setup ghenga config
	towerName := "my-tower"
	config, err := LoadConfig()
	require.NoError(t, err)

	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name: towerName,
			Branches: []Branch{
				{Name: branchName},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// 3. Run the sync command (no confirmation needed as no actions performed)
	syncCmd := &SyncDoCmd{Remote: "origin"}
	output, err := CaptureOutput(func() error {
		return syncCmd.Run(nil)
	})
	require.NoError(t, err, "ghenga sync failed. Output:\n%s", output)

	t.Logf("Sync output:\n%s", output)

	// 4. Assertions
	// Check output for skip message
	require.Contains(t, output, fmt.Sprintf("Branch '%s': Does not exist on remote 'origin'. Skipping push actions", branchName),
		"Output should indicate skipping due to no remote")
	require.NotContains(t, output, "Marked for normal push", "Output should not mention normal push")
	require.NotContains(t, output, "Executing", "Output should not mention executing any actions")

	// Check remote repo state (branch should NOT exist)
	_, err = getRemoteHeadHash(t, remoteRepoPath, branchName)
	require.Error(t, err, "Branch should not exist on remote")
	require.ErrorContains(t, err, "failed to get reference", "Error should be about missing reference")
}

func TestSyncLocalAhead(t *testing.T) {
	remoteRepoPath, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	// 1. Create a local branch, add a commit, and PUSH it
	branchName := "feature-ahead"
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   headRef.Hash(),
		Branch: plumbing.NewBranchReferenceName(branchName),
		Create: true,
	})
	require.NoError(t, err, "Failed to checkout new branch")

	firstCommitHash := addSingleCommit(t, localRepoPath, wt, "feat-ahead-1.txt", "content 1", "First commit feature Ahead")

	// Push the initial commit
	localRefName := plumbing.NewBranchReferenceName(branchName)
	pushRefSpec := fmt.Sprintf("%s:%s", localRefName.String(), localRefName.String())
	err = localRepo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec(pushRefSpec)},
	})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err, "Failed to push initial commit")
	}

	// 2. Add a second commit LOCALLY ONLY
	secondCommitHash := addSingleCommit(t, localRepoPath, wt, "feat-ahead-2.txt", "content 2", "Second commit feature Ahead")

	// 3. Setup ghenga config
	towerName := "my-tower-ahead"
	config, err := LoadConfig()
	require.NoError(t, err)

	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name: towerName,
			Branches: []Branch{
				{Name: branchName},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// 4. Run the sync command (mocking confirmation)
	restoreStdin := mockInput("y")
	defer restoreStdin()

	syncCmd := &SyncDoCmd{Remote: "origin"}
	output, err := CaptureOutput(func() error {
		return syncCmd.Run(nil)
	})
	require.NoError(t, err, "ghenga sync failed. Output:\n%s", output)

	t.Logf("Sync output:\n%s", output)

	// 5. Assertions
	// Check output for normal push message
	require.Contains(t, output, fmt.Sprintf("Marked for normal push (ahead of remote 'origin'"),
		"Output should indicate normal push")
	require.Contains(t, output, fmt.Sprintf("Successfully pushed branch '%s'", branchName),
		"Output should confirm successful normal push")
	require.NotContains(t, output, "Executing force-pushes", "Output should not mention force push")
	require.NotContains(t, output, "Executing pulls", "Output should not mention pull")

	// Check remote repo state
	remoteHeadHash, err := getRemoteHeadHash(t, remoteRepoPath, branchName)
	require.NoError(t, err, "Failed to get remote head for %s", branchName)
	require.Equal(t, secondCommitHash, remoteHeadHash, "Remote head should match the second local commit after sync")
	require.NotEqual(t, firstCommitHash, remoteHeadHash, "Remote head should not match the first local commit")
}

func TestSyncRemoteAhead(t *testing.T) {
	remoteRepoPath, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	// 1. Create branch, commit, push (establish baseline)
	branchName := "feature-remote"
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   headRef.Hash(),
		Branch: plumbing.NewBranchReferenceName(branchName),
		Create: true,
	})
	require.NoError(t, err)
	localCommitHash1 := addSingleCommit(t, localRepoPath, wt, "feat-remote-1.txt", "content 1", "First commit remote ahead")
	pushRefSpec := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(branchName).String(), plumbing.NewBranchReferenceName(branchName).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpec)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}

	// 2. Add a commit DIRECTLY TO REMOTE (simulate someone else pushing)
	remoteCommitHash2 := commitDirectlyToRemote(t, remoteRepoPath, branchName, "feat-remote-2.txt", "content 2", "Second commit on remote")
	require.NotEqual(t, localCommitHash1, remoteCommitHash2, "Remote commit should be different")

	// 2.5 Fetch remote changes so local repo knows about the remote commit
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = localRepoPath // Run in local repo
	fetchOutput, err := fetchCmd.CombinedOutput()
	require.NoError(t, err, "Failed to fetch origin: %s", string(fetchOutput))

	// 3. Setup ghenga config
	towerName := "my-tower-remote"
	config, err := LoadConfig()
	require.NoError(t, err)
	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name:     towerName,
			Branches: []Branch{{Name: branchName}},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// 4. Run the sync command (mocking confirmation)
	restoreStdin := mockInput("y")
	defer restoreStdin()

	syncCmd := &SyncDoCmd{Remote: "origin"}
	output, err := CaptureOutput(func() error {
		return syncCmd.Run(nil)
	})
	require.NoError(t, err, "ghenga sync failed. Output:\n%s", output)

	t.Logf("Sync output:\n%s", output)

	// 5. Assertions
	// Check output for pull message
	require.Contains(t, output, fmt.Sprintf("Marked for pull (remote 'origin' is ahead"),
		"Output should indicate pull needed")
	require.Contains(t, output, fmt.Sprintf("Successfully pulled branch '%s'", branchName),
		"Output should confirm successful pull")
	require.Contains(t, output, "Fast-forward", "Pull output should mention Fast-forward") // Check ff-only worked
	require.NotContains(t, output, "Executing force-pushes", "Output should not mention force push")
	require.NotContains(t, output, "Executing normal pushes", "Output should not mention normal push")

	// Check local repo state
	localRef, err := localRepo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err, "Failed to get local head for %s after pull", branchName)
	require.Equal(t, remoteCommitHash2, localRef.Hash(), "Local head should match the second remote commit after sync pull")
}

func TestSyncDiverged(t *testing.T) {
	remoteRepoPath, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	// 1. Create branch, commit, push (establish baseline)
	branchName := "feature-diverged"
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   headRef.Hash(),
		Branch: plumbing.NewBranchReferenceName(branchName),
		Create: true,
	})
	require.NoError(t, err)
	baseCommitHash := addSingleCommit(t, localRepoPath, wt, "feat-diverged-base.txt", "content base", "Base commit diverged")
	pushRefSpec := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(branchName).String(), plumbing.NewBranchReferenceName(branchName).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpec)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}

	// 2. Add a commit LOCALLY
	localCommitHash := addSingleCommit(t, localRepoPath, wt, "feat-diverged-local.txt", "content local", "Local commit diverged")
	require.NotEqual(t, baseCommitHash, localCommitHash)

	// 3. Add a DIFFERENT commit DIRECTLY TO REMOTE
	remoteCommitHash := commitDirectlyToRemote(t, remoteRepoPath, branchName, "feat-diverged-remote.txt", "content remote", "Remote commit diverged")
	require.NotEqual(t, baseCommitHash, remoteCommitHash)
	require.NotEqual(t, localCommitHash, remoteCommitHash)

	// 4. Fetch remote changes
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = localRepoPath
	fetchOutput, err := fetchCmd.CombinedOutput()
	require.NoError(t, err, "Failed to fetch origin: %s", string(fetchOutput))

	// 5. Setup ghenga config
	towerName := "my-tower-diverged"
	config, err := LoadConfig()
	require.NoError(t, err)
	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name:     towerName,
			Branches: []Branch{{Name: branchName}},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// 6. Run the sync command (mocking confirmation)
	restoreStdin := mockInput("y")
	defer restoreStdin()

	syncCmd := &SyncDoCmd{Remote: "origin"}
	output, err := CaptureOutput(func() error {
		return syncCmd.Run(nil)
	})
	require.NoError(t, err, "ghenga sync failed. Output:\n%s", output)

	t.Logf("Sync output:\n%s", output)

	// 7. Assertions
	// Check output for force-push message
	require.Contains(t, output, fmt.Sprintf("Marked for force-push (diverged from remote 'origin'"),
		"Output should indicate force-push needed")
	require.Contains(t, output, fmt.Sprintf("Successfully force-pushed branch '%s'", branchName),
		"Output should confirm successful force-push")
	require.NotContains(t, output, "Executing normal pushes", "Output should not mention normal push")
	require.NotContains(t, output, "Executing pulls", "Output should not mention pull")

	// Check remote repo state
	remoteHeadHash, err := getRemoteHeadHash(t, remoteRepoPath, branchName)
	require.NoError(t, err, "Failed to get remote head for %s after sync", branchName)
	require.Equal(t, localCommitHash, remoteHeadHash, "Remote head should match the local commit after sync force-push")

	// Check local repo state (should be unchanged by force-push)
	localRef, err := localRepo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err, "Failed to get local head for %s after sync", branchName)
	require.Equal(t, localCommitHash, localRef.Hash(), "Local head should remain the same after sync force-push")
}

func TestSyncUpToDate(t *testing.T) {
	remoteRepoPath, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	// 1. Create branch, commit, push
	branchName := "feature-uptodate"
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   headRef.Hash(),
		Branch: plumbing.NewBranchReferenceName(branchName),
		Create: true,
	})
	require.NoError(t, err)
	commitHash := addSingleCommit(t, localRepoPath, wt, "feat-uptodate.txt", "content", "Commit up-to-date")
	pushRefSpec := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(branchName).String(), plumbing.NewBranchReferenceName(branchName).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpec)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}

	// 2. Setup ghenga config
	towerName := "my-tower-uptodate"
	config, err := LoadConfig()
	require.NoError(t, err)
	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name:     towerName,
			Branches: []Branch{{Name: branchName}},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// 3. Run the sync command (no confirmation needed)
	syncCmd := &SyncDoCmd{Remote: "origin"}
	output, err := CaptureOutput(func() error {
		return syncCmd.Run(nil)
	})
	require.NoError(t, err, "ghenga sync failed. Output:\n%s", output)

	t.Logf("Sync output:\n%s", output)

	// 4. Assertions
	require.Contains(t, output, fmt.Sprintf("Branch '%s': Up-to-date with remote 'origin'. Skipping.", branchName),
		"Output should indicate skipping due to up-to-date")
	require.Contains(t, output, "No branches require pulling or pushing.", "Output should indicate no actions needed")
	require.NotContains(t, output, "Executing", "Output should not mention executing any actions")

	// Check remote and local repo state (should be identical)
	remoteHeadHash, err := getRemoteHeadHash(t, remoteRepoPath, branchName)
	require.NoError(t, err)
	localRef, err := localRepo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err)
	require.Equal(t, commitHash, remoteHeadHash, "Remote head should match commit")
	require.Equal(t, commitHash, localRef.Hash(), "Local head should match commit")
}

func TestSyncMixedTower(t *testing.T) {
	remoteRepoPath, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)
	baseHash := headRef.Hash()

	// --- Branch Setups ---
	// 1. No Remote Branch ("no-remote")
	brNoRemote := "no-remote"
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseHash, Branch: plumbing.NewBranchReferenceName(brNoRemote), Create: true})
	require.NoError(t, err)
	addSingleCommit(t, localRepoPath, wt, "no-remote.txt", "nr", "Commit No Remote")

	// 2. Local Ahead Branch ("local-ahead")
	brLocalAhead := "local-ahead"
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseHash, Branch: plumbing.NewBranchReferenceName(brLocalAhead), Create: true})
	require.NoError(t, err)
	_ = addSingleCommit(t, localRepoPath, wt, "local-ahead-1.txt", "la1", "Commit Local Ahead 1")
	pushRefSpecLA := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(brLocalAhead).String(), plumbing.NewBranchReferenceName(brLocalAhead).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpecLA)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}
	commitLocalAhead2 := addSingleCommit(t, localRepoPath, wt, "local-ahead-2.txt", "la2", "Commit Local Ahead 2")

	// 3. Remote Ahead Branch ("remote-ahead")
	brRemoteAhead := "remote-ahead"
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseHash, Branch: plumbing.NewBranchReferenceName(brRemoteAhead), Create: true})
	require.NoError(t, err)
	_ = addSingleCommit(t, localRepoPath, wt, "remote-ahead-1.txt", "ra1", "Commit Remote Ahead 1")
	pushRefSpecRA := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(brRemoteAhead).String(), plumbing.NewBranchReferenceName(brRemoteAhead).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpecRA)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}
	commitRemoteAhead2 := commitDirectlyToRemote(t, remoteRepoPath, brRemoteAhead, "remote-ahead-2.txt", "ra2", "Commit Remote Ahead 2")

	// 4. Diverged Branch ("diverged")
	brDiverged := "diverged"
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseHash, Branch: plumbing.NewBranchReferenceName(brDiverged), Create: true})
	require.NoError(t, err)
	_ = addSingleCommit(t, localRepoPath, wt, "diverged-base.txt", "db", "Commit Diverged Base")
	pushRefSpecD := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(brDiverged).String(), plumbing.NewBranchReferenceName(brDiverged).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpecD)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}
	commitDivergedLocal := addSingleCommit(t, localRepoPath, wt, "diverged-local.txt", "dl", "Commit Diverged Local")
	_ = commitDirectlyToRemote(t, remoteRepoPath, brDiverged, "diverged-remote.txt", "dr", "Commit Diverged Remote")

	// 5. Up-to-date Branch ("uptodate")
	brUpToDate := "uptodate"
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseHash, Branch: plumbing.NewBranchReferenceName(brUpToDate), Create: true})
	require.NoError(t, err)
	commitUpToDate := addSingleCommit(t, localRepoPath, wt, "uptodate.txt", "utd", "Commit Up To Date")
	pushRefSpecUTD := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(brUpToDate).String(), plumbing.NewBranchReferenceName(brUpToDate).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpecUTD)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}

	// Fetch all remote changes before running sync
	err = wt.Checkout(&git.CheckoutOptions{Branch: headRef.Name()}) // Go to a known branch
	require.NoError(t, err)
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchOutput, err := fetchCmd.CombinedOutput()
	require.NoError(t, err, "Failed to fetch origin: %s", string(fetchOutput))

	// --- Setup Ghenga Config ---
	towerName := "mixed-tower"
	config, err := LoadConfig()
	require.NoError(t, err)
	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name: towerName,
			Branches: []Branch{
				{Name: brNoRemote},
				{Name: brLocalAhead},
				{Name: brRemoteAhead},
				{Name: brDiverged},
				{Name: brUpToDate},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// --- Run Sync ---
	restoreStdin := mockInput("y")
	defer restoreStdin()

	syncCmd := &SyncDoCmd{Remote: "origin"}
	output, err := CaptureOutput(func() error {
		return syncCmd.Run(nil)
	})
	require.NoError(t, err, "ghenga sync failed. Output:\n%s", output)

	t.Logf("Sync output:\n%s", output)

	// --- Assertions ---
	// Check output for specific branch statuses
	require.Contains(t, output, fmt.Sprintf("Branch '%s': Does not exist on remote", brNoRemote))
	require.Contains(t, output, fmt.Sprintf("Branch '%s': Marked for normal push", brLocalAhead))
	require.Contains(t, output, fmt.Sprintf("Branch '%s': Marked for pull", brRemoteAhead))
	require.Contains(t, output, fmt.Sprintf("Branch '%s': Marked for force-push", brDiverged))
	require.Contains(t, output, fmt.Sprintf("Branch '%s': Up-to-date", brUpToDate))

	// Check execution messages
	require.Contains(t, output, "Executing pulls (fast-forward only) for 1 branches")
	require.Contains(t, output, fmt.Sprintf("Successfully pulled branch '%s'", brRemoteAhead))
	require.Contains(t, output, "Executing normal pushes for 1 branches")
	require.Contains(t, output, fmt.Sprintf("Successfully pushed branch '%s'", brLocalAhead))
	require.Contains(t, output, "Executing force-pushes (with lease) for 1 branches")
	require.Contains(t, output, fmt.Sprintf("Successfully force-pushed branch '%s'", brDiverged))

	// Check summary counts
	require.Contains(t, output, "Total branches checked: 5")
	require.Contains(t, output, "Skipped (up-to-date, no remote): 2") // no-remote + uptodate
	require.Contains(t, output, "Successfully pulled (ff-only): 1")
	require.Contains(t, output, "Successfully pushed (normal): 1")
	require.Contains(t, output, "Successfully pushed (force): 1")
	require.Contains(t, output, "Failed to pull (ff-only): 0")
	require.Contains(t, output, "Failed to push (normal): 0")
	require.Contains(t, output, "Failed to push (force): 0")
	require.Contains(t, output, "Errors during checks/operations: 0")

	// Check final state of branches
	_, err = getRemoteHeadHash(t, remoteRepoPath, brNoRemote)
	require.Error(t, err) // Should still not exist remotely

	remoteHeadLA, err := getRemoteHeadHash(t, remoteRepoPath, brLocalAhead)
	require.NoError(t, err)
	require.Equal(t, commitLocalAhead2, remoteHeadLA) // Should be pushed

	localHeadRA, err := localRepo.Reference(plumbing.NewBranchReferenceName(brRemoteAhead), true)
	require.NoError(t, err)
	require.Equal(t, commitRemoteAhead2, localHeadRA.Hash()) // Should be pulled

	remoteHeadD, err := getRemoteHeadHash(t, remoteRepoPath, brDiverged)
	require.NoError(t, err)
	require.Equal(t, commitDivergedLocal, remoteHeadD) // Should be force-pushed

	remoteHeadUTD, err := getRemoteHeadHash(t, remoteRepoPath, brUpToDate)
	require.NoError(t, err)
	require.Equal(t, commitUpToDate, remoteHeadUTD) // Should be unchanged

}

func TestSyncUndo(t *testing.T) {
	remoteRepoPath, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)
	baseHash := headRef.Hash()

	// --- Setup Branches for Undo Test ---
	// 1. Branch to be pulled ("remote-ahead")
	brPull := "remote-ahead-undo"
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseHash, Branch: plumbing.NewBranchReferenceName(brPull), Create: true})
	require.NoError(t, err)
	commitPullPre := addSingleCommit(t, localRepoPath, wt, "ra-undo-1.txt", "ra1u", "RA Undo 1")
	pushRefSpecRAU := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(brPull).String(), plumbing.NewBranchReferenceName(brPull).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpecRAU)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}
	commitPullPost := commitDirectlyToRemote(t, remoteRepoPath, brPull, "ra-undo-2.txt", "ra2u", "RA Undo 2")

	// 2. Branch to be pushed ("local-ahead")
	brPush := "local-ahead-undo"
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseHash, Branch: plumbing.NewBranchReferenceName(brPush), Create: true})
	require.NoError(t, err)
	_ = addSingleCommit(t, localRepoPath, wt, "la-undo-1.txt", "la1u", "LA Undo 1")
	pushRefSpecLAU := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(brPush).String(), plumbing.NewBranchReferenceName(brPush).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpecLAU)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}
	commitPushPost := addSingleCommit(t, localRepoPath, wt, "la-undo-2.txt", "la2u", "LA Undo 2")

	// 3. Branch to be force-pushed ("diverged")
	brForce := "diverged-undo"
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseHash, Branch: plumbing.NewBranchReferenceName(brForce), Create: true})
	require.NoError(t, err)
	_ = addSingleCommit(t, localRepoPath, wt, "d-undo-base.txt", "dbu", "D Undo Base")
	pushRefSpecDU := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(brForce).String(), plumbing.NewBranchReferenceName(brForce).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpecDU)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}
	commitForceLocalPre := addSingleCommit(t, localRepoPath, wt, "d-undo-local.txt", "dlu", "D Undo Local")
	_ = commitDirectlyToRemote(t, remoteRepoPath, brForce, "d-undo-remote.txt", "dru", "D Undo Remote")

	// Fetch before sync
	err = wt.Checkout(&git.CheckoutOptions{Branch: headRef.Name()})
	require.NoError(t, err)
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchOutput, err := fetchCmd.CombinedOutput()
	require.NoError(t, err, "Failed to fetch origin: %s", string(fetchOutput))

	// --- Setup Config ---
	towerName := "undo-tower"
	config, err := LoadConfig()
	require.NoError(t, err)
	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name: towerName,
			Branches: []Branch{
				{Name: brPull},
				{Name: brPush},
				{Name: brForce},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// --- Run Sync ---
	restoreStdinSync := mockInput("y")
	defer restoreStdinSync()
	syncCmd := &SyncDoCmd{Remote: "origin"}
	_, err = CaptureOutput(func() error { return syncCmd.Run(nil) })
	require.NoError(t, err, "Initial sync failed")

	// --- Verify state AFTER sync (before undo) ---
	// Pulled branch should match remote commit
	localHeadPullAfter, err := localRepo.Reference(plumbing.NewBranchReferenceName(brPull), true)
	require.NoError(t, err)
	require.Equal(t, commitPullPost, localHeadPullAfter.Hash())
	// Pushed branch remote should match local commit
	remoteHeadPushAfter, err := getRemoteHeadHash(t, remoteRepoPath, brPush)
	require.NoError(t, err)
	require.Equal(t, commitPushPost, remoteHeadPushAfter)
	// Force-pushed branch remote should match local commit
	remoteHeadForceAfter, err := getRemoteHeadHash(t, remoteRepoPath, brForce)
	require.NoError(t, err)
	require.Equal(t, commitForceLocalPre, remoteHeadForceAfter) // Remote was overwritten

	// --- Run Sync Undo ---
	restoreStdinUndo := mockInput("y")
	defer restoreStdinUndo()
	undoCmd := &SyncUndoCmd{}
	outputUndo, err := CaptureOutput(func() error { return undoCmd.Run(nil) })
	require.NoError(t, err, "ghenga sync undo failed. Output:\n%s", outputUndo)
	t.Logf("Sync Undo output:\n%s", outputUndo)

	// --- Assertions AFTER Undo ---
	// Check output
	require.Contains(t, outputUndo, "Successfully restored branch 'remote-ahead-undo'")
	require.Contains(t, outputUndo, "Successfully restored branch 'local-ahead-undo'")
	require.Contains(t, outputUndo, "Successfully restored branch 'diverged-undo'")
	require.Contains(t, outputUndo, "Successfully undid the last sync operation!")

	// Verify local branches are reset to their pre-sync state
	localHeadPullUndo, err := localRepo.Reference(plumbing.NewBranchReferenceName(brPull), true)
	require.NoError(t, err)
	require.Equal(t, commitPullPre, localHeadPullUndo.Hash(), "Branch %s not reset correctly by undo", brPull)

	localHeadPushUndo, err := localRepo.Reference(plumbing.NewBranchReferenceName(brPush), true)
	require.NoError(t, err)
	require.Equal(t, commitPushPost, localHeadPushUndo.Hash(), "Branch %s should still be at its post-sync state (undo doesn't revert pushes)", brPush)

	localHeadForceUndo, err := localRepo.Reference(plumbing.NewBranchReferenceName(brForce), true)
	require.NoError(t, err)
	require.Equal(t, commitForceLocalPre, localHeadForceUndo.Hash(), "Branch %s not reset correctly by undo", brForce)

	// Verify config state is cleared
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(finalConfig.Repos[0], towerName)
	require.NotNil(t, tower)
	require.Empty(t, tower.LastSynced, "LastSynced timestamp should be cleared")
	for _, b := range tower.Branches {
		require.Empty(t, b.PreSyncReflogID, "PreSyncReflogID should be cleared for branch %s", b.Name)
	}
}

func TestSyncUndoMultiple(t *testing.T) {
	_, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)
	baseHash := headRef.Hash()

	// --- Branch Setups ---
	brSync1 := "branch-sync1"
	brSync2 := "branch-sync2"

	// Setup branch-sync1 (Local Ahead for first sync)
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseHash, Branch: plumbing.NewBranchReferenceName(brSync1), Create: true})
	require.NoError(t, err)
	_ = addSingleCommit(t, localRepoPath, wt, "sync1-1.txt", "s1-1", "Sync1 Commit 1")
	pushRefSpec1 := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(brSync1).String(), plumbing.NewBranchReferenceName(brSync1).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpec1)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}
	commitSync1Post := addSingleCommit(t, localRepoPath, wt, "sync1-2.txt", "s1-2", "Sync1 Commit 2")

	// Setup branch-sync2 (Up To Date for first sync)
	err = wt.Checkout(&git.CheckoutOptions{Hash: baseHash, Branch: plumbing.NewBranchReferenceName(brSync2), Create: true})
	require.NoError(t, err)
	commitSync2Pre := addSingleCommit(t, localRepoPath, wt, "sync2-1.txt", "s2-1", "Sync2 Commit 1")
	pushRefSpec2 := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(brSync2).String(), plumbing.NewBranchReferenceName(brSync2).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpec2)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}

	// --- Config Setup ---
	towerName := "multi-undo-tower"
	config, err := LoadConfig()
	require.NoError(t, err)
	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name: towerName,
			Branches: []Branch{
				{Name: brSync1},
				{Name: brSync2},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// --- First Sync ---
	fmt.Println("--- Running First Sync ---")
	restoreStdin1 := mockInput("y")
	defer restoreStdin1()
	syncCmd1 := &SyncDoCmd{Remote: "origin"}
	output1, err := CaptureOutput(func() error { return syncCmd1.Run(nil) })
	require.NoError(t, err, "First sync failed: %s", output1)
	require.Contains(t, output1, "Successfully pushed branch 'branch-sync1'")
	require.Contains(t, output1, "Branch 'branch-sync2': Up-to-date")

	// Record state after first sync
	hashSync1AfterSync1, err := localRepo.Reference(plumbing.NewBranchReferenceName(brSync1), true)
	require.NoError(t, err)
	hashSync2AfterSync1, err := localRepo.Reference(plumbing.NewBranchReferenceName(brSync2), true)
	require.NoError(t, err)
	require.Equal(t, commitSync1Post, hashSync1AfterSync1.Hash())
	require.Equal(t, commitSync2Pre, hashSync2AfterSync1.Hash())

	// --- Modify Branch 2 for Second Sync ---
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(brSync2)})
	require.NoError(t, err)
	// The scenario where pull overwrites local is tested in TestSyncUndo
	commitSync2Post := addSingleCommit(t, localRepoPath, wt, "sync2-2.txt", "s2-2", "Sync2 Commit 2")

	// --- Second Sync ---
	fmt.Println("--- Running Second Sync ---")
	restoreStdin2 := mockInput("y")
	defer restoreStdin2()
	syncCmd2 := &SyncDoCmd{Remote: "origin"}
	output2, err := CaptureOutput(func() error { return syncCmd2.Run(nil) })
	require.NoError(t, err, "Second sync failed: %s", output2)
	require.Contains(t, output2, "Branch 'branch-sync1': Up-to-date")
	require.Contains(t, output2, "Successfully pushed branch 'branch-sync2'")

	// --- Run Sync Undo ---
	fmt.Println("--- Running Sync Undo ---")
	restoreStdinUndo := mockInput("y")
	defer restoreStdinUndo()
	undoCmd := &SyncUndoCmd{}
	outputUndo, err := CaptureOutput(func() error { return undoCmd.Run(nil) })
	require.NoError(t, err, "ghenga sync undo failed. Output:\n%s", outputUndo)
	t.Logf("Sync Undo output:\n%s", outputUndo)

	// --- Assertions AFTER Undo ---
	require.Contains(t, outputUndo, "Successfully restored branch 'branch-sync2'")
	require.NotContains(t, outputUndo, "Successfully restored branch 'branch-sync1'") // Should only restore #2
	require.Contains(t, outputUndo, "Successfully undid the last sync operation!")

	// Verify local branch states after undo
	// Branch 1 should NOT be reverted (still at commitSync1Post)
	hashSync1AfterUndo, err := localRepo.Reference(plumbing.NewBranchReferenceName(brSync1), true)
	require.NoError(t, err)
	require.Equal(t, commitSync1Post, hashSync1AfterUndo.Hash(), "Branch %s should NOT be reverted by undo", brSync1)

	// Branch 2 SHOULD be reverted to state BEFORE second sync
	hashSync2AfterUndo, err := localRepo.Reference(plumbing.NewBranchReferenceName(brSync2), true)
	require.NoError(t, err)
	// The undo resets to the state *before* the second sync ran, which was commitSync2Post
	require.Equal(t, commitSync2Post, hashSync2AfterUndo.Hash(), "Branch %s SHOULD be reverted by undo to its state before the 2nd sync", brSync2)

	// Verify config state is cleared
	finalConfig, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(finalConfig.Repos[0], towerName)
	require.NotNil(t, tower)
	require.Empty(t, tower.LastSynced, "LastSynced timestamp should be cleared")
	for _, b := range tower.Branches {
		require.Empty(t, b.PreSyncReflogID, "PreSyncReflogID should be cleared for branch %s", b.Name)
	}
}

func TestSyncForceWithLeaseSafety(t *testing.T) {
	// This test verifies that force-with-lease protects against overwriting
	// changes made by others after our last fetch
	remoteRepoPath, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	branchName := "feature-force-lease-test"
	
	// 1. Create branch, commit, push (establish baseline)
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   headRef.Hash(),
		Branch: plumbing.NewBranchReferenceName(branchName),
		Create: true,
	})
	require.NoError(t, err)
	localCommitHash1 := addSingleCommit(t, localRepoPath, wt, "force-lease-1.txt", "content 1", "First commit")
	pushRefSpec := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(branchName).String(), plumbing.NewBranchReferenceName(branchName).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpec)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}

	// 2. Fetch to establish our local remote tracking reference baseline
	fetchCmd := exec.Command("git", "fetch", "origin")
	fetchCmd.Dir = localRepoPath
	fetchOutput, err := fetchCmd.CombinedOutput()
	require.NoError(t, err, "Failed to initial fetch origin: %s", string(fetchOutput))

	// 3. Make divergent local commit (this will conflict with remote)
	localCommitHash2 := addSingleCommit(t, localRepoPath, wt, "force-lease-local.txt", "local content", "Local divergent commit")
	require.NotEqual(t, localCommitHash1, localCommitHash2)

	// 4. Simulate someone else pushing to remote AFTER our fetch
	// This creates the scenario where force-with-lease should fail because
	// our local remote tracking ref is now stale (points to localCommitHash1)
	// but the actual remote now points to a different commit
	remoteCommitHash := commitDirectlyToRemote(t, remoteRepoPath, branchName, "force-lease-remote.txt", "remote content", "Remote commit by someone else")
	require.NotEqual(t, localCommitHash2, remoteCommitHash)
	require.NotEqual(t, localCommitHash1, remoteCommitHash) // Remote moved from our baseline

	// 5. Setup ghenga config for force-push scenario
	towerName := "force-lease-tower"
	config, err := LoadConfig()
	require.NoError(t, err)
	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name:     towerName,
			Branches: []Branch{{Name: branchName}},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// 6. Run sync - this should FAIL due to force-with-lease protection
	// Since remote has changed since our last fetch, force-with-lease should reject the push
	restoreStdin := mockInput("y")
	defer restoreStdin()

	syncCmd := &SyncDoCmd{Remote: "origin"}
	output, err := CaptureOutput(func() error {
		return syncCmd.Run(nil)
	})
	
	t.Logf("Sync output (should show force-with-lease failure):\n%s", output)
	
	// Check if it properly detected divergence and attempted force-push
	if strings.Contains(output, "Marked for force-push") {
		// Expected path: force-push should fail due to force-with-lease protection
		require.Error(t, err, "Sync should fail due to force-with-lease protection when branches diverged")
		require.Contains(t, output, "Error force-pushing branch", "Output should show force-push error")
		require.Contains(t, output, "Failed to push (force): 1", "Output should show force push failure count")
		t.Logf("✅ Force-with-lease correctly rejected push!")
	} else {
		// Unexpected: branch was not detected as diverged
		t.Logf("⚠️  Branch was not detected as diverged. Output:\n%s", output)
		require.Fail(t, "Branch should have been detected as diverged and marked for force-push")
	}
}

func TestSyncPullNonExistentRemote(t *testing.T) {
	// This test verifies sync behavior when a branch exists locally 
	// but the corresponding remote branch doesn't exist (deleted, never pushed, etc.)
	_, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	branchName := "feature-no-remote"
	
	// 1. Create a local branch with commits but DON'T push it
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   headRef.Hash(),
		Branch: plumbing.NewBranchReferenceName(branchName),
		Create: true,
	})
	require.NoError(t, err)
	localCommitHash := addSingleCommit(t, localRepoPath, wt, "no-remote.txt", "content", "Local commit, no remote")

	// 2. Create another branch that DOES exist on remote for comparison
	anotherBranchName := "feature-has-remote"
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   headRef.Hash(),
		Branch: plumbing.NewBranchReferenceName(anotherBranchName),
		Create: true,
	})
	require.NoError(t, err)
	_ = addSingleCommit(t, localRepoPath, wt, "has-remote.txt", "content", "Commit that will be pushed")
	
	// Push the second branch so it has a remote
	pushRefSpec := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(anotherBranchName).String(), plumbing.NewBranchReferenceName(anotherBranchName).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpec)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}

	// 3. Setup ghenga config with BOTH branches
	towerName := "no-remote-tower"
	config, err := LoadConfig()
	require.NoError(t, err)
	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name: towerName,
			Branches: []Branch{
				{Name: branchName},        // This one has NO remote
				{Name: anotherBranchName}, // This one HAS remote
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// 4. Run sync - should handle missing remote branch gracefully
	restoreStdin := mockInput("n") // Cancel to just see the analysis
	defer restoreStdin()

	syncCmd := &SyncDoCmd{Remote: "origin"}
	output, err := CaptureOutput(func() error {
		return syncCmd.Run(nil)
	})
	
	t.Logf("Sync output (should handle missing remote gracefully):\n%s", output)
	
	// 5. Verify behavior
	// The branch with no remote should be skipped or marked appropriately
	require.Contains(t, output, fmt.Sprintf("Branch '%s': Does not exist on remote", branchName), 
		"Should detect and report missing remote branch")
	
	// The branch with remote should be processed normally  
	require.Contains(t, output, fmt.Sprintf("Branch '%s':", anotherBranchName),
		"Should process branch that has remote normally")
	
	// Should not crash or error on the missing remote branch
	if err != nil {
		t.Logf("Sync failed, but checking if it's due to cancellation: %v", err)
		// If it failed due to cancellation (user said 'n'), that's expected
		require.Contains(t, output, "operation cancelled", "If sync failed, should be due to user cancellation")
	}
	
	// Verify no remote tracking ref was created for the non-existent remote branch
	nonExistentRemoteRef := plumbing.NewRemoteReferenceName("origin", branchName)
	_, err = localRepo.Reference(nonExistentRemoteRef, true)
	require.Error(t, err, "Should not have remote tracking ref for branch that doesn't exist on remote")
	
	// Verify the local branch still exists and hasn't been modified
	localBranchRef, err := localRepo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err, "Local branch should still exist")
	require.Equal(t, localCommitHash, localBranchRef.Hash(), "Local branch should be unchanged")
}

func TestSyncPullNonFastForward(t *testing.T) {
	// This test verifies sync behavior when pulling would require a merge (not fast-forward)
	// Our sync logic uses Force: false for pulls, so this should fail gracefully
	remoteRepoPath, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	branchName := "feature-non-ff"
	
	// 1. Create branch, commit, push (establish baseline)
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   headRef.Hash(),
		Branch: plumbing.NewBranchReferenceName(branchName),
		Create: true,
	})
	require.NoError(t, err)
	baseCommitHash := addSingleCommit(t, localRepoPath, wt, "base.txt", "base content", "Base commit")
	
	// Push the base commit
	pushRefSpec := fmt.Sprintf("%s:%s", plumbing.NewBranchReferenceName(branchName).String(), plumbing.NewBranchReferenceName(branchName).String())
	err = localRepo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{config.RefSpec(pushRefSpec)}})
	if err != git.NoErrAlreadyUpToDate {
		require.NoError(t, err)
	}

	// 2. Add DIFFERENT commits locally and remotely (creating non-fast-forward scenario)
	
	// Local commit
	localCommitHash := addSingleCommit(t, localRepoPath, wt, "local.txt", "local content", "Local commit - non-FF")
	require.NotEqual(t, baseCommitHash, localCommitHash)

	// Remote commit (different from local)
	remoteCommitHash := commitDirectlyToRemote(t, remoteRepoPath, branchName, "remote.txt", "remote content", "Remote commit - non-FF")
	require.NotEqual(t, baseCommitHash, remoteCommitHash)
	require.NotEqual(t, localCommitHash, remoteCommitHash)

	// 3. Verify the scenario is set up correctly: 
	// - Local has: base -> local
	// - Remote has: base -> remote  
	// - Pulling would require merge (not fast-forward)

	// 4. Setup ghenga config
	towerName := "non-ff-tower"
	config, err := LoadConfig()
	require.NoError(t, err)
	config.Repos[0].Current = towerName
	config.Repos[0].Towers = []*Tower{
		{
			Name:     towerName,
			Branches: []Branch{{Name: branchName}},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// 5. Run sync - this should detect the issue and NOT attempt a pull
	// because our fetch will show the branches are diverged, not that remote is ahead
	restoreStdin := mockInput("n") // Cancel if it asks for confirmation
	defer restoreStdin()

	syncCmd := &SyncDoCmd{Remote: "origin"}
	output, err := CaptureOutput(func() error {
		return syncCmd.Run(nil)
	})
	
	t.Logf("Sync output (should detect divergence, not attempt non-FF pull):\n%s", output)
	
	// 6. Verify behavior: Should detect DIVERGENCE, not "remote ahead"
	// Because both local and remote have moved from the base, it's diverged, not ahead
	require.Contains(t, output, fmt.Sprintf("Branch '%s': Marked for force-push (diverged", branchName), 
		"Should detect divergence, not remote ahead requiring pull")
	
	// Should NOT contain pull-related messages
	require.NotContains(t, output, "Marked for pull", "Should not try to pull diverged branches")
	require.NotContains(t, output, "Executing pulls", "Should not attempt pull operations")
	
	// Verify local branch is unchanged (no failed pull attempt)
	localBranchRef, err := localRepo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err, "Local branch should still exist")
	require.Equal(t, localCommitHash, localBranchRef.Hash(), "Local branch should be unchanged - no pull attempted")
}

func TestSyncSpecialBranchNames(t *testing.T) {
	// This test verifies sync behavior with branch names containing special characters
	// Our ref name construction should handle these correctly
	_, localRepoPath, localRepo, cleanup := setupSyncTestEnv(t)
	defer cleanup()

	// Test various special characters that are VALID in git branch names
	testBranches := []string{
		"feature/my-feature",    // Slashes (very common)
		"release-v1.2.3",        // Dots and hyphens
		"hotfix_urgent_fix",     // Underscores
		"feat-branch-123",       // Normal case for comparison  
		"feature/sub/deep",      // Multiple slashes
		"fix.urgent.123",        // Multiple dots
	}

	var createdCommits []plumbing.Hash
	
	// 1. Create branches with special names and commits
	wt, err := localRepo.Worktree()
	require.NoError(t, err)
	headRef, err := localRepo.Head()
	require.NoError(t, err)
	baseHash := headRef.Hash()

	for i, branchName := range testBranches {
		t.Logf("Creating branch with special name: '%s'", branchName)
		
		err = wt.Checkout(&git.CheckoutOptions{
			Hash:   baseHash,
			Branch: plumbing.NewBranchReferenceName(branchName),
			Create: true,
		})
		require.NoError(t, err, "Should be able to create branch '%s'", branchName)
		
		commitHash := addSingleCommit(t, localRepoPath, wt, 
			fmt.Sprintf("file-%d.txt", i), 
			fmt.Sprintf("content for %s", branchName), 
			fmt.Sprintf("Commit for %s", branchName))
		createdCommits = append(createdCommits, commitHash)
		
		// Push each branch to establish remote tracking
		pushRefSpec := fmt.Sprintf("%s:%s", 
			plumbing.NewBranchReferenceName(branchName).String(), 
			plumbing.NewBranchReferenceName(branchName).String())
		err = localRepo.Push(&git.PushOptions{
			RemoteName: "origin", 
			RefSpecs:   []config.RefSpec{config.RefSpec(pushRefSpec)},
		})
		if err != git.NoErrAlreadyUpToDate {
			require.NoError(t, err, "Should be able to push branch '%s'", branchName)
		}
	}

	// 2. Setup ghenga config with all special branch names
	towerName := "special-names-tower"
	config, err := LoadConfig()
	require.NoError(t, err)
	config.Repos[0].Current = towerName
	
	var branches []Branch
	for _, name := range testBranches {
		branches = append(branches, Branch{Name: name})
	}
	
	config.Repos[0].Towers = []*Tower{
		{
			Name:     towerName,
			Branches: branches,
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// 3. Run sync - should handle all special branch names without errors
	syncCmd := &SyncDoCmd{Remote: "origin"}
	output, err := CaptureOutput(func() error {
		return syncCmd.Run(nil)
	})
	
	// Should complete without errors
	require.NoError(t, err, "Sync should handle special branch names without errors")
	
	t.Logf("Sync output with special branch names:\n%s", output)
	
	// 4. Verify all branches were processed correctly
	for _, branchName := range testBranches {
		// Should appear in output without errors
		require.Contains(t, output, fmt.Sprintf("Branch '%s':", branchName), 
			"Branch '%s' should be processed", branchName)
		
		// Should not have error messages for this branch
		require.NotContains(t, output, fmt.Sprintf("Error checking status.*%s", branchName),
			"Should not have status check errors for branch '%s'", branchName)
	}
	
	// 5. Verify our ref name construction worked by checking remote tracking refs exist
	for _, branchName := range testBranches {
		remoteTrackingRef := plumbing.NewRemoteReferenceName("origin", branchName)
		_, err := localRepo.Reference(remoteTrackingRef, true)
		require.NoError(t, err, "Remote tracking ref should exist for branch '%s'", branchName)
		
		localBranchRef := plumbing.NewBranchReferenceName(branchName)
		_, err = localRepo.Reference(localBranchRef, true)
		require.NoError(t, err, "Local branch ref should exist for branch '%s'", branchName)
	}
}