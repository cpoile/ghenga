package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestCurrentCommand(t *testing.T) {
	// Setup test repository
	repoPath, _ := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Temporarily change working directory
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(oldWd)
	os.Chdir(repoPath)

	// Create temporary config file
	configFile, err := os.CreateTemp("", "ghenga-config-*.toml")
	require.NoError(t, err)
	defer os.Remove(configFile.Name())

	// Mock the config path
	oldConfigPath := ConfigPath
	ConfigPath = mockedConfigPath(configFile.Name())
	defer func() { ConfigPath = oldConfigPath }()

	// Initialize config with two towers
	config := &Config{
		Repos: []*Repo{
			{
				Path: repoPath,
				Towers: []*Tower{
					{
						Name:     "tower-1",
						Branches: []Branch{},
					},
					{
						Name:     "tower-2",
						Branches: []Branch{},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Test setting current tower
	currentCmd := &CurrentCmd{
		Tower: "tower-2",
	}
	err = currentCmd.Run(mockCtx)
	require.NoError(t, err, "Failed to run Current command")

	// Verify the configuration was updated correctly
	config, err = LoadConfig()
	require.NoError(t, err, "Failed to load config")

	// Verify the current tower is set
	repo := FindRepoByPath(config, repoPath)
	require.NotNil(t, repo, "Repository not found in config")
	assert.Equal(t, "tower-2", repo.Current, "Expected current tower to be 'tower-2'")

	// Test setting another tower as current
	currentCmd = &CurrentCmd{
		Tower: "tower-1",
	}
	err = currentCmd.Run(mockCtx)
	require.NoError(t, err, "Failed to run Current command")

	// Verify the configuration was updated correctly
	config, err = LoadConfig()
	require.NoError(t, err, "Failed to load config")

	// Verify the current tower is updated
	repo = FindRepoByPath(config, repoPath)
	assert.Equal(t, "tower-1", repo.Current, "Expected current tower to be 'tower-1'")

	// Test setting a nonexistent tower (should fail)
	invalidCmd := &CurrentCmd{
		Tower: "nonexistent-tower",
	}
	err = invalidCmd.Run(mockCtx)
	assert.Error(t, err, "Expected error when setting nonexistent tower as current")

	// The current tower should still be tower-1
	config, _ = LoadConfig()
	repo = FindRepoByPath(config, repoPath)
	assert.Equal(t, "tower-1", repo.Current, "Current tower should still be 'tower-1'")
}

func TestGetCurrentTower(t *testing.T) {
	// Create a repo with multiple towers and a current tower set
	repo := &Repo{
		Path:    "/test/path",
		Current: "second-tower",
		Towers: []*Tower{
			{
				Name:     "first-tower",
				Branches: []Branch{},
			},
			{
				Name:     "second-tower",
				Branches: []Branch{},
			},
			{
				Name:     "third-tower",
				Branches: []Branch{},
			},
		},
	}

	// Test getting the current tower
	tower := GetCurrentTower(repo)
	require.NotNil(t, tower, "GetCurrentTower should return a tower")
	assert.Equal(t, "second-tower", tower.Name, "Expected to get 'second-tower'")

	// Test with nonexistent current tower name
	repo.Current = "nonexistent-tower"
	tower = GetCurrentTower(repo)
	require.NotNil(t, tower, "GetCurrentTower should fall back to the first tower")
	assert.Equal(t, "first-tower", tower.Name, "Expected to get 'first-tower' as fallback")

	// Test with empty current tower name
	repo.Current = ""
	tower = GetCurrentTower(repo)
	require.NotNil(t, tower, "GetCurrentTower should fall back to the first tower")
	assert.Equal(t, "first-tower", tower.Name, "Expected to get 'first-tower' as fallback")

	// Test with no towers
	repo.Towers = []*Tower{}
	tower = GetCurrentTower(repo)
	assert.Nil(t, tower, "GetCurrentTower should return nil when no towers exist")
}

func TestRenameCommand(t *testing.T) {
	// Setup test repository
	repoPath, _ := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Temporarily change working directory
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(oldWd)
	os.Chdir(repoPath)

	// Create temporary config file
	configFile, err := os.CreateTemp("", "ghenga-config-*.toml")
	require.NoError(t, err)
	defer os.Remove(configFile.Name())

	// Mock the config path
	oldConfigPath := ConfigPath
	ConfigPath = mockedConfigPath(configFile.Name())
	defer func() { ConfigPath = oldConfigPath }()

	// Test without a current tower set
	config := &Config{
		Repos: []*Repo{
			{
				Path: repoPath,
				Towers: []*Tower{
					{
						Name:     "tower-1",
						Branches: []Branch{},
					},
					{
						Name:     "tower-2",
						Branches: []Branch{},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// This should fail because no current tower is set
	renameCmd := &RenameCmd{
		NewName: "renamed-tower",
	}
	err = renameCmd.Run(mockCtx)
	assert.Error(t, err, "Rename should fail when no current tower is set")
	assert.Contains(t, err.Error(), "no current tower set", "Error should mention that no current tower is set")

	// Now set a current tower
	config.Repos[0].Current = "tower-1"
	err = SaveConfig(config)
	require.NoError(t, err)

	// Test renaming the current tower
	err = renameCmd.Run(mockCtx)
	require.NoError(t, err, "Failed to run Rename command")

	// Verify the configuration was updated correctly
	config, err = LoadConfig()
	require.NoError(t, err, "Failed to load config")

	// Verify the tower was renamed
	repo := FindRepoByPath(config, repoPath)
	require.NotNil(t, repo, "Repository not found in config")

	// The current tower should be updated to the new name
	assert.Equal(t, "renamed-tower", repo.Current, "Current tower reference should be updated")

	// Check that the tower was actually renamed
	var foundRenamedTower bool
	for _, tower := range repo.Towers {
		if tower.Name == "renamed-tower" {
			foundRenamedTower = true
			break
		}
	}
	assert.True(t, foundRenamedTower, "Tower should be renamed to 'renamed-tower'")

	// The old tower name should no longer exist
	var foundOldTower bool
	for _, tower := range repo.Towers {
		if tower.Name == "tower-1" {
			foundOldTower = true
			break
		}
	}
	assert.False(t, foundOldTower, "Tower 'tower-1' should no longer exist")

	// Test trying to rename to a name that already exists (should fail)
	renameCmd = &RenameCmd{
		NewName: "tower-2",
	}
	err = renameCmd.Run(mockCtx)
	assert.Error(t, err, "Expected error when renaming to a name that already exists")

	// Test with a non-existent current tower
	config.Repos[0].Current = "non-existent-tower"
	err = SaveConfig(config)
	require.NoError(t, err)

	renameCmd = &RenameCmd{
		NewName: "another-name",
	}
	err = renameCmd.Run(mockCtx)
	assert.Error(t, err, "Expected error when current tower doesn't exist")
	assert.Contains(t, err.Error(), "not found", "Error should mention that the current tower was not found")
}

func TestBaseCommand(t *testing.T) {
	// Setup test repository
	repoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Create a test commit to use as base
	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create a file
	filePath := filepath.Join(repoPath, "base-test.txt")
	err = os.WriteFile(filePath, []byte("base test content"), 0644)
	require.NoError(t, err)

	// Add and commit the file
	_, err = wt.Add("base-test.txt")
	require.NoError(t, err)

	commit, err := wt.Commit("Base commit for test", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Get the commit as a string
	baseCommit := commit.String()

	// Temporarily change working directory
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(oldWd)
	os.Chdir(repoPath)

	// Create temporary config file
	configFile, err := os.CreateTemp("", "ghenga-config-*.toml")
	require.NoError(t, err)
	defer os.Remove(configFile.Name())

	// Mock the config path
	oldConfigPath := ConfigPath
	ConfigPath = mockedConfigPath(configFile.Name())
	defer func() { ConfigPath = oldConfigPath }()

	// Initialize config with a tower and set it as current
	config := &Config{
		Repos: []*Repo{
			{
				Path:    repoPath,
				Current: "test-tower",
				Towers: []*Tower{
					{
						Name:     "test-tower",
						Branches: []Branch{},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Test setting base commit
	baseCmd := &BaseCmd{
		Commit: baseCommit,
	}
	err = baseCmd.Run(mockCtx)
	require.NoError(t, err, "Failed to run Base command")

	// Verify the configuration was updated correctly
	config, err = LoadConfig()
	require.NoError(t, err, "Failed to load config")

	// Verify the base commit was set
	repo1 := FindRepoByPath(config, repoPath)
	require.NotNil(t, repo1, "Repository not found in config")

	tower := FindTowerByName(repo1, "test-tower")
	require.NotNil(t, tower, "Tower not found in config")

	assert.Equal(t, baseCommit, tower.Base, "Base commit should be set correctly")

	// Test with no current tower set
	repo1.Current = ""
	err = SaveConfig(config)
	require.NoError(t, err)

	err = baseCmd.Run(mockCtx)
	assert.Error(t, err, "Base should fail when no current tower is set")
	assert.Contains(t, err.Error(), "no current tower set", "Error should mention that no current tower is set")

	// Test with non-existent commit
	repo1.Current = "test-tower"
	err = SaveConfig(config)
	require.NoError(t, err)

	invalidBaseCmd := &BaseCmd{
		Commit: "nonexistentcommit",
	}
	err = invalidBaseCmd.Run(mockCtx)
	assert.Error(t, err, "Base should fail with non-existent commit")
	assert.Contains(t, err.Error(), "failed to resolve commit", "Error should mention that the commit could not be resolved")
}

func TestRmTowerCommand(t *testing.T) {
	// Setup test repository
	repoPath, _ := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Temporarily change working directory
	oldWd, err := os.Getwd()
	require.NoError(t, err)
	defer os.Chdir(oldWd)
	os.Chdir(repoPath)

	// Create temporary config file
	configFile, err := os.CreateTemp("", "ghenga-config-*.toml")
	require.NoError(t, err)
	defer os.Remove(configFile.Name())

	// Mock the config path
	oldConfigPath := ConfigPath
	ConfigPath = mockedConfigPath(configFile.Name())
	defer func() { ConfigPath = oldConfigPath }()

	// Initialize config with multiple towers including the current tower
	config := &Config{
		Repos: []*Repo{
			{
				Path:    repoPath,
				Current: "tower-1",
				Towers: []*Tower{
					{
						Name: "tower-1",
						Branches: []Branch{
							{Name: "branch-1"},
							{Name: "branch-2"},
						},
					},
					{
						Name: "tower-2",
						Branches: []Branch{
							{Name: "branch-3"},
						},
					},
					{
						Name:     "tower-3",
						Branches: []Branch{},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Test 1: Remove a non-current tower with confirmation
	// Mock standard input with "y" for yes
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	go func() {
		w.Write([]byte("y\n"))
		w.Close()
	}()

	rmCmd := &RmTowerCmd{
		Name: "tower-3",
	}

	output, err := CaptureOutput(func() error {
		return rmCmd.Run(mockCtx)
	})
	require.NoError(t, err, "Failed to run Rm command for a non-current tower")

	// Restore stdin
	os.Stdin = oldStdin

	// Verify the output contains the warning
	assert.Contains(t, output, "WARNING", "Output should contain a warning message")
	assert.Contains(t, output, "tower-3", "Output should mention the tower being removed")
	assert.Contains(t, output, "Removed tower 'tower-3'", "Output should confirm removal")
	assert.NotContains(t, output, "branch", "Output should not mention branches for an empty tower")

	// Verify the configuration was updated correctly
	config, err = LoadConfig()
	require.NoError(t, err, "Failed to load config")

	// Verify the tower was removed
	repo := FindRepoByPath(config, repoPath)
	require.NotNil(t, repo, "Repository not found in config")

	// Check that only two towers remain
	assert.Equal(t, 2, len(repo.Towers), "Expected 2 towers after removal")

	// Check that the removed tower doesn't exist
	var foundTower3 bool
	for _, tower := range repo.Towers {
		if tower.Name == "tower-3" {
			foundTower3 = true
			break
		}
	}
	assert.False(t, foundTower3, "Tower 'tower-3' should no longer exist")

	// Current tower should still be set
	assert.Equal(t, "tower-1", repo.Current, "Current tower should still be 'tower-1'")

	// Test 2: Remove the current tower with confirmation
	// Mock standard input again with "y" for yes
	r, w, _ = os.Pipe()
	os.Stdin = r
	go func() {
		w.Write([]byte("y\n"))
		w.Close()
	}()

	rmCurrentCmd := &RmTowerCmd{
		Name: "tower-1",
	}

	output, err = CaptureOutput(func() error {
		return rmCurrentCmd.Run(mockCtx)
	})
	require.NoError(t, err, "Failed to run Rm command for the current tower")

	// Restore stdin
	os.Stdin = oldStdin

	// Verify the output contains the correct warnings
	assert.Contains(t, output, "WARNING", "Output should contain a warning message")
	assert.Contains(t, output, "current tower", "Output should mention it's the current tower")
	assert.Contains(t, output, "2 branch", "Output should mention the number of branches")
	assert.Contains(t, output, "Removed tower 'tower-1'", "Output should confirm removal")
	assert.Contains(t, output, "Note: Removed the current tower", "Output should warn about removing current tower")

	// Verify the configuration was updated correctly
	config, err = LoadConfig()
	require.NoError(t, err, "Failed to load config")

	repo = FindRepoByPath(config, repoPath)

	// Only one tower should remain
	assert.Equal(t, 1, len(repo.Towers), "Expected 1 tower after removing current tower")

	// Current tower reference should be empty
	assert.Equal(t, "", repo.Current, "Current tower should be unset after removing it")

	// Test 3: Try to remove a tower but cancel the operation
	// Mock standard input with "n" for no
	r, w, _ = os.Pipe()
	os.Stdin = r
	go func() {
		w.Write([]byte("n\n"))
		w.Close()
	}()

	cancelCmd := &RmTowerCmd{
		Name: "tower-2",
	}

	output, err = CaptureOutput(func() error {
		return cancelCmd.Run(mockCtx)
	})
	require.NoError(t, err, "Command should run without error even when cancelled")

	// Restore stdin
	os.Stdin = oldStdin

	// Verify the output indicates cancellation
	assert.Contains(t, output, "WARNING", "Output should contain a warning message")
	assert.Contains(t, output, "tower-2", "Output should mention the tower name")
	assert.Contains(t, output, "cancelled", "Output should indicate operation was cancelled")

	// Verify the configuration was not changed
	config, err = LoadConfig()
	require.NoError(t, err, "Failed to load config")

	repo = FindRepoByPath(config, repoPath)
	assert.Equal(t, 1, len(repo.Towers), "Expected tower to still exist after cancellation")
	assert.Equal(t, "tower-2", repo.Towers[0].Name, "tower-2 should still exist")

	// Test 4: Try to remove a non-existent tower
	rmNonExistentCmd := &RmTowerCmd{
		Name: "non-existent-tower",
	}
	err = rmNonExistentCmd.Run(mockCtx)
	assert.Error(t, err, "Expected error when removing non-existent tower")
	assert.Contains(t, err.Error(), "not found", "Error should mention that the tower was not found")

	// Test 5: Remove the last tower with confirmation
	// Mock standard input with "y" for yes
	r, w, _ = os.Pipe()
	os.Stdin = r
	go func() {
		w.Write([]byte("y\n"))
		w.Close()
	}()

	rmLastCmd := &RmTowerCmd{
		Name: "tower-2",
	}

	output, err = CaptureOutput(func() error {
		return rmLastCmd.Run(mockCtx)
	})
	require.NoError(t, err, "Failed to run Rm command for the last tower")

	// Restore stdin
	os.Stdin = oldStdin

	// Verify the output
	assert.Contains(t, output, "WARNING", "Output should contain a warning message")
	assert.Contains(t, output, "1 branch", "Output should mention the number of branches")
	assert.Contains(t, output, "Removed tower 'tower-2'", "Output should confirm removal")

	// Verify the configuration was updated correctly
	config, err = LoadConfig()
	require.NoError(t, err, "Failed to load config")

	repo = FindRepoByPath(config, repoPath)
	assert.Equal(t, 0, len(repo.Towers), "Expected 0 towers after removing the last tower")
}
