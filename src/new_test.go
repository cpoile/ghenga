package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestNewCommand(t *testing.T) {
	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "ghenga-new-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Resolve symlinks in tempDir to get the real path
	realTempDir, err := filepath.EvalSymlinks(tempDir)
	if err != nil {
		t.Fatalf("Failed to resolve symlinks in tempDir: %v", err)
	}

	// Set up a test git repository
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("Failed to initialize git repository: %v", err)
	}

	// Create a temporary file and commit it
	filePath := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(filePath, []byte("test content"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Get the worktree
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Failed to get worktree: %v", err)
	}

	// Add the file to git
	if _, err := wt.Add("test.txt"); err != nil {
		t.Fatalf("Failed to add file to git: %v", err)
	}

	// Create an initial commit
	_, err = wt.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("Failed to commit: %v", err)
	}

	// Override the config path for testing
	originalConfigPath := ConfigPath
	configFilePath := filepath.Join(tempDir, "config.toml")
	ConfigPath = func() (string, error) {
		return configFilePath, nil
	}
	defer func() { ConfigPath = originalConfigPath }()

	// Change to the temp directory
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	defer os.Chdir(originalDir)
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Failed to change to temp directory: %v", err)
	}

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Create a new tower using the New command
	newCmd := &NewCmd{
		Name: "feature-tower",
	}
	if err := newCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to run New command: %v", err)
	}

	// Verify the configuration was saved correctly
	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify the repo was added
	if len(config.Repos) != 1 {
		t.Errorf("Expected 1 repo, got %d", len(config.Repos))
	}

	repo1 := config.Repos[0]
	// Compare the real paths to account for symlinks
	repoPath, err := filepath.EvalSymlinks(repo1.Path)
	if err != nil {
		t.Fatalf("Failed to resolve symlinks in repo path: %v", err)
	}
	if repoPath != realTempDir {
		t.Errorf("Expected repo path %s, got %s", realTempDir, repoPath)
	}

	// Verify the tower was added
	if len(repo1.Towers) != 1 {
		t.Errorf("Expected 1 tower, got %d", len(repo1.Towers))
		for i, tower := range repo1.Towers {
			t.Logf("Tower %d: %s", i, tower.Name)
		}
	}

	// Find and verify the tower
	var featureTower *Tower
	for _, tower := range repo1.Towers {
		if tower.Name == "feature-tower" {
			featureTower = tower
		}
	}

	if featureTower == nil {
		t.Fatalf("Could not find feature-tower")
	}

	// Verify the tower has no branches initially
	if len(featureTower.Branches) != 0 {
		t.Errorf("Expected 0 branches in feature-tower, got %d", len(featureTower.Branches))
	}

	// Create another tower
	newCmd2 := &NewCmd{
		Name: "bugfix-tower",
	}
	if err := newCmd2.Run(mockCtx); err != nil {
		t.Fatalf("Failed to run New command for second tower: %v", err)
	}

	// Verify the configuration again
	config, err = LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify we now have two towers
	if len(config.Repos[0].Towers) != 2 {
		t.Errorf("Expected 2 towers, got %d", len(config.Repos[0].Towers))
	}

	// Try to create a duplicate tower (should fail)
	duplicateCmd := &NewCmd{
		Name: "feature-tower",
	}
	if err := duplicateCmd.Run(mockCtx); err == nil {
		t.Errorf("Expected error when adding duplicate tower, but got none")
	}
}
