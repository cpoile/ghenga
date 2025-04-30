package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/fatih/color"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
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

// setupTestEnv handles common test setup:
// - Creates a temporary repo.
// - Changes CWD to the repo path.
// - Creates a temporary config file.
// - Mocks the global ConfigPath function.
// Returns the repo path, repo object, and a cleanup function.
func setupTestEnv(t *testing.T) (string, *git.Repository, func()) {
	t.Helper()

	repoPath, repo := setupTestRepo(t)

	oldWd, err := os.Getwd()
	require.NoError(t, err)
	err = os.Chdir(repoPath)
	require.NoError(t, err)

	configFile, err := os.CreateTemp("", "ghenga-config-*.toml")
	require.NoError(t, err)
	configFilePath := configFile.Name()
	// Close the file immediately as we only need its path
	require.NoError(t, configFile.Close())

	oldConfigPath := ConfigPath
	ConfigPath = mockedConfigPath(configFilePath)

	cleanup := func() {
		ConfigPath = oldConfigPath
		os.Remove(configFilePath)
		os.Chdir(oldWd)
		os.RemoveAll(repoPath)
	}

	return repoPath, repo, cleanup
}

// mockedConfigPath creates a function that returns a temporary config path
func mockedConfigPath(tempPath string) ConfigPathFunc {
	return func() (string, error) {
		return tempPath, nil
	}
}

// CaptureOutput captures stdout during function execution
func CaptureOutput(f func() error) (string, error) {
	// Save the original stdout and color.Output
	oldStdout := os.Stdout
	oldColorOutput := color.Output

	// Create a pipe
	r, w, _ := os.Pipe()

	// Redirect both stdout and color.Output to the pipe
	os.Stdout = w
	color.Output = w

	// Run the function
	err := f()

	// Flush all writes before closing
	os.Stdout.Sync()

	// Close the writer to get all output
	w.Close()

	// Restore original stdout and color.Output
	os.Stdout = oldStdout
	color.Output = oldColorOutput

	// Read the output
	var buf bytes.Buffer
	io.Copy(&buf, r)

	return buf.String(), err
}

// runLsCommandWithConfig saves the provided config, runs the LsCmd, and returns the captured output.
func runLsCommandWithConfig(t *testing.T, config *Config, cmd *LsCmd) string {
	t.Helper()

	err := SaveConfig(config)
	require.NoError(t, err, "Failed to save config")

	// Create a mock context for running commands (needed for some Run() calls)
	// It's okay if it's not always used.
	// TODO: Investigate if a nil context is always sufficient or if we need a more sophisticated mock.
	mockCtx := &kong.Context{}

	output, err := CaptureOutput(func() error {
		return cmd.Run(mockCtx)
	})
	require.NoError(t, err, "LsCmd.Run failed")

	return output
}

// createTestConfig creates a standard Config object for tests with a single repo.
// Optionally sets the Base commit on the first tower.
func createTestConfig(t *testing.T, repoPath string, currentTower string, towers []*Tower, baseCommit string) *Config {
	t.Helper()
	// Set base on the first tower if provided and towers exist
	if baseCommit != "" && len(towers) > 0 {
		towers[0].Base = baseCommit
	}
	return &Config{
		Repos: []*Repo{
			{
				Path:    repoPath,
				Current: currentTower,
				Towers:  towers,
			},
		},
	}
}
