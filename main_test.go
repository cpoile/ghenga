package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitCmd(t *testing.T) {
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
	emptyConfig := &Config{
		Repos: []*RepoInfo{},
	}
	err = SaveConfig(emptyConfig)
	require.NoError(t, err)

	// Run the init command
	cmd := &InitCmd{DefaultTower: "test-tower"}
	err = cmd.Run(nil)
	require.NoError(t, err)

	// Load the updated config
	config, err := LoadConfig()
	require.NoError(t, err)

	// Verify that the repository was added to the config
	repo := findRepoByPath(config, repoPath)
	require.NotNil(t, repo, "Repository should exist in config")

	// Verify that the tower was created
	tower := findTowerByName(repo, "test-tower")
	require.NotNil(t, tower, "Tower 'test-tower' should exist")

	// Test idempotence - running init again should not create duplicate entries
	err = cmd.Run(nil)
	require.NoError(t, err)

	// Load config again
	config, err = LoadConfig()
	require.NoError(t, err)

	// Should still have just one repo
	require.Equal(t, 1, len(config.Repos), "Should have exactly one repository")

	// Repo should have just one tower
	repo = findRepoByPath(config, repoPath)
	require.Equal(t, 1, len(repo.Towers), "Repository should have exactly one tower")
}
