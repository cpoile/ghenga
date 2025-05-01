package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/fatih/color"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type LsCmd struct {
	TowerName string `arg:"" optional:"" help:"Name of the tower to list. If not provided, all towers will be listed." predictor:"predictTowers"`
}

func (l *LsCmd) Run(_ *kong.Context) error {
	_, repo, _, err := loadRepoConfig()
	if err != nil {
		// Handle the specific error where the repo is not found but config loaded
		if repo == nil && strings.Contains(err.Error(), "not found in configuration") {
			parts := strings.SplitN(err.Error(), "'", 3)
			if len(parts) == 3 {
				return fmt.Errorf("repository at '%s' not found in configuration", parts[1])
			}
			return err
		}
		return err
	}

	r, err := openGitRepo()
	if err != nil {
		return err
	}

	headRef, err := r.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}

	currentBranch := color.New(color.FgGreen).Add(color.Bold)
	towerColor := color.New(color.FgBlue).Add(color.Bold)
	branchColor := color.New(color.FgYellow)
	commitColor := color.New(color.FgWhite)
	baseCommitColor := color.New(color.FgCyan)
	divergedColor := color.New(color.FgRed).Add(color.Bold)
	warningColor := color.New(color.FgYellow).Add(color.Bold)

	// If a tower name is provided, use that tower, otherwise use all towers
	var towers []*Tower
	if l.TowerName != "" {
		tower := findTowerByName(repo, l.TowerName)
		if tower == nil {
			return fmt.Errorf("tower '%s' not found", l.TowerName)
		}
		towers = []*Tower{tower}
	} else {
		towers = repo.Towers
	}

	for _, tower := range towers {
		towerColor.Printf("Tower: %s", tower.Name)

		if repo.Current == tower.Name {
			currentBranch.Printf(" (current)")
		}

		if tower.Base != "" {
			baseCommitColor.Printf(" [base: %s]", tower.Base[:7])
		}

		fmt.Println()

		// Check if the tower base branch exists
		if len(tower.Branches) > 0 {
			baseBranchName := tower.Branches[0].Name
			baseBranchRefName := plumbing.NewBranchReferenceName(baseBranchName)
			_, err := r.Reference(baseBranchRefName, true)
			if err != nil && errors.Is(err, plumbing.ErrReferenceNotFound) {
				warningColor.Println("  ⚠️ Warning: The base branch is no longer valid -- if it has been merged,")
				warningColor.Println("           you need to run 'ghenga land' to mark the base branch as merged.")
				warningColor.Println("           The rest of the tower will then be rebased.")
			}
		}

		// Store branch hashes for limiting commit display
		branchHashes := make(map[int]plumbing.Hash)

		// Track divergence points for highlighting
		divergencePoints := make(map[string]bool)

		// First pass: collect branch hashes
		for i, branch := range tower.Branches {
			branchRefName := plumbing.NewBranchReferenceName(branch.Name)
			branchRef, err := r.Reference(branchRefName, true)
			if err == nil {
				branchHashes[i] = branchRef.Hash()
			}
		}

		// Display most recent branches first (like git log)
		for i := len(tower.Branches) - 1; i >= 0; i-- {
			branch := tower.Branches[i]

			branchRefName := plumbing.NewBranchReferenceName(branch.Name)
			if headRef.Name().String() == branchRefName.String() {
				currentBranch.Printf("  %s (current)\n", branch.Name)
			} else {
				branchColor.Printf("  %s\n", branch.Name)
			}

			branchRef, err := r.Reference(branchRefName, true)
			if err != nil && errors.Is(err, plumbing.ErrReferenceNotFound) {
				warningColor.Println("  ⚠️ Warning: ^^^ Branch not found ^^^")
				continue
			}

			// Determine if we need to find the stop commit (branch below)
			var stopAtCommit plumbing.Hash
			var foundStopCommit bool
			var hasDiverged bool
			var divergencePoint plumbing.Hash

			// If this is not the first branch (base branch), use the branch below as stop point
			if i > 0 {
				if lowerHash, exists := branchHashes[i-1]; exists {
					stopAtCommit = lowerHash
					foundStopCommit = true

					// Find the merge base (common ancestor) between this branch and the one below
					mergeBase, err := findMergeBase(r, branchRef.Hash(), lowerHash)
					if err == nil {
						// If the merge base is not the head of the lower branch, they've diverged
						hasDiverged = mergeBase != lowerHash
						if hasDiverged {
							divergencePoint = mergeBase
							// Store the divergence point for highlighting in all branches
							divergencePoints[divergencePoint.String()] = true
						}
					}
				}
			}

			// Display commits
			commitIter, err := r.Log(&git.LogOptions{From: branchRef.Hash()})
			if err != nil {
				continue
			}

			var commits []*object.Commit
			count := 0
			commitIter.ForEach(func(c *object.Commit) error {
				// For the base branch (i==0), stop at the tower base commit
				if i == 0 && tower.Base != "" && c.Hash.String() == tower.Base {
					commits = append(commits, c)
					return fmt.Errorf("stop")
				}

				// For non-base branches, stop at the lower branch's commit
				if foundStopCommit && c.Hash == stopAtCommit {
					return fmt.Errorf("stop")
				}

				if count < MAX_COMMITS_PER_BRANCH_TO_DISPLAY {
					commits = append(commits, c)
					count++
					return nil
				}
				return fmt.Errorf("stop")
			})

			for j := range commits {
				commit := commits[j]

				if hasDiverged && commit.Hash == divergencePoint {
					divergedColor.Printf("    ⚠️ This branch has diverged ↓↓ here ↓↓ from the branch below\n")
					message := strings.Split(commit.Message, "\n")[0]
					divergedColor.Printf("    %s %s\n", commit.Hash.String()[:7], message)
					break
				}

				message := strings.Split(commit.Message, "\n")[0]

				// Check if this is the base commit or a divergence point
				if i == 0 && tower.Base != "" && commit.Hash.String() == tower.Base {
					baseCommitColor.Printf("    %s %s (base)\n", commit.Hash.String()[:7], message)
				} else if divergencePoints[commit.Hash.String()] {
					// TODO: test this
					divergedColor.Printf("    %s %s\n", commit.Hash.String()[:7], message)
				} else {
					commitColor.Printf("    %s %s\n", commit.Hash.String()[:7], message)
				}
			}
		}
		fmt.Println()
	}

	return nil
}
