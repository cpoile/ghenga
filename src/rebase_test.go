package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
)

func TestRebaseBranchSpecificSave(t *testing.T) {
	// Setup a test repository
	tempDir, err := os.MkdirTemp("", "ghenga-test-rebase")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Initialize git repository
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("Failed to initialize git repository: %v", err)
	}

	// Create a dummy file and commit it
	fileName := filepath.Join(tempDir, "file.txt")
	err = os.WriteFile(fileName, []byte("initial content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	// Get the worktree
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Failed to get worktree: %v", err)
	}

	// Add the file
	_, err = worktree.Add("file.txt")
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	// Create initial commit
	initialCommit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Create some branches with different commits
	branches := []string{"base-branch", "feature-1", "feature-2"}
	branchHashes := make(map[string]string)

	for i, branchName := range branches {
		// Checkout master first to create branches from it
		err = worktree.Checkout(&git.CheckoutOptions{
			Hash: initialCommit,
		})
		if err != nil {
			t.Fatalf("Failed to checkout commit: %v", err)
		}

		// Create and checkout the branch
		err = worktree.Checkout(&git.CheckoutOptions{
			Branch: plumbing.NewBranchReferenceName(branchName),
			Create: true,
		})
		if err != nil {
			t.Fatalf("Failed to create branch %s: %v", branchName, err)
		}

		// Modify the file and commit
		err = os.WriteFile(fileName, []byte(branchName+" content "+string(rune('A'+i))), 0644)
		if err != nil {
			t.Fatalf("Failed to modify file: %v", err)
		}
		_, err = worktree.Add("file.txt")
		if err != nil {
			t.Fatalf("Failed to add file: %v", err)
		}
		commit, err := worktree.Commit("Commit on "+branchName, &git.CommitOptions{
			Author: &object.Signature{
				Name:  "test",
				Email: "test@example.com",
				When:  time.Now(),
			},
		})
		if err != nil {
			t.Fatalf("Failed to commit: %v", err)
		}

		// Store the branch hash
		branchHashes[branchName] = commit.String()
	}

	// Create mock config for testing
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()

	configFile := filepath.Join(tempDir, "config.toml")
	ConfigPath = func() (string, error) {
		return configFile, nil
	}

	// Create a tower with the branches
	config := &Config{
		Repos: []*Repo{
			{
				Path:    tempDir,
				Current: "test-tower",
				Towers: []*Tower{
					{
						Name: "test-tower",
						Branches: []Branch{
							{Name: branches[0]},
							{Name: branches[1]},
							{Name: branches[2]},
						},
					},
				},
			},
		},
	}

	// Save the config
	err = SaveConfig(config)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Test the reflog saving functionality
	// In a real test, we'd mock the git commands to avoid actual execution
	// Here we can just verify that the data structures would be updated correctly

	// Load the config
	config, err = LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Get the tower from config
	tower := FindTowerByName(config.Repos[0], "test-tower")
	assert.NotNil(t, tower, "Tower should exist in config")

	// Simulate the RebaseDoCmd.Run() storing reflog IDs
	tower.LastRebased = time.Now().Format(time.RFC3339)
	for i := range tower.Branches {
		branch := &tower.Branches[i]
		// In real implementation this would be from git, here we use our stored hashes
		branch.LastReflogID = branchHashes[branch.Name]
	}

	// Save the config
	err = SaveConfig(config)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Load the config again
	updatedConfig, err := LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Check that the reflog IDs were saved correctly
	updatedTower := FindTowerByName(updatedConfig.Repos[0], "test-tower")
	assert.NotNil(t, updatedTower, "Tower should exist in config")
	assert.NotEmpty(t, updatedTower.LastRebased, "Last rebased timestamp should be set")

	for _, branch := range updatedTower.Branches {
		assert.Equal(t, branchHashes[branch.Name], branch.LastReflogID,
			"Branch %s should have correct LastReflogID", branch.Name)
	}

	// Test that RebaseUndoCmd would process these correctly
	// Here we'd simulate the undo by clearing the reflog IDs

	// Clear the reflog IDs
	for i := range updatedTower.Branches {
		branch := &updatedTower.Branches[i]
		branch.LastReflogID = ""
	}
	updatedTower.LastRebased = ""

	// Save the config
	err = SaveConfig(updatedConfig)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Verify they were cleared
	finalConfig, err := LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	finalTower := FindTowerByName(finalConfig.Repos[0], "test-tower")
	assert.NotNil(t, finalTower, "Tower should exist in config")
	assert.Empty(t, finalTower.LastRebased, "Last rebased timestamp should be cleared")

	for _, branch := range finalTower.Branches {
		assert.Empty(t, branch.LastReflogID,
			"Branch %s should have LastReflogID cleared", branch.Name)
	}
}

