package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestBranchAddCommand(t *testing.T) {
	// Simulate stdin for base branch prompts using os.Pipe
	originalStdin := os.Stdin
	r, w, _ := os.Pipe()
	defer func() {
		os.Stdin = originalStdin
		r.Close() // Close the reader end of the pipe
	}()
	// Provide two inputs for the two new towers created
	go func() {
		defer w.Close() // Close writer after writing
		_, _ = io.WriteString(w, "1\n1\n")
	}()
	os.Stdin = r // Assign the reader end to os.Stdin

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
	// Get the HEAD hash after initial commit for branch creation
	headRef, err := repo.Head()
	if err != nil {
		t.Fatalf("Failed to get HEAD ref: %v", err)
	}
	initialHeadHash := headRef.Hash()

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

	// Create the branches in the git repo before adding them
	// Use the existing helper from test_utils.go, which needs commitCount (set to 0 as we just need the branch ref)
	// We need to reset HEAD each time as createTestBranch checks out the new branch
	err = wt.Checkout(&git.CheckoutOptions{Hash: initialHeadHash})
	if err != nil {
		t.Fatalf("Failed checkout initial commit: %v", err)
	}
	createTestBranch(t, repo, "feature-x", 0)
	err = wt.Checkout(&git.CheckoutOptions{Hash: initialHeadHash})
	if err != nil {
		t.Fatalf("Failed checkout initial commit: %v", err)
	}
	createTestBranch(t, repo, "feature-y", 0)
	err = wt.Checkout(&git.CheckoutOptions{Hash: initialHeadHash})
	if err != nil {
		t.Fatalf("Failed checkout initial commit: %v", err)
	}
	createTestBranch(t, repo, "bugfix-z", 0)
	err = wt.Checkout(&git.CheckoutOptions{Hash: initialHeadHash})
	if err != nil {
		t.Fatalf("Failed checkout initial commit: %v", err)
	}

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

func TestBranchRemoveCommand(t *testing.T) {
	// Simulate stdin for base branch prompts using os.Pipe
	originalStdin := os.Stdin
	r, w, _ := os.Pipe()
	defer func() {
		os.Stdin = originalStdin
		r.Close()
	}()
	// Provide two inputs for the two new towers created
	go func() {
		defer w.Close()
		io.WriteString(w, "1\n1\n") // Input for feature-1 and another-feature
	}()
	os.Stdin = r

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
	// Get the HEAD hash after initial commit for branch creation
	headRef, err := repo.Head()
	if err != nil {
		t.Fatalf("Failed to get HEAD ref: %v", err)
	}
	initialHeadHash := headRef.Hash()

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

	// Create branches in the git repo directly before adding them via AddCmd
	branchesToAdd := []string{"feature-1", "feature-2", "feature-3"}
	for _, branchName := range branchesToAdd {
		branchRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), initialHeadHash)
		if err := repo.Storer.SetReference(branchRef); err != nil {
			t.Fatalf("Failed to create git branch %s reference: %v", branchName, err)
		}
	}

	// Add branches to the tower config via AddCmd
	for _, branchName := range branchesToAdd {
		addCmd := &AddCmd{
			Name:  branchName,
			Tower: "test-tower", // Explicitly target test-tower
		}
		if err := addCmd.Run(mockCtx); err != nil {
			t.Fatalf("Failed to add branch %s to config: %v", branchName, err)
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
	testTower := findTowerByName(repo1, "test-tower")
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
	testTower = findTowerByName(repo1, "test-tower")

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

	// Create and add a branch to the new tower
	branchNameToAdd := "another-feature"
	branchRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchNameToAdd), initialHeadHash)
	if err := repo.Storer.SetReference(branchRef); err != nil {
		t.Fatalf("Failed to create git branch %s reference: %v", branchNameToAdd, err)
	}
	addCmd := &AddCmd{
		Name:  branchNameToAdd,
		Tower: "another-tower",
	}
	if err := addCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to add branch %s to another tower: %v", branchNameToAdd, err)
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
	testTower = findTowerByName(repo1, "test-tower")
	if len(testTower.Branches) != 2 {
		t.Errorf("Expected test-tower to still have 2 branches, got %d", len(testTower.Branches))
	}

	// New tower should have 0 branches
	anotherTower := findTowerByName(repo1, "another-tower")
	if len(anotherTower.Branches) != 0 {
		t.Errorf("Expected another-tower to have 0 branches after removal, got %d", len(anotherTower.Branches))
	}
}

