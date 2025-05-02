package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// KongRunnable defines an interface for commands that can be run via Kong.
// This helps in creating generic test helpers.
type KongRunnable interface {
	Run(*kong.Context) error
}

// runTowerCommandAndGetRepo runs a KongRunnable command, reloads the config,
// finds the repo config for the given path, and returns it along with any error
// from the command execution.
func runTowerCommandAndGetRepo(t *testing.T, cmd KongRunnable, ctx *kong.Context, repoPath string) (*RepoInfo, error) {
	t.Helper()
	runErr := cmd.Run(ctx) // Run the command first

	config, err := LoadConfig()
	// Use require for fatal errors in test setup/verification steps
	require.NoError(t, err, "Failed to load config after running command")

	repoConfig := findRepoByPath(config, repoPath)
	require.NotNil(t, repoConfig, "Repository %s not found in config after running command", repoPath)

	return repoConfig, runErr // Return the repo state *after* the command ran, and the command's error
}

func TestTower_NewCommand(t *testing.T) {
	// Setup test environment (handles repo, config, CWD)
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	// Resolve symlinks for comparison later if needed (though setupTestEnv might handle this)
	realRepoPath, err := filepath.EvalSymlinks(repoPath)
	require.NoError(t, err, "Failed to resolve symlinks")

	// Initial config is empty, setupTestEnv handles mock path

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Create a new tower using the New command
	newCmd := &NewCmd{
		Name: "feature-tower",
	}
	err = newCmd.Run(mockCtx)
	require.NoError(t, err, "Failed to run New command")

	// Verify the configuration was saved correctly
	config, err := LoadConfig()
	require.NoError(t, err, "Failed to load config")

	// Verify the repo was added
	require.Len(t, config.Repos, 1, "Expected 1 repo")

	repo1 := config.Repos[0]
	// Compare the real paths to account for symlinks
	configRepoPath, err := filepath.EvalSymlinks(repo1.Path)
	require.NoError(t, err, "Failed to resolve symlinks in config repo path")
	assert.Equal(t, realRepoPath, configRepoPath, "Repo path mismatch")

	// Verify the tower was added
	require.Len(t, repo1.Towers, 1, "Expected 1 tower initially")

	// Find and verify the tower
	featureTower := findTowerByName(repo1, "feature-tower")
	require.NotNil(t, featureTower, "Could not find feature-tower")

	// Verify the tower has no branches initially
	assert.Empty(t, featureTower.Branches, "Expected 0 branches in feature-tower")

	// Create another tower
	newCmd2 := &NewCmd{
		Name: "bugfix-tower",
	}
	err = newCmd2.Run(mockCtx)
	require.NoError(t, err, "Failed to run New command for second tower")

	// Verify the configuration again
	config, err = LoadConfig()
	require.NoError(t, err, "Failed to load config")

	// Verify we now have two towers
	require.Len(t, config.Repos[0].Towers, 2, "Expected 2 towers")

	// Try to create a duplicate tower (should fail)
	duplicateCmd := &NewCmd{
		Name: "feature-tower",
	}
	err = duplicateCmd.Run(mockCtx)
	assert.Error(t, err, "Expected error when adding duplicate tower, but got none")
}

