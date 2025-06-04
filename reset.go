package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type ResetCmd struct {
	Do   ResetDoCmd   `cmd:"" default:"1" hidden:"" help:"Reset the entire tower onto a new base"`
	Undo ResetUndoCmd `cmd:"undo" help:"Undo the last reset operation for the current tower"`
}

// ResetDoCmd implements the main reset command that rebases the entire tower onto a new base
type ResetDoCmd struct {
	NewBase string `help:"Branch or commit to reset the tower to" predictor:"predictGitRefs"`
}

// ResetUndoCmd implements the reset undo command
type ResetUndoCmd struct {
}

// Run executes the reset do command
func (r *ResetDoCmd) Run(_ *kong.Context) error {
	// Load current tower configuration
	config, _, currentTower, _, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	// Validate inputs
	if r.NewBase == "" {
		return fmt.Errorf("new base argument is required")
	}

	// Open git repository
	gitRepo, err := openGitRepo()
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Check if working directory is clean
	if err := isWorkingDirectoryClean(gitRepo); err != nil {
		return fmt.Errorf("working directory is not clean. Please commit or stash your changes before resetting tower")
	}

	// Validate that new base exists
	_, err = gitRepo.ResolveRevision(plumbing.Revision(r.NewBase))
	if err != nil {
		return fmt.Errorf("new base '%s' does not exist: %w", r.NewBase, err)
	}

	// Check if tower has branches
	if len(currentTower.Branches) == 0 {
		return fmt.Errorf("tower '%s' has no branches to reset", currentTower.Name)
	}

	// Update the tower's base to the new base
	fmt.Printf("Resetting tower '%s' to new base '%s'...\n", currentTower.Name, r.NewBase)

	// Save pre-reset state for undo
	fmt.Println("Saving pre-reset state for undo...")
	currentTime := time.Now().Format(time.RFC3339)
	currentTower.LastRebased = currentTime

	for i := range currentTower.Branches {
		branch := &currentTower.Branches[i]
		branchRefName := plumbing.NewBranchReferenceName(branch.Name)
		branchRef, err := gitRepo.Reference(branchRefName, true)
		if err != nil {
			fmt.Printf("  Warning: Could not get current ref for branch '%s' to save undo state: %v\n", branch.Name, err)
			continue
		}
		branch.LastReflogID = branchRef.Hash().String()
		fmt.Printf("  Saved undo state for branch '%s' at commit %s\n", branch.Name, branch.LastReflogID[:7])
	}

	currentTower.Base = r.NewBase

	// Save the updated configuration with undo state
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to update tower configuration: %w", err)
	}

	// Perform custom reset logic to rebase all branches
	fmt.Println("Rebasing tower onto new base...")
	err = r.performTowerReset(gitRepo, currentTower)
	if err != nil {
		return fmt.Errorf("failed to rebase tower onto new base: %w", err)
	}

	fmt.Printf("Successfully reset tower '%s' to '%s'\n", currentTower.Name, r.NewBase)
	return nil
}