func TestBranchAddToCurrent(t *testing.T) {
	// Simulate stdin for base branch prompts using os.Pipe
	originalStdin := os.Stdin
	r, w, _ := os.Pipe()
	defer func() {
		os.Stdin = originalStdin
		r.Close()
	}()
	// Provide two inputs for the two towers getting their first branch
	go func() {
		defer w.Close()
		io.WriteString(w, "1\n1\n") // Input for explicit-tower-branch and current-tower-branch
	}()
	os.Stdin = r

	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "ghenga-branch-add-test")
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
	// Get the HEAD hash after initial commit for branch creation
	headRef, err := repo.Head()
	if err != nil {
		t.Fatalf("Failed to get HEAD ref: %v", err)
	}
	initialHeadHash := headRef.Hash()

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

	// Initialize with default tower
	initCmd := &InitCmd{
		DefaultTower: "default-tower",
	}
	if err := initCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to run Init command: %v", err)
	}

	// Create a second tower
	newCmd := &NewCmd{
		Name: "second-tower",
	}
	if err := newCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to create second tower: %v", err)
	}

	// Set the second tower as current
	currentCmd := &CurrentCmd{
		Tower: "second-tower",
	}
	if err := currentCmd.Run(mockCtx); err != nil {
		t.Fatalf("Failed to set current tower: %v", err)
	}

	// Create the branches in the git repo before adding them
	err = wt.Checkout(&git.CheckoutOptions{Hash: initialHeadHash}) // Reset HEAD
	if err != nil {
		t.Fatalf("Failed checkout initial commit: %v", err)
	}
	createTestBranch(t, repo, "explicit-tower-branch", 0)          // Use existing helper
	err = wt.Checkout(&git.CheckoutOptions{Hash: initialHeadHash}) // Reset HEAD
	if err != nil {
		t.Fatalf("Failed checkout initial commit: %v", err)
	}
	createTestBranch(t, repo, "current-tower-branch", 0)           // Use existing helper
	err = wt.Checkout(&git.CheckoutOptions{Hash: initialHeadHash}) // Reset HEAD
	if err != nil {
		t.Fatalf("Failed checkout initial commit: %v", err)
	}

	// Add a branch using AddCmd directly with default tower
	// This tests that we respect explicitly specified towers even when not "default"
	addCmdWithTower := &AddCmd{
		Name:  "explicit-tower-branch",
		Tower: "default-tower", // Explicitly specify the non-current tower
	}
	if err := addCmdWithTower.Run(mockCtx); err != nil {
		t.Fatalf("Failed to add branch to explicit tower: %v", err)
	}

	// Add a branch with unspecified tower (should go to current, but explicitly set for test robustness)
	addCmdCurrentTower := &AddCmd{
		Name:  "current-tower-branch",
		Tower: "second-tower", // Explicitly set tower instead of relying on current
	}
	if err := addCmdCurrentTower.Run(mockCtx); err != nil {
		t.Fatalf("Failed to add branch to second tower: %v", err) // Updated error message
	}

	// Verify the configuration
	config, err := LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if len(config.Repos) != 1 {
		t.Fatalf("Expected 1 repo, got %d", len(config.Repos))
	}

	repo1 := config.Repos[0]

	// Get both towers
	defaultTower := findTowerByName(repo1, "default-tower")
	if defaultTower == nil {
		t.Fatalf("Could not find default-tower")
	}

	secondTower := findTowerByName(repo1, "second-tower")
	if secondTower == nil {
		t.Fatalf("Could not find second-tower")
	}

	// Verify branch with explicit tower went to the default tower
	if len(defaultTower.Branches) != 1 {
		t.Errorf("Expected 1 branch in default-tower, got %d", len(defaultTower.Branches))
	} else if defaultTower.Branches[0].Name != "explicit-tower-branch" {
		t.Errorf("Expected branch name 'explicit-tower-branch', got '%s'", defaultTower.Branches[0].Name)
	}

	// Verify the branch with unspecified tower went to the current tower (second-tower)
	if len(secondTower.Branches) != 1 {
		t.Errorf("Expected 1 branch in second-tower, got %d", len(secondTower.Branches))
	} else if secondTower.Branches[0].Name != "current-tower-branch" {
		t.Errorf("Expected branch name 'current-tower-branch', got '%s'", secondTower.Branches[0].Name)
	}
}
