package main

import (
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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

type BranchLister struct{}

func (l BranchLister) Predict(args complete.Args) []string {
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