// performTowerReset performs the actual rebase operation for all branches in the tower
func (r *ResetDoCmd) performTowerReset(gitRepo *git.Repository, tower *Tower) error {
	// Get worktree for git commands
	wt, err := gitRepo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	repoPath := wt.Filesystem.Root()

	// Remember the current branch to restore later
	originalBranch, err := getCurrentBranchName(gitRepo)
	if err != nil {
		fmt.Printf("Warning: Could not determine current branch: %v\n", err)
		originalBranch = "main" // fallback
	}

	// Rebase each branch in the tower sequentially
	for i, branch := range tower.Branches {
		var baseBranchName string
		if i == 0 {
			// First branch gets rebased onto the new base
			baseBranchName = r.NewBase
		} else {
			// Subsequent branches get rebased onto the previous branch
			baseBranchName = tower.Branches[i-1].Name
		}

		fmt.Printf("  Rebasing '%s' onto '%s'...\n", branch.Name, baseBranchName)

		// Get the unique commits for this branch relative to its current base
		branchRef, err := gitRepo.Reference(plumbing.NewBranchReferenceName(branch.Name), true)
		if err != nil {
			return fmt.Errorf("failed to get reference for branch '%s': %w", branch.Name, err)
		}

		// Note: We don't need to track the old base since we're doing a full reset

		// Get unique commits for this branch relative to its immediate base in the tower structure
		var uniqueCommits []string

		if i == 0 {
			// For the first branch, we need to get ALL commits from it that should be rebased onto the new base
			// This means commits that are unique to this branch vs the new base
			newBaseRef, err := gitRepo.ResolveRevision(plumbing.Revision(r.NewBase))
			if err != nil {
				return fmt.Errorf("failed to resolve new base '%s': %w", r.NewBase, err)
			}

			// Find commits from new base to branch tip
			cmd := exec.Command("git", "rev-list", "--reverse", newBaseRef.String()+".."+branchRef.Hash().String())
			cmd.Dir = repoPath
			output, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("failed to get unique commits for branch '%s': %w", branch.Name, err)
			}
			uniqueCommits = parseCommitList(string(output))
		} else {
			// For subsequent branches, get commits unique to this branch relative to the previous branch
			// This should only be the commits that were added ON TOP of the previous branch
			prevBranchRef, err := gitRepo.Reference(plumbing.NewBranchReferenceName(tower.Branches[i-1].Name), true)
			if err != nil {
				return fmt.Errorf("failed to get reference for previous branch '%s': %w", tower.Branches[i-1].Name, err)
			}

			cmd := exec.Command("git", "rev-list", "--reverse", prevBranchRef.Hash().String()+".."+branchRef.Hash().String())
			cmd.Dir = repoPath
			output, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("failed to get unique commits for branch '%s': %w", branch.Name, err)
			}
			uniqueCommits = parseCommitList(string(output))
		}

		// If there are no unique commits, just update the branch to point to the base
		if len(uniqueCommits) == 0 {
			fmt.Printf("    No unique commits found for '%s', updating to point to '%s'\n", branch.Name, baseBranchName)
			updateCmd := exec.Command("git", "branch", "-f", branch.Name, baseBranchName)
			updateCmd.Dir = repoPath
			if output, err := updateCmd.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to update branch '%s' to '%s': %w\nOutput: %s", branch.Name, baseBranchName, err, string(output))
			}
			continue
		}

		// Perform the rebase using cherry-pick approach (similar to existing rebase logic)
		err = r.rebaseBranchOnto(gitRepo, branch.Name, baseBranchName, uniqueCommits)
		if err != nil {
			return fmt.Errorf("failed to rebase branch '%s' onto '%s': %w", branch.Name, baseBranchName, err)
		}

		fmt.Printf("    Successfully rebased '%s' onto '%s'\n", branch.Name, baseBranchName)
	}

	// Restore original branch
	fmt.Printf("Restoring original branch '%s'...\n", originalBranch)
	if err := CheckoutBranch(repoPath, originalBranch); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s': %v\n", originalBranch, err)
	}

	return nil
}

// rebaseBranchOnto rebases a single branch onto a new base using cherry-pick
func (r *ResetDoCmd) rebaseBranchOnto(gitRepo *git.Repository, branchName, newBase string, commits []string) error {
	wt, err := gitRepo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	repoPath := wt.Filesystem.Root()

	// Create a temporary branch from the new base
	tempBranch := fmt.Sprintf("temp-reset-%s-%d", branchName, time.Now().UnixNano())

	// Checkout the new base (could be branch or commit hash)
	if err := checkoutBranchOrCommit(repoPath, newBase); err != nil {
		return fmt.Errorf("failed to checkout base '%s': %w", newBase, err)
	}

	// Create temporary branch
	createCmd := exec.Command("git", "checkout", "-b", tempBranch)
	createCmd.Dir = repoPath
	if output, err := createCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create temporary branch '%s': %w\nOutput: %s", tempBranch, err, string(output))
	}

	// Cherry-pick all commits
	for _, commit := range commits {
		fmt.Printf("      Cherry-picking commit %s...\n", commit[:7])
		cherryPickCmd := exec.Command("git", "cherry-pick", commit)
		cherryPickCmd.Dir = repoPath
		if output, err := cherryPickCmd.CombinedOutput(); err != nil {
			// Check if it's an empty commit (commit already exists)
			if strings.Contains(string(output), "The previous cherry-pick is now empty") {
				fmt.Printf("        Commit %s already exists, skipping...\n", commit[:7])
				// Skip the empty commit
				skipCmd := exec.Command("git", "cherry-pick", "--skip")
				skipCmd.Dir = repoPath
				if skipOutput, skipErr := skipCmd.CombinedOutput(); skipErr != nil {
					return fmt.Errorf("failed to skip empty cherry-pick for commit '%s': %w\nOutput: %s", commit, skipErr, string(skipOutput))
				}
				continue
			}
			// TODO: Handle other conflicts properly (for now, just fail)
			return fmt.Errorf("cherry-pick failed for commit '%s': %w\nOutput: %s", commit, err, string(output))
		}
	}

	// Update the original branch to point to the temporary branch
	updateCmd := exec.Command("git", "branch", "-f", branchName, tempBranch)
	updateCmd.Dir = repoPath
	if output, err := updateCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to update branch '%s': %w\nOutput: %s", branchName, err, string(output))
	}

	// Checkout the updated branch before deleting temp branch
	if err := CheckoutBranch(repoPath, branchName); err != nil {
		fmt.Printf("Warning: Failed to checkout updated branch '%s': %v\n", branchName, err)
	}

	// Clean up temporary branch
	deleteCmd := exec.Command("git", "branch", "-D", tempBranch)
	deleteCmd.Dir = repoPath
	if output, err := deleteCmd.CombinedOutput(); err != nil {
		fmt.Printf("Warning: Failed to delete temporary branch '%s': %v\nOutput: %s", tempBranch, err, string(output))
	}

	return nil
}