func TestRebaseAndUndoWithActualRepo(t *testing.T) {
	// Setup test repository
	tempDir, err := os.MkdirTemp("", "ghenga-test-rebase-actual")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Initialize git repository
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("Failed to initialize git repository: %v", err)
	}

	// Get the worktree
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Failed to get worktree: %v", err)
	}

	// Create an initial commit
	initialFilePath := filepath.Join(tempDir, "initial.txt")
	err = os.WriteFile(initialFilePath, []byte("initial content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create initial file: %v", err)
	}
	_, err = wt.Add("initial.txt")
	if err != nil {
		t.Fatalf("Failed to add initial file: %v", err)
	}
	initialCommit, err := wt.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Failed to create initial commit: %v", err)
	}

	// Step 1: Create the base branch with 3 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Create: true,
		Branch: plumbing.NewBranchReferenceName("base-branch"),
	})
	if err != nil {
		t.Fatalf("Failed to create base branch: %v", err)
	}

	// Add 3 commits to base branch
	for i := range 3 {
		filename := fmt.Sprintf("file-base-branch-%d.txt", i)
		filePath := filepath.Join(tempDir, filename)
		err = os.WriteFile(filePath, fmt.Appendf(nil, "content %d", i), 0644)
		if err != nil {
			t.Fatalf("Failed to write to file: %v", err)
		}
		_, err = wt.Add(filename)
		if err != nil {
			t.Fatalf("Failed to add file: %v", err)
		}
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
				When:  time.Now(),
			},
		})
		if err != nil {
			t.Fatalf("Failed to commit: %v", err)
		}
	}

	// Get base branch reference
	baseRef, err := repo.Reference(plumbing.NewBranchReferenceName("base-branch"), true)
	if err != nil {
		t.Fatalf("Failed to get base branch reference: %v", err)
	}

	// Step 2: Create middle-branch from base branch's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   baseRef.Hash(),
		Create: true,
		Branch: plumbing.NewBranchReferenceName("middle-branch"),
	})
	if err != nil {
		t.Fatalf("Failed to create middle branch: %v", err)
	}

	// Add 2 commits to middle branch
	for i := range 2 {
		filename := fmt.Sprintf("file-middle-branch-%d.txt", i)
		filePath := filepath.Join(tempDir, filename)
		err = os.WriteFile(filePath, fmt.Appendf(nil, "content %d", i), 0644)
		if err != nil {
			t.Fatalf("Failed to write to file: %v", err)
		}
		_, err = wt.Add(filename)
		if err != nil {
			t.Fatalf("Failed to add file: %v", err)
		}
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
				When:  time.Now(),
			},
		})
		if err != nil {
			t.Fatalf("Failed to commit: %v", err)
		}
	}

	// Get middle branch reference
	middleRef, err := repo.Reference(plumbing.NewBranchReferenceName("middle-branch"), true)
	if err != nil {
		t.Fatalf("Failed to get middle branch reference: %v", err)
	}

	// Step 3: Create top-branch from middle branch's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   middleRef.Hash(),
		Create: true,
		Branch: plumbing.NewBranchReferenceName("top-branch"),
	})
	if err != nil {
		t.Fatalf("Failed to create top branch: %v", err)
	}

	// Add 2 commits to top branch

	for i := range 2 {
		filename := fmt.Sprintf("file-top-branch-%d.txt", i)
		filePath := filepath.Join(tempDir, filename)
		err = os.WriteFile(filePath, fmt.Appendf(nil, "content %d", i), 0644)
		if err != nil {
			t.Fatalf("Failed to write to file: %v", err)
		}
		_, err = wt.Add(filename)
		if err != nil {
			t.Fatalf("Failed to add file: %v", err)
		}
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
				When:  time.Now(),
			},
		})
		if err != nil {
			t.Fatalf("Failed to commit: %v", err)
		}
	}

	// Step 4: Go back to base-branch and add one more commit (creates divergence)
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("base-branch"),
	})
	if err != nil {
		t.Fatalf("Failed to checkout base branch: %v", err)
	}

	// Add divergent commit to base branch
	divergentBasePath := filepath.Join(tempDir, "divergent-base.txt")
	err = os.WriteFile(divergentBasePath, []byte("divergent base content"), 0644)
	if err != nil {
		t.Fatalf("Failed to write to divergent file: %v", err)
	}
	_, err = wt.Add("divergent-base.txt")
	if err != nil {
		t.Fatalf("Failed to add divergent file: %v", err)
	}
	_, err = wt.Commit("Divergent commit on base branch", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit divergent change: %v", err)
	}

	// Temporarily change working directory
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current working directory: %v", err)
	}
	defer os.Chdir(oldWd)

	// create a new temp directory for config file
	configTempDir, err := os.MkdirTemp("", "ghenga-test-rebase-actual-config")
	if err != nil {
		t.Fatalf("Failed to create temp directory: %v", err)
	}
	defer os.RemoveAll(configTempDir)

	err = os.Chdir(configTempDir)
	if err != nil {
		t.Fatalf("Failed to change working directory: %v", err)
	}

	// Create mock config file
	configFile := filepath.Join(configTempDir, "config.toml")
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()
	ConfigPath = func() (string, error) {
		return configFile, nil
	}

	// Initialize config with one tower and all branches in order
	config := &Config{
		Repos: []*Repo{
			{
				Path:    tempDir,
				Current: "test-tower",
				Towers: []*Tower{
					{
						Name: "test-tower",
						Base: initialCommit.String(),
						Branches: []Branch{
							{Name: "base-branch"},
							{Name: "middle-branch"},
							{Name: "top-branch"},
						},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// change back to the temp directory
	err = os.Chdir(tempDir)
	if err != nil {
		t.Fatalf("Failed to change working directory: %v", err)
	}

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
	mockInput := func(input string) func() {
		oldStdin := os.Stdin
		r, w, _ := os.Pipe()
		os.Stdin = r
		w.Write([]byte(input + "\n"))
		w.Close()
		return func() { os.Stdin = oldStdin }
	}
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

	tower := FindTowerByName(finalConfig.Repos[0], "test-tower")
	assert.NotNil(t, tower, "Tower should exist")
	assert.Empty(t, tower.LastRebased, "LastRebased should be cleared after undo")

	for _, branch := range tower.Branches {
		assert.Empty(t, branch.LastReflogID,
			"Branch %s should have LastReflogID cleared after undo", branch.Name)
	}
}