func TestTower_CurrentCommand(t *testing.T) {
	// Setup test environment
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	// Initialize config with two towers
	towers := []*Tower{
		{
			Name:     "tower-1",
			Branches: []Branch{},
		},
		{
			Name:     "tower-2",
			Branches: []Branch{},
		},
	}
	config := createTestConfig(t, repoPath, "", towers, "") // Initially no current tower
	err := SaveConfig(config)
	require.NoError(t, err)

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Test setting current tower
	currentCmd := &CurrentCmd{
		Tower: "tower-2",
	}
	repoConfig, err := runTowerCommandAndGetRepo(t, currentCmd, mockCtx, repoPath)
	require.NoError(t, err, "Failed to run Current command")
	// Verify the current tower is set
	assert.Equal(t, "tower-2", repoConfig.Current, "Expected current tower to be 'tower-2'")

	// Test setting another tower as current
	currentCmd = &CurrentCmd{
		Tower: "tower-1",
	}
	repoConfig, err = runTowerCommandAndGetRepo(t, currentCmd, mockCtx, repoPath)
	require.NoError(t, err, "Failed to run Current command")
	// Verify the current tower is updated
	assert.Equal(t, "tower-1", repoConfig.Current, "Expected current tower to be 'tower-1'")

	// Test setting a nonexistent tower (should fail)
	invalidCmd := &CurrentCmd{
		Tower: "nonexistent-tower",
	}
	repoConfig, err = runTowerCommandAndGetRepo(t, invalidCmd, mockCtx, repoPath)
	assert.Error(t, err, "Expected error when setting nonexistent tower as current")

	// The current tower should still be tower-1 (check the latest loaded state)
	assert.Equal(t, "tower-1", repoConfig.Current, "Current tower should still be 'tower-1'")
}

