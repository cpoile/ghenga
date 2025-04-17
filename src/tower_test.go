package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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

func TestListCmd_NoTowers(t *testing.T) {
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

	// Initialize empty config
	config := &Config{
		Repos: []*Repo{
			{
				Path:   repoPath,
				Towers: []*Tower{},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Run the list command
	cmd := &ListCmd{}
	output, err := CaptureOutput(func() error {
		return cmd.Run(nil)
	})
	require.NoError(t, err)

	// Verify output doesn't contain any towers
	assert.NotContains(t, output, "Tower:")
}

func TestListCmd_OneTowerNoRepositories(t *testing.T) {
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

	// Initialize config with one tower but no branches
	config := &Config{
		Repos: []*Repo{
			{
				Path: repoPath,
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

	// Run the list command
	cmd := &ListCmd{}
	output, err := CaptureOutput(func() error {
		return cmd.Run(nil)
	})
	require.NoError(t, err)

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")
	assert.NotContains(t, output, "Initial commit")
}

func TestListCmd_OneTowerOneBranchNoCommits(t *testing.T) {
	// Setup test repository
	repoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Create a branch but don't add any commits
	branchName := "feature-branch"
	headRef, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), headRef.Hash())
	err = repo.Storer.SetReference(branchRef)
	require.NoError(t, err)

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

	// Initialize config with one tower and one branch
	config := &Config{
		Repos: []*Repo{
			{
				Path: repoPath,
				Towers: []*Tower{
					{
						Name: "test-tower",
						Branches: []Branch{
							{Name: branchName},
						},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Run the list command
	cmd := &ListCmd{}
	output, err := CaptureOutput(func() error {
		return cmd.Run(nil)
	})
	require.NoError(t, err)

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")
	assert.Contains(t, output, branchName)
	assert.Contains(t, output, "Initial commit")
}

func TestListCmd_OneTowerOneBranchWithCommits(t *testing.T) {
	// Setup test repository
	repoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Create a branch with commits
	branchName := "feature-branch"
	createTestBranch(t, repo, branchName, 3)

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

	// Initialize config with one tower and one branch
	config := &Config{
		Repos: []*Repo{
			{
				Path: repoPath,
				Towers: []*Tower{
					{
						Name: "test-tower",
						Branches: []Branch{
							{Name: branchName},
						},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Run the list command
	cmd := &ListCmd{}
	output, err := CaptureOutput(func() error {
		return cmd.Run(nil)
	})
	require.NoError(t, err)

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")
	assert.Contains(t, output, branchName)
	assert.Contains(t, output, "Add file-feature-branch-0.txt")
	assert.Contains(t, output, "Add file-feature-branch-1.txt")
	assert.Contains(t, output, "Add file-feature-branch-2.txt")

	// Verify ordering - the most recent commit (2) should come before older commits (1 and 0)
	pos2 := strings.Index(output, "Add file-feature-branch-2.txt")
	pos1 := strings.Index(output, "Add file-feature-branch-1.txt")
	pos0 := strings.Index(output, "Add file-feature-branch-0.txt")
	assert.True(t, pos2 < pos1, "Most recent commit should be listed first")
	assert.True(t, pos1 < pos0, "Commits should be in reverse chronological order")
}

func TestListCmd_MultipleTowersMultipleBranches(t *testing.T) {
	// Setup test repository
	repoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Create branches with commits
	createTestBranch(t, repo, "feature-1", 2)
	createTestBranch(t, repo, "feature-2", 3)
	createTestBranch(t, repo, "bugfix-1", 1)
	createTestBranch(t, repo, "bugfix-2", 2)

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

	// Initialize config with multiple towers and branches
	config := &Config{
		Repos: []*Repo{
			{
				Path: repoPath,
				Towers: []*Tower{
					{
						Name: "feature-tower",
						Branches: []Branch{
							{Name: "feature-1"},
							{Name: "feature-2"},
						},
					},
					{
						Name: "bugfix-tower",
						Branches: []Branch{
							{Name: "bugfix-1"},
							{Name: "bugfix-2"},
						},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Run the list command
	cmd := &ListCmd{}
	output, err := CaptureOutput(func() error {
		return cmd.Run(nil)
	})
	require.NoError(t, err)

	// Verify output
	assert.Contains(t, output, "Tower: feature-tower")
	assert.Contains(t, output, "Tower: bugfix-tower")

	// Check branch ordering (reverse order)
	feature2Index := strings.Index(output, "feature-2")
	feature1Index := strings.Index(output, "feature-1")
	assert.True(t, feature2Index < feature1Index, "feature-2 should be listed before feature-1")

	bugfix2Index := strings.Index(output, "bugfix-2")
	bugfix1Index := strings.Index(output, "bugfix-1")
	assert.True(t, bugfix2Index < bugfix1Index, "bugfix-2 should be listed before bugfix-1")

	// Check for commit messages
	assert.Contains(t, output, "Add file-feature-1-0.txt")
	assert.Contains(t, output, "Add file-feature-1-1.txt")
	assert.Contains(t, output, "Add file-feature-2-0.txt")
	assert.Contains(t, output, "Add file-feature-2-1.txt")
	assert.Contains(t, output, "Add file-feature-2-2.txt")
	assert.Contains(t, output, "Add file-bugfix-1-0.txt")
	assert.Contains(t, output, "Add file-bugfix-2-0.txt")
	assert.Contains(t, output, "Add file-bugfix-2-1.txt")

	// Verify ordering of commits within feature-2
	f2Pos2 := strings.Index(output, "Add file-feature-2-2.txt")
	f2Pos1 := strings.Index(output, "Add file-feature-2-1.txt")
	f2Pos0 := strings.Index(output, "Add file-feature-2-0.txt")
	assert.True(t, f2Pos2 < f2Pos1, "Most recent commit should be listed first")
	assert.True(t, f2Pos1 < f2Pos0, "Commits should be in reverse chronological order")
}

func TestListCmd_FilterByTowerName(t *testing.T) {
	// Setup test repository
	repoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Create branches with commits
	createTestBranch(t, repo, "feature-1", 2)
	createTestBranch(t, repo, "bugfix-1", 1)

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

	// Initialize config with multiple towers and branches
	config := &Config{
		Repos: []*Repo{
			{
				Path: repoPath,
				Towers: []*Tower{
					{
						Name: "feature-tower",
						Branches: []Branch{
							{Name: "feature-1"},
						},
					},
					{
						Name: "bugfix-tower",
						Branches: []Branch{
							{Name: "bugfix-1"},
						},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Run the list command with tower filter
	cmd := &ListCmd{TowerName: "feature-tower"}
	output, err := CaptureOutput(func() error {
		return cmd.Run(nil)
	})
	require.NoError(t, err)

	// Verify output
	assert.Contains(t, output, "Tower: feature-tower")
	assert.NotContains(t, output, "Tower: bugfix-tower")
	assert.Contains(t, output, "feature-1")
	assert.NotContains(t, output, "bugfix-1")
	assert.Contains(t, output, "Add file-feature-1-0.txt")
	assert.Contains(t, output, "Add file-feature-1-1.txt")
}

func TestListCmd_StaggeredCommitView(t *testing.T) {
	// Setup test repository
	repoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Create a stack of branches with specific commits
	// main -> feature-base -> feature-middle -> feature-top

	// Get the initial main branch reference (created by setupTestRepo)
	headRef, err := repo.Head()
	require.NoError(t, err)
	mainHash := headRef.Hash()

	// Create feature-base branch with 2 commits
	createTestBranch(t, repo, "feature-base", 2)

	// Get the feature-base reference
	_, err = repo.Reference(plumbing.NewBranchReferenceName("feature-base"), true)
	require.NoError(t, err)

	// Create feature-middle branch with 3 commits
	createTestBranch(t, repo, "feature-middle", 3)

	// Get the feature-middle reference
	_, err = repo.Reference(plumbing.NewBranchReferenceName("feature-middle"), true)
	require.NoError(t, err)

	// Create feature-top branch with 2 commits
	createTestBranch(t, repo, "feature-top", 2)

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

	// Initialize config with one tower and a stack of branches in order
	config := &Config{
		Repos: []*Repo{
			{
				Path:    repoPath,
				Current: "stacked-tower",
				Towers: []*Tower{
					{
						Name: "stacked-tower",
						Base: mainHash.String(), // Set the main commit as the base
						Branches: []Branch{
							{Name: "feature-base"},
							{Name: "feature-middle"},
							{Name: "feature-top"},
						},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Run the list command
	cmd := &ListCmd{}
	output, err := CaptureOutput(func() error {
		return cmd.Run(mockCtx)
	})
	require.NoError(t, err)

	// Verify output
	// 1. Should contain all three branches in the correct order
	assert.Contains(t, output, "Tower: stacked-tower")

	// Find positions of each branch in the output to verify order
	featureTopPos := strings.Index(output, "feature-top (current)\n")
	assert.True(t, featureTopPos != -1, "feature-top should be in the output")
	featureMiddlePos := strings.Index(output, "feature-middle\n")
	assert.True(t, featureMiddlePos != -1, "feature-middle should be in the output")
	featureBasePos := strings.Index(output, "feature-base\n")
	assert.True(t, featureBasePos != -1, "feature-base should be in the output")

	// Verify branches are in the correct order (top-to-bottom)
	assert.True(t, featureTopPos < featureMiddlePos, "feature-top should be listed before feature-middle")
	assert.True(t, featureMiddlePos < featureBasePos, "feature-middle should be listed before feature-base")

	// 2. The base branch (feature-base) should show commits up to the base commit
	// It has 2 commits of its own + the base commit marker
	assert.Contains(t, output, "file-feature-base-0.txt")
	assert.Contains(t, output, "file-feature-base-1.txt")
	assert.Contains(t, output, "(base)")

	// 3. The middle branch should only show its unique commits (not feature-base commits)
	assert.Contains(t, output, "file-feature-middle-0.txt")
	assert.Contains(t, output, "file-feature-middle-1.txt")
	assert.Contains(t, output, "file-feature-middle-2.txt")

	// 4. The top branch should only show its unique commits (not feature-middle or feature-base commits)
	assert.Contains(t, output, "file-feature-top-0.txt")
	assert.Contains(t, output, "file-feature-top-1.txt")

	// 5. Verify separation - feature-base section should not contain feature-middle or feature-top commits
	baseSection := output[featureBasePos:]
	assert.NotContains(t, baseSection, "file-feature-middle", "Base branch section should not contain middle branch commits")
	assert.NotContains(t, baseSection, "file-feature-top", "Base branch section should not contain top branch commits")

	// 6. Verify separation - feature-middle section should not contain feature-top or feature-base commits
	middleSection := output[featureMiddlePos:featureBasePos]
	assert.NotContains(t, middleSection, "file-feature-top", "Middle branch section should not contain top branch commits")
	assert.NotContains(t, middleSection, "file-feature-base", "Middle branch section should not contain base branch commits")

	// 7. Verify separation - feature-top section should not contain feature-middle or feature-base commits
	topSection := output[featureTopPos:featureMiddlePos]
	assert.NotContains(t, topSection, "file-feature-middle", "Top branch section should not contain middle branch commits")
	assert.NotContains(t, topSection, "file-feature-base", "Top branch section should not contain base branch commits")

	// Run the list command with specific tower name
	cmdWithName := &ListCmd{TowerName: "stacked-tower"}
	outputWithName, err := CaptureOutput(func() error {
		return cmdWithName.Run(mockCtx)
	})
	require.NoError(t, err)

	// Verify filtering by name produces the same output
	assert.Equal(t, output, outputWithName, "Filtering by tower name should produce the same output")

	// Test case for when the tower has no base commit set
	config.Repos[0].Towers[0].Base = ""
	err = SaveConfig(config)
	require.NoError(t, err)

	outputNoBase, err := CaptureOutput(func() error {
		return cmd.Run(mockCtx)
	})
	require.NoError(t, err)

	// Verify output without base
	assert.Contains(t, outputNoBase, "Tower: stacked-tower")
	assert.NotContains(t, outputNoBase, "(base)", "Output should not contain base marker when no base is set")
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

func TestListCmd_MiddleBranchDivergence(t *testing.T) {
	// Setup test repository
	repoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Get the initial main branch reference
	headRef, err := repo.Head()
	require.NoError(t, err)
	mainHash := headRef.Hash()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Step 1: Create the base branch with 3 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Create: true,
		Branch: plumbing.NewBranchReferenceName("base-branch"),
	})
	require.NoError(t, err)

	// Add 3 commits to base branch
	for i := 0; i < 3; i++ {
		filename := fmt.Sprintf("file-base-branch-%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)
		_, err = wt.Add(filename)
		require.NoError(t, err)
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
			},
		})
		require.NoError(t, err)
	}

	// Get base branch reference
	baseRef, err := repo.Reference(plumbing.NewBranchReferenceName("base-branch"), true)
	require.NoError(t, err)

	// Step 2: Create branch-2 (middle-branch) off base branch's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   baseRef.Hash(),
		Create: true,
		Branch: plumbing.NewBranchReferenceName("middle-branch"),
	})
	require.NoError(t, err)

	// Add 2 commits to middle branch
	for i := 0; i < 2; i++ {
		filename := fmt.Sprintf("file-middle-branch-%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)
		_, err = wt.Add(filename)
		require.NoError(t, err)
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
			},
		})
		require.NoError(t, err)
	}

	// Get middle branch reference (before adding more commits)
	middleRef, err := repo.Reference(plumbing.NewBranchReferenceName("middle-branch"), true)
	require.NoError(t, err)

	// Step 3: Create branch-3 (top-branch) off branch-2's HEAD, with 3 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   middleRef.Hash(),
		Create: true,
		Branch: plumbing.NewBranchReferenceName("top-branch"),
	})
	require.NoError(t, err)

	// Add 3 commits to top branch
	for i := 0; i < 3; i++ {
		filename := fmt.Sprintf("file-top-branch-%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)
		_, err = wt.Add(filename)
		require.NoError(t, err)
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
			},
		})
		require.NoError(t, err)
	}

	// Step 4: Checkout branch-2, and create one more commit off branch-2's head
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("middle-branch"),
	})
	require.NoError(t, err)

	// Add divergent commit to middle branch
	filePath := filepath.Join(repoPath, "divergent-middle.txt")
	err = os.WriteFile(filePath, []byte("divergent content"), 0644)
	require.NoError(t, err)
	_, err = wt.Add("divergent-middle.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Divergent commit on middle branch", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

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

	// Initialize config with one tower and all branches in order
	config := &Config{
		Repos: []*Repo{
			{
				Path:    repoPath,
				Current: "test-tower",
				Towers: []*Tower{
					{
						Name: "test-tower",
						Base: mainHash.String(),
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
	require.NoError(t, err)

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Run the list command
	cmd := &ListCmd{}
	output, err := CaptureOutput(func() error {
		return cmd.Run(mockCtx)
	})
	require.NoError(t, err)

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")

	// Check for divergence warning
	assert.Contains(t, output, "⚠️ This branch has diverged", "Output should contain divergence warning")

	// Verify the top branch shows divergence from middle branch
	topPos := strings.Index(output, "top-branch\n")
	assert.True(t, topPos != -1, "Top branch should be in the output")
	middlePos := strings.Index(output, "middle-branch (current)\n")
	assert.True(t, middlePos != -1, "Middle branch should be in the output")
	divergencePos := strings.Index(output, "⚠️ This branch has diverged")
	assert.True(t, divergencePos != -1, "Divergence warning should be in the output")

	assert.True(t, topPos < divergencePos, "Divergence warning should appear in top branch section")
	assert.True(t, divergencePos < middlePos, "Divergence warning should appear before middle branch section")

	// Verify the divergent commit message is in the output
	assert.Contains(t, output, "Divergent commit on middle branch", "Divergent commit should be shown")
}

func TestListCmd_TopBranchDivergence(t *testing.T) {
	// Setup test repository
	repoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Get the initial main branch reference
	headRef, err := repo.Head()
	require.NoError(t, err)
	mainHash := headRef.Hash()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Step 1: Create the base branch with 3 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Create: true,
		Branch: plumbing.NewBranchReferenceName("base-branch"),
	})
	require.NoError(t, err)

	// Add 3 commits to base branch
	for i := 0; i < 3; i++ {
		filename := fmt.Sprintf("file-base-branch-%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)
		_, err = wt.Add(filename)
		require.NoError(t, err)
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
			},
		})
		require.NoError(t, err)
	}

	// Get base branch reference
	baseRef, err := repo.Reference(plumbing.NewBranchReferenceName("base-branch"), true)
	require.NoError(t, err)

	// Step 2: Create branch-2 (middle-branch) off base branch's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   baseRef.Hash(),
		Create: true,
		Branch: plumbing.NewBranchReferenceName("middle-branch"),
	})
	require.NoError(t, err)

	// Add 2 commits to middle branch
	for i := 0; i < 2; i++ {
		filename := fmt.Sprintf("file-middle-branch-%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)
		_, err = wt.Add(filename)
		require.NoError(t, err)
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
			},
		})
		require.NoError(t, err)
	}

	// Get middle branch reference (before adding more commits)
	middleRef, err := repo.Reference(plumbing.NewBranchReferenceName("middle-branch"), true)
	require.NoError(t, err)

	// Step 3: Create branch-3 (top-branch) off branch-2's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   middleRef.Hash(),
		Create: true,
		Branch: plumbing.NewBranchReferenceName("top-branch"),
	})
	require.NoError(t, err)

	// Add 2 commits to top branch
	for i := 0; i < 2; i++ {
		filename := fmt.Sprintf("file-top-branch-%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)
		_, err = wt.Add(filename)
		require.NoError(t, err)
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
			},
		})
		require.NoError(t, err)
	}

	// Step 4: Go back to top branch and add another commit (creates divergence)
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("top-branch"),
	})
	require.NoError(t, err)

	// Add divergent commit to top branch
	filePath := filepath.Join(repoPath, "divergent-top.txt")
	err = os.WriteFile(filePath, []byte("divergent content"), 0644)
	require.NoError(t, err)
	_, err = wt.Add("divergent-top.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Divergent commit on top branch", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Step 5: Go back to middle branch and add another commit
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("middle-branch"),
	})
	require.NoError(t, err)

	// Add another commit to middle branch
	filePath = filepath.Join(repoPath, "another-middle.txt")
	err = os.WriteFile(filePath, []byte("another middle content"), 0644)
	require.NoError(t, err)
	_, err = wt.Add("another-middle.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Another commit on middle branch", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

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

	// Initialize config with one tower and all branches in order
	config := &Config{
		Repos: []*Repo{
			{
				Path:    repoPath,
				Current: "test-tower",
				Towers: []*Tower{
					{
						Name: "test-tower",
						Base: mainHash.String(),
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
	require.NoError(t, err)

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Run the list command
	cmd := &ListCmd{}
	output, err := CaptureOutput(func() error {
		return cmd.Run(mockCtx)
	})
	require.NoError(t, err)

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")

	// Check for divergence warning
	assert.Contains(t, output, "⚠️ This branch has diverged", "Output should contain divergence warning")

	// Verify the top branch shows divergence
	topPos := strings.Index(output, "top-branch\n")
	assert.True(t, topPos != -1, "Top branch should be in the output")
	middlePos := strings.Index(output, "middle-branch (current)\n")
	assert.True(t, middlePos != -1, "Middle branch should be in the output")
	divergencePos := strings.Index(output, "⚠️ This branch has diverged")
	assert.True(t, divergencePos != -1, "Divergence warning should be in the output")

	assert.True(t, topPos < divergencePos, "Divergence warning should appear in top branch section")
	assert.True(t, divergencePos < middlePos, "Divergence warning should appear before middle branch section")

	// Verify the divergent commit messages are in the output
	assert.Contains(t, output, "Divergent commit on top branch", "Top branch divergent commit should be shown")
	assert.Contains(t, output, "Another commit on middle branch", "Middle branch additional commit should be shown")
}

func TestListCmd_MultipleDivergences(t *testing.T) {
	// Setup test repository
	repoPath, repo := setupTestRepo(t)
	defer os.RemoveAll(repoPath)

	// Get the initial main branch reference
	headRef, err := repo.Head()
	require.NoError(t, err)
	mainHash := headRef.Hash()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Step 1: Create the base branch with 3 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Create: true,
		Branch: plumbing.NewBranchReferenceName("base-branch"),
	})
	require.NoError(t, err)

	// Add 3 commits to base branch
	for i := 0; i < 3; i++ {
		filename := fmt.Sprintf("file-base-branch-%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)
		_, err = wt.Add(filename)
		require.NoError(t, err)
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
			},
		})
		require.NoError(t, err)
	}

	// Get base branch reference
	baseRef, err := repo.Reference(plumbing.NewBranchReferenceName("base-branch"), true)
	require.NoError(t, err)

	// Step 2: Create branch-2 (middle-branch) off base branch's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   baseRef.Hash(),
		Create: true,
		Branch: plumbing.NewBranchReferenceName("middle-branch"),
	})
	require.NoError(t, err)

	// Add 2 commits to middle branch
	for i := 0; i < 2; i++ {
		filename := fmt.Sprintf("file-middle-branch-%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)
		_, err = wt.Add(filename)
		require.NoError(t, err)
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
			},
		})
		require.NoError(t, err)
	}

	// Get middle branch reference (before adding more commits)
	middleRef, err := repo.Reference(plumbing.NewBranchReferenceName("middle-branch"), true)
	require.NoError(t, err)

	// Step 3: Create branch-3 (third-branch) off branch-2's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   middleRef.Hash(),
		Create: true,
		Branch: plumbing.NewBranchReferenceName("third-branch"),
	})
	require.NoError(t, err)

	// Add 2 commits to third branch
	for i := 0; i < 2; i++ {
		filename := fmt.Sprintf("file-third-branch-%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)
		_, err = wt.Add(filename)
		require.NoError(t, err)
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
			},
		})
		require.NoError(t, err)
	}

	// Get third branch reference before creating top branch
	thirdRef, err := repo.Reference(plumbing.NewBranchReferenceName("third-branch"), true)
	require.NoError(t, err)

	// Step 4: Create branch-4 (top-branch) off branch-3's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   thirdRef.Hash(),
		Create: true,
		Branch: plumbing.NewBranchReferenceName("top-branch"),
	})
	require.NoError(t, err)

	// Add 2 commits to top branch
	for i := 0; i < 2; i++ {
		filename := fmt.Sprintf("file-top-branch-%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		err = os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644)
		require.NoError(t, err)
		_, err = wt.Add(filename)
		require.NoError(t, err)
		_, err = wt.Commit(fmt.Sprintf("Add %s", filename), &git.CommitOptions{
			Author: &object.Signature{
				Name:  "Test User",
				Email: "test@example.com",
			},
		})
		require.NoError(t, err)
	}

	// Step 5: Go back to middle-branch and add one more commit (creates first divergence)
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("middle-branch"),
	})
	require.NoError(t, err)

	// Add divergent commit to middle branch
	filePath := filepath.Join(repoPath, "divergent-middle.txt")
	err = os.WriteFile(filePath, []byte("divergent middle content"), 0644)
	require.NoError(t, err)
	_, err = wt.Add("divergent-middle.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Divergent commit on middle branch", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Step 6: Go back to top-branch and add another commit (creates second divergence)
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("top-branch"),
	})
	require.NoError(t, err)

	// Add divergent commit to top branch
	filePath = filepath.Join(repoPath, "divergent-top.txt")
	err = os.WriteFile(filePath, []byte("divergent top content"), 0644)
	require.NoError(t, err)
	_, err = wt.Add("divergent-top.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Divergent commit on top branch", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	// Step 7: Go back to third-branch and add another commit (completes second divergence)
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("third-branch"),
	})
	require.NoError(t, err)

	// Add another commit to third branch
	filePath = filepath.Join(repoPath, "another-third.txt")
	err = os.WriteFile(filePath, []byte("another third content"), 0644)
	require.NoError(t, err)
	_, err = wt.Add("another-third.txt")
	require.NoError(t, err)
	_, err = wt.Commit("Another commit on third branch", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

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

	// Initialize config with one tower and all branches in order
	config := &Config{
		Repos: []*Repo{
			{
				Path:    repoPath,
				Current: "test-tower",
				Towers: []*Tower{
					{
						Name: "test-tower",
						Base: mainHash.String(),
						Branches: []Branch{
							{Name: "base-branch"},
							{Name: "middle-branch"},
							{Name: "third-branch"},
							{Name: "top-branch"},
						},
					},
				},
			},
		},
	}
	err = SaveConfig(config)
	require.NoError(t, err)

	// Create a mock context for running commands
	mockCtx := &kong.Context{}

	// Run the list command
	cmd := &ListCmd{}
	output, err := CaptureOutput(func() error {
		return cmd.Run(mockCtx)
	})
	require.NoError(t, err)

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")

	// There should be two divergence warnings
	divergencesCount := strings.Count(output, "⚠️ This branch has diverged")
	assert.Equal(t, 2, divergencesCount, "Output should contain two divergence warnings")

	// Verify both divergent commits are highlighted
	assert.Contains(t, output, "Divergent commit on middle branch", "Middle branch divergent commit should be shown")
	assert.Contains(t, output, "Divergent commit on top branch", "Top branch divergent commit should be shown")

	// Verify branch order and placement of warnings
	topPos := strings.Index(output, "top-branch\n")
	assert.True(t, topPos != -1, "Top branch should be in the output")
	thirdPos := strings.Index(output, "third-branch (current)\n")
	assert.True(t, thirdPos != -1, "Third branch should be in the output")
	middlePos := strings.Index(output, "middle-branch\n")
	assert.True(t, middlePos != -1, "Middle branch should be in the output")
	basePos := strings.Index(output, "base-branch\n")
	assert.True(t, basePos != -1, "Base branch should be in the output")

	// Find positions of divergence warnings
	firstWarningPos := strings.Index(output, "⚠️ This branch has diverged")
	secondWarningPos := strings.Index(output[firstWarningPos+1:], "⚠️ This branch has diverged") + firstWarningPos + 1

	// Verify warnings appear in the right sections
	assert.True(t, topPos < firstWarningPos && firstWarningPos < thirdPos,
		"First divergence warning should be in top branch section")
	assert.True(t, thirdPos < secondWarningPos && secondWarningPos < middlePos,
		"Second divergence warning should be in middle branch section")
}
