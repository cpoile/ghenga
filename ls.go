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
	statusColor := color.New(color.FgMagenta)
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
			baseCommitColor.Printf(" [base: %s]", tower.Base)
		} else {
			warningColor.Printf(" ⚠️ warning: base not set")
		}

		fmt.Println()

		// TODO: need to rename "base branch" to something else, confusing with "base"
		// Check if the tower base branch exists
		if len(tower.Branches) > 0 {
			bottomBranch := tower.Branches[0].Name
			bottomBranchRefName := plumbing.NewBranchReferenceName(bottomBranch)
			// TODO: need to check remote, not local
			_, err := r.Reference(bottomBranchRefName, true)
			if err != nil && errors.Is(err, plumbing.ErrReferenceNotFound) {
				warningColor.Println("  ⚠️ Warning: The bottom branch is no longer valid -- if it has been merged upstream,")
				warningColor.Println("           you need to run 'ghenga land' to mark the bottom branch as merged.")
				warningColor.Println("           The rest of the tower will then be rebased.")
			}
		}

		// Track divergence points for highlighting
		divergencePoints := make(map[string]bool)

		// Determine the actual base commit hash based on merge-base if applicable
		var calculatedBaseCommit plumbing.Hash
		baseCalculationFailed := false
		if tower.Base != "" && len(tower.Branches) > 0 {
			// Resolve the configured base branch
			baseBranchRef, errBase := r.ResolveRevision(plumbing.Revision(tower.Base))
			if errBase != nil {
				warningColor.Printf("  ⚠️ Could not resolve base branch '%s': %v\n", tower.Base, errBase)
				baseCalculationFailed = true
			} else {
				// Resolve the first branch of the tower
				firstTowerBranch := tower.Branches[0]
				firstTowerBranchRef, errFirst := r.Reference(plumbing.NewBranchReferenceName(firstTowerBranch.Name), true)
				if errFirst != nil {
					warningColor.Printf("  ⚠️ Could not resolve first tower branch '%s': %v\n", firstTowerBranch.Name, errFirst)
					baseCalculationFailed = true
				} else {
					// Find the merge base
					mergeBaseHash, errMerge := findMergeBase(r, firstTowerBranchRef.Hash(), *baseBranchRef)
					if errMerge != nil {
						warningColor.Printf("  ⚠️ Could not find merge base between '%s' and '%s': %v\n", firstTowerBranch.Name, tower.Base, errMerge)
						baseCalculationFailed = true
					} else {
						calculatedBaseCommit = mergeBaseHash
					}
				}
			}
		}

		// Store branch hashes for limiting commit display
		branchHashes := make(map[int]plumbing.Hash)

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
			branchRef, err := r.Reference(branchRefName, true)
			if err != nil && errors.Is(err, plumbing.ErrReferenceNotFound) {
				// Print branch name even if not found locally, but mark it
				branchColor.Printf("  %s", branch.Name)
				warningColor.Println(" (local branch not found!) ")
				continue // Skip commit listing and status check for non-existent local branches
			} else if err != nil {
				// Handle other errors getting local ref
				branchColor.Printf("  %s", branch.Name)
				warningColor.Printf(" (error: %v)\n", err)
				continue
			}

			// Print branch name
			if headRef.Name().String() == branchRefName.String() {
				currentBranch.Printf("  %s (current)", branch.Name)
			} else {
				branchColor.Printf("  %s", branch.Name)
			}

			// Check push status (automatically determines correct push remote)
			pushStatus, _, _, statusErr := GetBranchPushStatus(r, "", branch.Name)
			statusString := ""
			if statusErr != nil {
				statusString = " (status check failed)"
			} else {
				switch pushStatus {
				case LocalAhead:
					statusString = " (local ahead, run: ghenga sync)"
				case RemoteAhead:
					statusString = " (remote ahead, run: ghenga sync)"
				case Diverged:
					statusString = " (diverged from remote, run: ghenga sync)"
				case NoRemote:
					statusString = " (remote does not exist)"
					// UpToDate and StatusError (handled above) don't need explicit messages
				}
			}
			if statusString != "" {
				statusColor.Printf(statusString)
			}
			fmt.Println()

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
			commitIter, err := r.Log(&git.LogOptions{
				From:  branchRef.Hash(),
				Order: git.LogOrderCommitterTime,
			})
			if err != nil {
				continue
			}

			var commits []*object.Commit
			count := 0
			commitIter.ForEach(func(c *object.Commit) error {
				// For the base branch (i==0), stop at the calculated merge base
				if i == 0 && !calculatedBaseCommit.IsZero() && c.Hash == calculatedBaseCommit {
					commits = append(commits, c) // Include the merge-base commit itself
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
					divergedColor.Printf("    ⚠️ This branch has diverged ↓↓ here ↓↓ from the branch below. Run 'ghenga rebase' to fix.\n")
					branchColor.Printf("        If the branch below has been rebased onto its merge-base, you can run\n")
					branchColor.Printf("        'ghenga rebase from %s [commit-hash]' to rebase this branch onto the branch below.\n", branch.Name)
					branchColor.Printf("        Use the commit hash from %s that is the first unique commit after the branch below.\n", branch.Name)
					message := strings.Split(commit.Message, "\n")[0]
					divergedColor.Printf("    %s %s\n", commit.Hash.String()[:7], message)
					break
				}

				message := strings.Split(commit.Message, "\n")[0]

				// Check if this is the calculated base commit or a divergence point
				if i == 0 && !calculatedBaseCommit.IsZero() && commit.Hash == calculatedBaseCommit {
					baseCommitColor.Printf("    %s %s (merge-base with %s)\n", commit.Hash.String()[:7], message, tower.Base)
				} else if divergencePoints[commit.Hash.String()] {
					// TODO: test this
					divergedColor.Printf("    %s %s\n", commit.Hash.String()[:7], message)
				} else {
					fmt.Printf("    %s %s\n", commit.Hash.String()[:7], message)
				}
			}

			// If base calculation failed, print a warning after the commits for the base branch
			if i == 0 && baseCalculationFailed {
				warningColor.Println("    ⚠️ Could not determine the precise base commit history due to errors above.")
			}
		}
		fmt.Println()
	}

	return nil
}
