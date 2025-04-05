package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigCreateAndRead(t *testing.T) {
	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "ghenga-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Override the config path for testing
	originalConfigPath := ConfigPath
	ConfigPath = func() (string, error) {
		return filepath.Join(tempDir, "config.toml"), nil
	}
	defer func() { ConfigPath = originalConfigPath }()

	// Create test configuration
	testConfig := &Config{
		Repos: []*Repo{
			{
				Path: "/Users/test/projects/project1",
				Towers: []*Tower{
					{
						Name: "feature-a",
						Branches: []Branch{
							{Name: "feature-a-base"},
							{Name: "feature-a-ui"},
							{Name: "feature-a-api"},
						},
					},
					{
						Name: "bugfix-b",
						Branches: []Branch{
							{Name: "bugfix-b-base"},
							{Name: "bugfix-b-fix"},
						},
					},
				},
			},
			{
				Path: "/Users/test/projects/project2",
				Towers: []*Tower{
					{
						Name: "refactor-x",
						Branches: []Branch{
							{Name: "refactor-x-base"},
							{Name: "refactor-x-phase1"},
							{Name: "refactor-x-phase2"},
						},
					},
				},
			},
		},
	}

	// Save the test configuration
	err = SaveConfig(testConfig)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Load the configuration back
	loadedConfig, err := LoadConfig()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify the loaded configuration
	if len(loadedConfig.Repos) != 2 {
		t.Errorf("Expected 2 repos, got %d", len(loadedConfig.Repos))
	}

	// Verify first repo
	repo1 := loadedConfig.Repos[0]
	if repo1.Path != "/Users/test/projects/project1" {
		t.Errorf("Expected repo path %s, got %s", "/Users/test/projects/project1", repo1.Path)
	}
	if len(repo1.Towers) != 2 {
		t.Errorf("Expected 2 towers in repo1, got %d", len(repo1.Towers))
	}

	// Verify first tower in first repo
	tower1 := repo1.Towers[0]
	if tower1.Name != "feature-a" {
		t.Errorf("Expected tower name %s, got %s", "feature-a", tower1.Name)
	}
	if len(tower1.Branches) != 3 {
		t.Errorf("Expected 3 branches in tower1, got %d", len(tower1.Branches))
	}
	if tower1.Branches[0].Name != "feature-a-base" {
		t.Errorf("Expected branch name %s, got %s", "feature-a-base", tower1.Branches[0].Name)
	}

	// Verify second repo
	repo2 := loadedConfig.Repos[1]
	if repo2.Path != "/Users/test/projects/project2" {
		t.Errorf("Expected repo path %s, got %s", "/Users/test/projects/project2", repo2.Path)
	}
	if len(repo2.Towers) != 1 {
		t.Errorf("Expected 1 tower in repo2, got %d", len(repo2.Towers))
	}

	// Verify tower in second repo
	tower2 := repo2.Towers[0]
	if tower2.Name != "refactor-x" {
		t.Errorf("Expected tower name %s, got %s", "refactor-x", tower2.Name)
	}
	if len(tower2.Branches) != 3 {
		t.Errorf("Expected 3 branches in tower2, got %d", len(tower2.Branches))
	}
}
