package main

import (
	"fmt"
	"path/filepath"

	"github.com/go-git/go-git/v5"
)

// GetCurrentRepository returns the path of the current git repository
func GetCurrentRepository() (string, error) {
	// Open the repository
	r, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	// Get the repository path
	wt, err := r.Worktree()
	if err != nil {
		return "", fmt.Errorf("failed to get worktree: %w", err)
	}

	return wt.Filesystem.Root(), nil
}

// FindTowerByName finds a tower by name in the given repository configuration
func FindTowerByName(repo *Repo, towerName string) *Tower {
	for _, tower := range repo.Towers {
		if tower.Name == towerName {
			return tower
		}
	}
	return nil
}

// FindOrCreateTower finds a tower by name or creates it if it doesn't exist
func FindOrCreateTower(repo *Repo, towerName string) *Tower {
	tower := FindTowerByName(repo, towerName)
	if tower == nil {
		tower = &Tower{
			Name:     towerName,
			Branches: []Branch{},
		}
		repo.Towers = append(repo.Towers, tower)
	}
	return tower
}

// FindRepoByPath finds a repository configuration by path
func FindRepoByPath(config *Config, repoPath string) *Repo {
	// Try to find the repo by exact path
	for _, repo := range config.Repos {
		if repo.Path == repoPath {
			return repo
		}
	}

	// Try to find the repo by normalized path (resolving symlinks)
	normalizedPath, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		return nil
	}

	for _, repo := range config.Repos {
		normalizedRepoPath, err := filepath.EvalSymlinks(repo.Path)
		if err != nil {
			continue
		}
		if normalizedRepoPath == normalizedPath {
			return repo
		}
	}

	return nil
}

// FindOrCreateRepo finds a repository configuration by path or creates it if it doesn't exist
func FindOrCreateRepo(config *Config, repoPath string) *Repo {
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		repo = &Repo{
			Path:   repoPath,
			Towers: []*Tower{},
		}
		config.Repos = append(config.Repos, repo)
	}
	return repo
}

// ContainsBranch checks if a tower contains a branch with the given name
func ContainsBranch(tower *Tower, branchName string) bool {
	for _, branch := range tower.Branches {
		if branch.Name == branchName {
			return true
		}
	}
	return false
}

// SetCurrentTower sets the current tower for a repository
func SetCurrentTower(repo *Repo, towerName string) error {
	// Check if the tower exists
	tower := FindTowerByName(repo, towerName)
	if tower == nil {
		return fmt.Errorf("tower '%s' not found", towerName)
	}

	// Set the current tower
	repo.Current = towerName
	return nil
}

// GetCurrentTower gets the current tower for a repository
// If no current tower is set, returns the first tower or nil if no towers exist
func GetCurrentTower(repo *Repo) *Tower {
	if repo.Current != "" {
		tower := FindTowerByName(repo, repo.Current)
		if tower != nil {
			return tower
		}
	}

	// Fallback to first tower if available
	if len(repo.Towers) > 0 {
		return repo.Towers[0]
	}

	return nil
}
