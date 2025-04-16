package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestAddCommand(t *testing.T) {
	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "ghenga-add-test")
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

	// Add a branch using the Add command
	addCmd := &AddCmd{
		Name:  "feature-x",
		Tower: "test-tower",
	}
	if err := addCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to run Add command: %v", err)
	}

	// Add another branch to the same tower
	addCmd2 := &AddCmd{
		Name:  "feature-y",
		Tower: "test-tower",
	}
	if err := addCmd2.Run(mockCtx); err != nil {
		t.Fatalf("Failed to run Add command for second branch: %v", err)
	}

	// Add a branch to a different tower
	addCmd3 := &AddCmd{
		Name:  "bugfix-z",
		Tower: "bugfix-tower",
	}
	if err := addCmd3.Run(mockCtx); err != nil {
		t.Fatalf("Failed to run Add command for branch in different tower: %v", err)
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

	// Verify the towers were added
	if len(repo1.Towers) != 2 {
		t.Errorf("Expected 2 towers, got %d", len(repo1.Towers))
		for i, tower := range repo1.Towers {
			t.Logf("Tower %d: %s", i, tower.Name)
		}
	}

	// Find and verify the towers and branches
	var testTower, bugfixTower *Tower
	for _, tower := range repo1.Towers {
		if tower.Name == "test-tower" {
			testTower = tower
		} else if tower.Name == "bugfix-tower" {
			bugfixTower = tower
		}
	}

	if testTower == nil {
		t.Fatalf("Could not find test-tower")
	}
	if bugfixTower == nil {
		t.Fatalf("Could not find bugfix-tower")
	}

	// Verify branches in test-tower
	if len(testTower.Branches) != 2 {
		t.Errorf("Expected 2 branches in test-tower, got %d", len(testTower.Branches))
	}
	foundFeatureX := false
	foundFeatureY := false
	for _, branch := range testTower.Branches {
		if branch.Name == "feature-x" {
			foundFeatureX = true
		} else if branch.Name == "feature-y" {
			foundFeatureY = true
		}
	}
	if !foundFeatureX {
		t.Errorf("Could not find branch feature-x in test-tower")
	}
	if !foundFeatureY {
		t.Errorf("Could not find branch feature-y in test-tower")
	}

	// Verify branch in bugfix-tower
	if len(bugfixTower.Branches) != 1 {
		t.Errorf("Expected 1 branch in bugfix-tower, got %d", len(bugfixTower.Branches))
	}
	if len(bugfixTower.Branches) > 0 && bugfixTower.Branches[0].Name != "bugfix-z" {
		t.Errorf("Expected branch name bugfix-z, got %s", bugfixTower.Branches[0].Name)
	}

	// Test adding a duplicate branch (should fail)
	duplicateCmd := &AddCmd{
		Name:  "feature-x",
		Tower: "test-tower",
	}
	if err := duplicateCmd.Run(mockCtx); err == nil {
		t.Errorf("Expected error when adding duplicate branch, but got none")
	}
}

func TestRemoveCommand(t *testing.T) {
	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "ghenga-remove-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

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

	// Initialize the repository
	initCmd := &InitCmd{
		DefaultTower: "test-tower",
	}
	if err := initCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to run Init command: %v", err)
	}

	// Set current tower
	currentCmd := &CurrentCmd{
		Tower: "test-tower",
	}
	if err := currentCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to set current tower: %v", err)
	}

	// Add branches to the tower
	branchesToAdd := []string{"feature-1", "feature-2", "feature-3"}
	for _, branchName := range branchesToAdd {
		addCmd := &AddCmd{
			Name:  branchName,
			Tower: "test-tower",
		}
		if err := addCmd.Run(mockCtx); err != nil {
			t.Fatalf("Failed to add branch %s: %v", branchName, err)
		}
	}

	// Verify branches were added correctly
	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if len(config.Repos) != 1 {
		t.Fatalf("Expected 1 repo, got %d", len(config.Repos))
	}

	repo1 := config.Repos[0]
	testTower := FindTowerByName(repo1, "test-tower")
	if testTower == nil {
		t.Fatalf("Could not find test-tower")
	}

	if len(testTower.Branches) != 3 {
		t.Fatalf("Expected 3 branches initially, got %d", len(testTower.Branches))
	}

	// Test 1: Remove the middle branch
	removeCmd := &RmCmd{
		Name: "feature-2",
	}
	if err := removeCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to remove branch feature-2: %v", err)
	}

	// Verify the branch was removed
	config, err = LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config after remove: %v", err)
	}
	repo1 = config.Repos[0]
	testTower = FindTowerByName(repo1, "test-tower")

	if len(testTower.Branches) != 2 {
		t.Fatalf("Expected 2 branches after removal, got %d", len(testTower.Branches))
	}

	// Check remaining branches
	expectedBranches := []string{"feature-1", "feature-3"}
	for i, branch := range testTower.Branches {
		if branch.Name != expectedBranches[i] {
			t.Errorf("Expected branch %s at position %d, got %s", expectedBranches[i], i, branch.Name)
		}
	}

	// Test 2: Try to remove a non-existent branch (should fail)
	removeNonExistentCmd := &RmCmd{
		Name: "non-existent-branch",
	}
	if err := removeNonExistentCmd.Run(mockCtx); err == nil {
		t.Errorf("Expected error when removing non-existent branch, but got none")
	}

	// Test 3: Create another tower
	newCmd := &NewCmd{
		Name: "another-tower",
	}
	if err := newCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to create another tower: %v", err)
	}

	// Add a branch to the new tower
	addCmd := &AddCmd{
		Name:  "another-feature",
		Tower: "another-tower",
	}
	if err := addCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to add branch to another tower: %v", err)
	}

	// Test 4: Make sure removing from current tower only affects current tower
	// Switch current tower
	currentCmd = &CurrentCmd{
		Tower: "another-tower",
	}
	if err := currentCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to change current tower: %v", err)
	}

	// Remove the branch from the new current tower
	removeFromNewTowerCmd := &RmCmd{
		Name: "another-feature",
	}
	if err := removeFromNewTowerCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to remove branch from another tower: %v", err)
	}

	// Verify branch was removed from the correct tower
	config, err = LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	repo1 = config.Repos[0]

	// Original tower should still have 2 branches
	testTower = FindTowerByName(repo1, "test-tower")
	if len(testTower.Branches) != 2 {
		t.Errorf("Expected test-tower to still have 2 branches, got %d", len(testTower.Branches))
	}

	// New tower should have 0 branches
	anotherTower := FindTowerByName(repo1, "another-tower")
	if len(anotherTower.Branches) != 0 {
		t.Errorf("Expected another-tower to have 0 branches after removal, got %d", len(anotherTower.Branches))
	}
}