func TestTower_GetCurrentTower(t *testing.T) {
	// Create a repo with multiple towers and a current tower set
	repo := &RepoInfo{
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

func TestTower_RenameCommand(t *testing.T) {
	// Setup test environment
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	// Test without a current tower set
	towers := []*Tower{
		{
			Name:     "tower-1",
			Branches: []Branch{},
		},
		{
			Name:     "tower-2",
			Branches: []Branch{},
		},
	}
	config := createTestConfig(t, repoPath, "", towers, "")
	err := SaveConfig(config)
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
	loadedConfig, err := LoadConfig() // Load fresh config
	require.NoError(t, err)
	loadedConfig.Repos[0].Current = "tower-1"
	err = SaveConfig(loadedConfig)
	require.NoError(t, err)

	// Test renaming the current tower
	repoConfig, err := runTowerCommandAndGetRepo(t, renameCmd, mockCtx, repoPath)
	require.NoError(t, err, "Failed to run Rename command")

	// Verify the tower was renamed
	// The current tower should be updated to the new name
	assert.Equal(t, "renamed-tower", repoConfig.Current, "Current tower reference should be updated")

	// Check that the tower was actually renamed
	var foundRenamedTower bool
	for _, tower := range repoConfig.Towers {
		if tower.Name == "renamed-tower" {
			foundRenamedTower = true
			break
		}
	}
	assert.True(t, foundRenamedTower, "Tower should be renamed to 'renamed-tower'")

	// The old tower name should no longer exist
	var foundOldTower bool
	for _, tower := range repoConfig.Towers {
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
	loadedConfig, err = LoadConfig() // Load fresh config
	require.NoError(t, err)
	loadedConfig.Repos[0].Current = "non-existent-tower"
	err = SaveConfig(loadedConfig)
	require.NoError(t, err)

	renameCmd = &RenameCmd{
		NewName: "another-name",
	}
	_, err = runTowerCommandAndGetRepo(t, renameCmd, mockCtx, repoPath)
	assert.Error(t, err, "Expected error when current tower doesn't exist")
	assert.Contains(t, err.Error(), "not found", "Error should mention that the current tower was not found")
}

func TestTower_BaseCommand(t *testing.T) {
	// Setup test environment
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Create a test commit to use as base
	wt, err := repo.Worktree()
	require.NoError(t, err)

	commitHash := addSingleCommit(t, repoPath, wt, "base-test.txt", "base test content", "Base commit for test")

	// Get the commit hash as a string
	baseCommit := commitHash.String()

	// Initialize config with a tower and set it as current
	towerName := "test-tower"
	towers := []*Tower{
		{
			Name:     towerName,
			Branches: []Branch{},
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, "") // Base will be set by the command
	err = SaveConfig(config)
	require.NoError(t, err)

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Test setting base commit
	baseCmd := &BaseCmd{
		BaseBranch: "main",
	}
	repoConfig, err := runTowerCommandAndGetRepo(t, baseCmd, mockCtx, repoPath)
	require.NoError(t, err, "Failed to run Base command")

	// Verify the base commit was set
	tower := findTowerByName(repoConfig, towerName)
	require.NotNil(t, tower, "Tower not found in config")
	towerBaseHash, err := repo.ResolveRevision(plumbing.Revision(tower.Base))
	assert.NoError(t, err)
	assert.Equal(t, baseCommit, towerBaseHash.String(), "Base commit should be set correctly")

	// Test with no current tower set
	loadedConfig, err := LoadConfig()
	require.NoError(t, err)
	loadedConfig.Repos[0].Current = ""
	err = SaveConfig(loadedConfig)
	require.NoError(t, err)

	err = baseCmd.Run(mockCtx)
	assert.Error(t, err, "Base should fail when no current tower is set")
	assert.Contains(t, err.Error(), "no current tower set", "Error should mention that no current tower is set")

	// Test with non-existent commit
	loadedConfig, err = LoadConfig()
	require.NoError(t, err)
	loadedConfig.Repos[0].Current = towerName // Set current back
	err = SaveConfig(loadedConfig)
	require.NoError(t, err)

	invalidBaseCmd := &BaseCmd{
		BaseBranch: "nonexistent-branch",
	}
	_, err = runTowerCommandAndGetRepo(t, invalidBaseCmd, mockCtx, repoPath)
	assert.Error(t, err, "Base should fail with non-existent commit")
	assert.Contains(t, err.Error(), "branch 'nonexistent-branch' not found locally or on origin", "Error should mention that the commit could not be resolved")
}

func TestTower_RmTowerCommand(t *testing.T) {
	// Setup test environment
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Create some branches for the towers
	createTestBranch(t, repo, "branch-1", 1)
	createTestBranch(t, repo, "branch-2", 1)
	createTestBranch(t, repo, "branch-3", 1)

	// Initialize config with multiple towers including the current tower
	towers := []*Tower{
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
			Branches: []Branch{}, // Keep one empty
		},
	}
	config := createTestConfig(t, repoPath, "tower-1", towers, "")
	err := SaveConfig(config)
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
	repoConfig := findRepoByPath(config, repoPath)
	require.NotNil(t, repoConfig, "Repository not found in config")

	// Check that only two towers remain
	assert.Equal(t, 2, len(repoConfig.Towers), "Expected 2 towers after removal")

	// Check that the removed tower doesn't exist
	assert.Nil(t, findTowerByName(repoConfig, "tower-3"), "Tower 'tower-3' should no longer exist")

	// Current tower should still be set
	assert.Equal(t, "tower-1", repoConfig.Current, "Current tower should still be 'tower-1'")

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
	repoConfig = findRepoByPath(config, repoPath)
	require.NotNil(t, repoConfig, "Repository not found in config")

	// Only one tower should remain
	assert.Equal(t, 1, len(repoConfig.Towers), "Expected 1 tower after removing current tower")
	assert.Nil(t, findTowerByName(repoConfig, "tower-1"), "Tower 'tower-1' should no longer exist")

	// Current tower reference should be empty
	assert.Equal(t, "", repoConfig.Current, "Current tower should be unset after removing it")

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
	repoConfig = findRepoByPath(config, repoPath)
	require.NotNil(t, repoConfig, "Repository not found in config")
	assert.Equal(t, 1, len(repoConfig.Towers), "Expected tower to still exist after cancellation")
	assert.NotNil(t, findTowerByName(repoConfig, "tower-2"), "tower-2 should still exist")

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
	repoConfig = findRepoByPath(config, repoPath)
	require.NotNil(t, repoConfig, "Repository not found in config")
	assert.Equal(t, 0, len(repoConfig.Towers), "Expected 0 towers after removing the last tower")
}
