package main

import (
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	kongcompletion "github.com/jotaen/kong-completion"
	"github.com/posener/complete"
)

var predictTowers = kongcompletion.WithPredictor(
	"predictTowers",
	TowerLister{},
)

type TowerLister struct{}

func (l TowerLister) Predict(args complete.Args) []string {
	config, err := LoadConfig()
	if err != nil {
		return nil
	}

	repoPath, err := GetCurrentRepository()
	if err != nil {
		return nil
	}

	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return nil
	}

	towers := make([]string, 0)
	for _, tower := range repo.Towers {
		towers = append(towers, tower.Name)
	}

	return towers
}

var predictBranches = kongcompletion.WithPredictor(
	"predictBranches",
	BranchLister{},
)

var predictTowerBranches = kongcompletion.WithPredictor(
	"predictTowerBranches",
	TowerBranchLister{},
)

type TowerBranchLister struct{}

func (l TowerBranchLister) Predict(args complete.Args) []string {
	// Get current repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return nil
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return nil
	}

	// Find repo in config
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return nil
	}

	// Get current tower
	var currentTower *Tower
	if repo.Current != "" {
		currentTower = FindTowerByName(repo, repo.Current)
	}

	// If no current tower, return empty list
	if currentTower == nil {
		return nil
	}

	// Get all branch names from the current tower
	branches := make([]string, 0, len(currentTower.Branches))
	for _, branch := range currentTower.Branches {
		branches = append(branches, branch.Name)
	}

	return branches
}

type BranchLister struct{}

func (l BranchLister) Predict(args complete.Args) []string {
	// Check if git is installed
	_, err := exec.LookPath("git")
	if err != nil {
		// Fall back to unsorted branch listing
		return fallbackBranchListing()
	}

	// Use Git's native sort by committer date for local branches
	cmd := exec.Command("git", "for-each-ref", "--sort=-committerdate", "refs/heads/", "--format=%(refname:short)")
	output, err := cmd.Output()
	if err != nil {
		// Fall back to unsorted branch listing
		return fallbackBranchListing()
	}

	// Split output into lines and filter out empty lines
	branches := []string{}
	for _, branch := range strings.Split(string(output), "\n") {
		if branch != "" {
			branches = append(branches, branch)
		}
	}

	return branches
}

// fallbackBranchListing provides branch listing without sorting if git command fails
func fallbackBranchListing() []string {
	// Open the repository
	r, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return nil
	}

	// Get references
	refs, err := r.References()
	if err != nil {
		return nil
	}

	branches := make([]string, 0)
	refs.ForEach(func(ref *plumbing.Reference) error {
		// Only include local branches
		if ref.Name().IsBranch() {
			branches = append(branches, ref.Name().Short())
		}
		return nil
	})

	return branches
}

var predictGitRefs = kongcompletion.WithPredictor(
	"predictGitRefs",
	GitRefLister{},
)

type GitRefLister struct{}

func (l GitRefLister) Predict(args complete.Args) []string {
	// Open the repository
	r, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return nil
	}

	gitRefs := []string{}

	// Get current repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return gitRefs
	}

	// Load configuration to find current tower
	config, err := LoadConfig()
	if err != nil {
		return gitRefs
	}

	// Find repo in config
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return gitRefs
	}

	// Get current tower
	var currentTower *Tower
	if repo.Current != "" {
		currentTower = FindTowerByName(repo, repo.Current)
	}

	// If no current tower or tower has no branches, try to use current HEAD
	if currentTower == nil || len(currentTower.Branches) == 0 {
		// Get the current HEAD reference as fallback
		headRef, err := r.Head()
		if err != nil {
			return gitRefs
		}

		// Add the current branch name if it's a branch
		if headRef.Name().IsBranch() {
			gitRefs = append(gitRefs, headRef.Name().Short())
		}

		// Get commits from log, ordered by recency
		logIter, err := r.Log(&git.LogOptions{
			From:  headRef.Hash(),
			Order: git.LogOrderCommitterTime,
		})
		if err != nil {
			return gitRefs
		}

		seenCommits := make(map[string]bool)
		commitCount := 0

		// Process commits
		logIter.ForEach(func(c *object.Commit) error {
			if commitCount >= 50 {
				return plumbing.ErrObjectNotFound // Stop after 50 items
			}

			hash := c.Hash.String()
			shortHash := hash[:7]

			// Add the commit hash if we haven't seen it yet
			if !seenCommits[shortHash] {
				gitRefs = append(gitRefs, shortHash)
				seenCommits[shortHash] = true
				commitCount++
			}

			return nil
		})

		return gitRefs
	}

	// Use the first branch in the tower
	firstBranchName := currentTower.Branches[0].Name
	gitRefs = append(gitRefs, firstBranchName)

	// Get the branch reference
	branchRef, err := r.Reference(plumbing.NewBranchReferenceName(firstBranchName), true)
	if err != nil {
		return gitRefs
	}

	// Get commits from log, starting from the first branch, ordered by recency
	logIter, err := r.Log(&git.LogOptions{
		From:  branchRef.Hash(),
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return gitRefs
	}

	seenCommits := make(map[string]bool)
	commitCount := 0

	// Process commits
	logIter.ForEach(func(c *object.Commit) error {
		if commitCount >= 50 {
			return plumbing.ErrObjectNotFound // Stop after 50 items
		}

		hash := c.Hash.String()
		shortHash := hash[:7]

		// Add the commit hash if we haven't seen it yet
		if !seenCommits[shortHash] {
			gitRefs = append(gitRefs, shortHash)
			seenCommits[shortHash] = true
			commitCount++
		}

		return nil
	})

	return gitRefs
}
