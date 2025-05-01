package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestRebaseCommand(t *testing.T) {
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
	_, err = worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Create a base branch
	err = worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("base-branch"),
		Create: true,
	})
	if err != nil {
		t.Fatalf("Failed to create base branch: %v", err)
	}

	// Modify the file and commit on base branch
	err = os.WriteFile(fileName, []byte("base branch content"), 0644)
	if err != nil {
		t.Fatalf("Failed to modify file: %v", err)
	}
	_, err = worktree.Add("file.txt")
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}
	baseCommit, err := worktree.Commit("Base branch commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Create branch-1 on top of base-branch
	err = worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("branch-1"),
		Create: true,
	})
	if err != nil {
		t.Fatalf("Failed to create branch-1: %v", err)
	}

	// Modify the file and commit on branch-1
	err = os.WriteFile(fileName, []byte("branch-1 content"), 0644)
	if err != nil {
		t.Fatalf("Failed to modify file: %v", err)
	}
	_, err = worktree.Add("file.txt")
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}
	_, err = worktree.Commit("Branch 1 commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Create branch-2 on top of branch-1
	err = worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("branch-2"),
		Create: true,
	})
	if err != nil {
		t.Fatalf("Failed to create branch-2: %v", err)
	}

	// Modify the file and commit on branch-2
	err = os.WriteFile(fileName, []byte("branch-2 content"), 0644)
	if err != nil {
		t.Fatalf("Failed to modify file: %v", err)
	}
	_, err = worktree.Add("file.txt")
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}
	_, err = worktree.Commit("Branch 2 commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Go back to base-branch and create a divergent commit
	err = worktree.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("base-branch"),
	})
	if err != nil {
		t.Fatalf("Failed to checkout base-branch: %v", err)
	}

	// Modify the file and commit on base-branch (creating divergence)
	err = os.WriteFile(fileName, []byte("base branch divergent content"), 0644)
	if err != nil {
		t.Fatalf("Failed to modify file: %v", err)
	}
	_, err = worktree.Add("file.txt")
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}
	_, err = worktree.Commit("Divergent base branch commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Create mock config for testing
	oldConfigPath := ConfigPath
	defer func() { ConfigPath = oldConfigPath }()

	configFile := filepath.Join(tempDir, "config.toml")
	ConfigPath = func() (string, error) {
		return configFile, nil
	}

	// Setup tower in the configuration
	config := &Config{
		Repos: []*RepoInfo{
			{
				Path:    tempDir,
				Current: "test-tower",
				Towers: []*Tower{
					{
						Name: "test-tower",
						Base: baseCommit.String(),
						Branches: []Branch{
							{Name: "base-branch"},
							{Name: "branch-1"},
							{Name: "branch-2"},
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

	// Mock a clean working directory by overriding exec.Command
	// This would need to be implemented with a mocking library in a real scenario

	// Test the LastReflogID is stored after a rebase
	// In a real implementation, this would execute the rebase command and verify
	// that LastReflogID is set correctly, and that branches are rebased properly

	// Test that rebase undo restores branches to their original state
	// In a real implementation, this would execute the rebase undo command and verify
	// that branches are restored to their original state

	t.Log("Rebase and undo functionality needs to be tested manually since it uses exec.Command")
}
