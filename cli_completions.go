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

const MAX_COMMITS_TO_PREDICT = 50

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

	repoPath, err := getCurrentRepository()
	if err != nil {
		return nil
	}

	repo := findRepoByPath(config, repoPath)
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
	repoPath, err := getCurrentRepository()
	if err != nil {
		return nil
	}

	config, err := LoadConfig()
	if err != nil {
		return nil
	}

	repo := findRepoByPath(config, repoPath)
	if repo == nil {
		return nil
	}

	var currentTower *Tower
	if repo.Current != "" {
		currentTower = findTowerByName(repo, repo.Current)
	}

	if currentTower == nil {
		return nil
	}

	branches := make([]string, 0, len(currentTower.Branches))
	for _, branch := range currentTower.Branches {
		branches = append(branches, branch.Name)
	}

	return branches
}

type BranchLister struct{}

func (l BranchLister) Predict(args complete.Args) []string {
	_, err := exec.LookPath("git")
	if err != nil {
		return fallbackBranchListing()
	}

	// Use Git's native sort by committer date for local branches
	cmd := exec.Command("git", "for-each-ref", "--sort=-committerdate", "refs/heads/", "--format=%(refname:short)")
	output, err := cmd.Output()
	if err != nil {
		return fallbackBranchListing()
	}

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
	r, err := openGitRepo()
	if err != nil {
		return nil
	}

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

// PredictGitRefs predicts Git references for the current repository.
func (l GitRefLister) Predict(args complete.Args) []string {
	_, _, currentTower, _, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return nil
	}

	r, err := openGitRepo()
	if err != nil {
		return nil
	}

	gitRefs := []string{}

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

		logIter.ForEach(func(c *object.Commit) error {
			if commitCount >= MAX_COMMITS_TO_PREDICT {
				return plumbing.ErrObjectNotFound // Stop after 50 items
			}

			hash := c.Hash.String()
			shortHash := hash[:7]

			if !seenCommits[shortHash] {
				gitRefs = append(gitRefs, shortHash)
				seenCommits[shortHash] = true
				commitCount++
			}

			return nil
		})

		return gitRefs
	}

	firstBranchName := currentTower.Branches[0].Name
	gitRefs = append(gitRefs, firstBranchName)

	firstBranchRef, err := r.Reference(plumbing.NewBranchReferenceName(firstBranchName), true)
	if err != nil {
		return gitRefs
	}

	logIter, err := r.Log(&git.LogOptions{
		From:  firstBranchRef.Hash(),
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return gitRefs
	}

	seenCommits := make(map[string]bool)
	commitCount := 0

	logIter.ForEach(func(c *object.Commit) error {
		if commitCount >= MAX_COMMITS_TO_PREDICT {
			return plumbing.ErrObjectNotFound // Stop after 50 items
		}

		hash := c.Hash.String()
		shortHash := hash[:7]

		if !seenCommits[shortHash] {
			gitRefs = append(gitRefs, shortHash)
			seenCommits[shortHash] = true
			commitCount++
		}

		return nil
	})

	return gitRefs
}
