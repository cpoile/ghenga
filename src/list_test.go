package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestRepo creates a temporary git repository for testing
func setupTestRepo(t *testing.T) (string, *git.Repository) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "ghenga-test-*")
	require.NoError(t, err)

	// Initialize a git repository
	repo, err := git.PlainInit(tempDir, false)
	require.NoError(t, err)

	// Create initial commit
	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create a README file
	readmePath := filepath.Join(tempDir, "README.md")
	err = os.WriteFile(readmePath, []byte("# Test Repository\n"), 0644)
	require.NoError(t, err)

	// Add and commit
	_, err = wt.Add("README.md")
	require.NoError(t, err)

	_, err = wt.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
		},
	})
	require.NoError(t, err)

	return tempDir, repo
}

// createTestBranch creates a branch and adds some commits to it
func createTestBranch(t *testing.T, repo *git.Repository, branchName string, commitCount int) {
	// Get the HEAD reference for the main branch
	headRef, err := repo.Head()
	require.NoError(t, err)

	// Create a new branch
	branchRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), headRef.Hash())
	err = repo.Storer.SetReference(branchRef)
	require.NoError(t, err)

	// Checkout the new branch
	wt, err := repo.Worktree()
	require.NoError(t, err)

	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branchName),
	})
	require.NoError(t, err)

	// Add commits to the branch
	for i := 0; i < commitCount; i++ {
		filename := fmt.Sprintf("file-%s-%d.txt", branchName, i)
		filePath := filepath.Join(wt.Filesystem.Root(), filename)

		err = os.WriteFile(filePath, []byte(fmt.Sprintf("Content for %s\n", filename)), 0644)
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
}

// mockedConfigPath creates a function that returns a temporary config path
func mockedConfigPath(tempPath string) ConfigPathFunc {
	return func() (string, error) {
		return tempPath, nil
	}
}

// CaptureOutput captures stdout during function execution
func CaptureOutput(f func() error) (string, error) {

	// Create a pipe
	r, w, _ := os.Pipe()

	// The color package writes to os.Stdout by default, so we need to redirect it to the pipe
	color.Output = w

	// Run the function
	err := f()

	// Flush all writes before closing
	os.Stdout.Sync()

	// Close the writer to get all output
	w.Close()

	// Restore color's default output
	color.Output = os.Stdout

	// Read the output
	var buf bytes.Buffer
	io.Copy(&buf, r)

	return buf.String(), err
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