// checkoutBranchOrCommit checks out either a branch name or a commit hash
func checkoutBranchOrCommit(repoPath, ref string) error {
	// Use git checkout command which can handle both branches and commit hashes
	checkoutCmd := exec.Command("git", "checkout", ref)
	checkoutCmd.Dir = repoPath
	if output, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to checkout '%s': %w\nOutput: %s", ref, err, string(output))
	}
	return nil
}

// Run executes the reset undo command
func (r *ResetUndoCmd) Run(_ *kong.Context) error {
	// Load current tower configuration
	config, _, currentTower, _, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	// Open git repository
	gitRepo, err := openGitRepo()
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Check if working directory is clean
	if err := isWorkingDirectoryClean(gitRepo); err != nil {
		return fmt.Errorf("working directory is not clean. Please commit or stash your changes before undoing reset")
	}

	// Check if there's undo state available
	if currentTower.LastRebased == "" {
		return fmt.Errorf("no reset undo state found for tower '%s'. Cannot undo reset.", currentTower.Name)
	}

	fmt.Printf("Undoing reset for tower '%s'...\n", currentTower.Name)

	// Get worktree for git commands
	wt, err := gitRepo.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}
	repoPath := wt.Filesystem.Root()

	// Remember current branch to restore later
	originalBranch, err := getCurrentBranchName(gitRepo)
	if err != nil {
		fmt.Printf("Warning: Could not determine current branch: %v\n", err)
		originalBranch = "main" // fallback
	}

	// Restore each branch from undo state (iterate backwards like rebase does)
	for i := len(currentTower.Branches) - 1; i >= 0; i-- {
		branch := &currentTower.Branches[i]

		if branch.LastReflogID == "" {
			fmt.Printf("  No undo state for branch '%s', skipping...\n", branch.Name)
			continue
		}

		fmt.Printf("  Restoring branch '%s' to commit %s...\n", branch.Name, branch.LastReflogID[:7])

		// Verify the target commit exists
		targetCommit := branch.LastReflogID
		_, err := gitRepo.CommitObject(plumbing.NewHash(targetCommit))
		if err != nil {
			fmt.Printf("  Warning: Commit %s no longer exists for branch '%s', skipping...\n", targetCommit[:7], branch.Name)
			continue
		}

		// Check if branch exists
		branchRefName := plumbing.NewBranchReferenceName(branch.Name)
		_, err = gitRepo.Reference(branchRefName, true)
		branchExists := err == nil

		// Restore branch to the saved commit
		var cmd *exec.Cmd
		if branchExists {
			// Branch exists, update it
			cmd = exec.Command("git", "update-ref", branchRefName.String(), targetCommit)
		} else {
			// Branch doesn't exist, create it
			cmd = exec.Command("git", "branch", branch.Name, targetCommit)
		}
		cmd.Dir = repoPath

		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to restore branch '%s' to commit %s: %w\nOutput: %s",
				branch.Name, targetCommit, err, string(output))
		}

		fmt.Printf("    Successfully restored '%s'\n", branch.Name)
	}

	// Clear undo state
	fmt.Println("Clearing reset undo state...")
	for i := range currentTower.Branches {
		currentTower.Branches[i].LastReflogID = ""
	}
	currentTower.LastRebased = ""

	// Save updated configuration
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration after undo: %w", err)
	}

	// Restore original branch
	fmt.Printf("Restoring original branch '%s'...\n", originalBranch)
	if err := CheckoutBranch(repoPath, originalBranch); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s': %v\n", originalBranch, err)
	}

	fmt.Printf("Successfully undid reset for tower '%s'\n", currentTower.Name)
	return nil
}
