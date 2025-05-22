package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5/plumbing"
)

// TODO: set remote so we don't need to ask for it
type LandCmd struct {
	Remote string `help:"Name of the remote to fetch from/check against" default:"origin"`
}

func (l *LandCmd) Run(ctx *kong.Context) error {
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		return fmt.Errorf("working directory is not clean. Please commit or stash your changes before landing")
	}

	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return fmt.Errorf("failed to load repository info: %w", err)
	}

	if len(currentTower.Branches) == 0 {
		return fmt.Errorf("current tower '%s' has no branches to land", currentTower.Name)
	}

	r, err := openGitRepo()
	if err != nil {
		return fmt.Errorf("failed to open git repository in %s: %w", repoPath, err)
	}

	// --- Get current branch --- (needed for checkout restore)
	originalBranchName, err := getCurrentBranchName(r)
	if err == nil {
		fmt.Printf("Warning: could not determine current branch: %v\n", err)
	}
	defer func() {
		if originalBranchName != "" {
			fmt.Printf("\nRestoring original branch: %s\n", originalBranchName)
			checkoutCmd := exec.Command("git", "checkout", originalBranchName)
			checkoutCmd.Dir = repoPath
			if output, err := checkoutCmd.CombinedOutput(); err != nil {
				fmt.Printf("  Warning: failed to restore original branch '%s': %v\nOutput: %s\n", originalBranchName, err, string(output))
			}
		}
	}()

	fmt.Println("Checking tower branches for divergence...")
	hasDiverged := false
	for _, branch := range currentTower.Branches {
		status, _, _, err := GetBranchPushStatus(r, l.Remote, branch.Name)
		if err != nil {
			// Handle cases like local branch deleted but still in config?
			fmt.Printf("  Warning: Could not check status for branch '%s': %v\n", branch.Name, err)
			continue // Or should this be an error?
		}

		if status == Diverged {
			fmt.Printf("  Error: Branch '%s' has diverged from the remote '%s'.\n", branch.Name, l.Remote)
			hasDiverged = true
		} else if status == RemoteAhead {
			// Also consider RemoteAhead as needing attention before landing
			fmt.Printf("  Error: Remote branch '%s/%s' is ahead of local branch '%s'.\n", l.Remote, branch.Name, branch.Name)
			hasDiverged = true // Treat as needing rebase/sync
		}
	}
	if hasDiverged {
		return fmt.Errorf("one or more tower branches have diverged or are behind the remote. Please run 'ghenga rebase' or 'ghenga sync' (respectively) to bring them up to date")
	}
	fmt.Println("  All tower branches are up-to-date or ahead of remote.")

	fmt.Println("Checking merge status of bottom branch...")
	bottomBranch := currentTower.Branches[0]
	fmt.Printf("  Bottom branch: %s\n", bottomBranch.Name)

	remoteBranchRefName := plumbing.NewRemoteReferenceName(l.Remote, bottomBranch.Name)
	_, err = r.Reference(remoteBranchRefName, false) // false = don't resolve symbolic refs
	if err == nil {
		// Remote branch reference *exists*
		return fmt.Errorf("remote branch '%s/%s' still exists. Please ensure it is merged and deleted on the remote before landing", l.Remote, bottomBranch.Name)
	} else if err != plumbing.ErrReferenceNotFound {
		// Some other error occurred trying to check the reference
		return fmt.Errorf("failed to check remote branch '%s/%s' status: %w", l.Remote, bottomBranch.Name, err)
	}
	fmt.Printf("  Remote branch '%s/%s' not found, assuming merged and deleted.\n", l.Remote, bottomBranch.Name)

	fmt.Println("\nStarting landing sequence...")

	// --- Update base branch ---
	if currentTower.Base == "" {
		return fmt.Errorf("tower '%s' has no base branch set. Use 'ghenga base <branch-name>' to set it", currentTower.Name)
	}
	fmt.Printf("  Updating base branch '%s' from remote '%s'...\n", currentTower.Base, l.Remote)
	if err := updateLocalBranchFromRemote(repoPath, r, l.Remote, currentTower.Base, originalBranchName); err != nil {
		return fmt.Errorf("failed to update base branch '%s': %w", currentTower.Base, err)
	}
	fmt.Printf("  Base branch '%s' updated successfully.\n", currentTower.Base)

	// --- Remove bottom branch from config (leave it in git, let user decide) ---
	landedBranchName := bottomBranch.Name // Keep track of the name for messages
	fmt.Printf("  Removing landed branch '%s' from tower configuration...\n", landedBranchName)
	currentTower.Branches = currentTower.Branches[1:] // Remove the first branch

	fmt.Println("  Saving updated tower configuration...")
	if err := SaveConfig(config); err != nil {
		// Non-fatal? Maybe just warn?
		fmt.Printf("  Warning: failed to save configuration after removing branch: %v\n", err)
		// Continue with git delete? Let's return error for now to be safe.
		return fmt.Errorf("failed to save config after removing branch '%s': %w", landedBranchName, err)
	}

	// --- Rebase new bottom branch onto base ---
	if len(currentTower.Branches) > 0 {
		newBottomBranch := currentTower.Branches[0]
		fmt.Printf("  Rebasing new bottom branch '%s' onto base '%s'...\n", newBottomBranch.Name, currentTower.Base)

		fmt.Printf("    Checking out '%s'...\n", newBottomBranch.Name)
		checkoutCmd := exec.Command("git", "checkout", newBottomBranch.Name)
		checkoutCmd.Dir = repoPath
		if output, err := checkoutCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to checkout new bottom branch '%s': %w\nOutput: %s", newBottomBranch.Name, err, string(output))
		}

		// landedBranchName is the old base of newBottomBranch
		fmt.Printf("    Running 'git rebase --onto %s %s %s'...\n", currentTower.Base, landedBranchName, newBottomBranch.Name)
		rebaseCmd := exec.Command("git", "rebase", "--onto", currentTower.Base, landedBranchName, newBottomBranch.Name)
		rebaseCmd.Dir = repoPath
		var rebaseStdout, rebaseStderr bytes.Buffer
		rebaseCmd.Stdout = &rebaseStdout
		rebaseCmd.Stderr = &rebaseStderr

		err = rebaseCmd.Run()
		if err != nil {
			errMsg := strings.TrimSpace(rebaseStderr.String())
			if errMsg == "" {
				errMsg = strings.TrimSpace(rebaseStdout.String())
			}
			fmt.Println("    Rebase failed. Attempting to abort...")
			abortCmd := exec.Command("git", "rebase", "--abort")
			abortCmd.Dir = repoPath
			abortCmd.Run() // Run abort, ignore errors for now
			return fmt.Errorf("failed to rebase '%s' onto '%s' (from base '%s'): %w\nOutput: %s", newBottomBranch.Name, currentTower.Base, landedBranchName, err, errMsg)
		}
		fmt.Printf("  Successfully rebased '%s' onto '%s'.\n", newBottomBranch.Name, currentTower.Base)

		// Update originalBranchName in case we are now on the new bottom branch
		originalBranchName = newBottomBranch.Name
	} else {
		fmt.Println("  No remaining branches in the tower to rebase.")
		// If no branches left after removing the bottom one, we are done.
		fmt.Printf("\nBranch '%s' landed and removed from tower '%s'. Tower is now empty.\n", landedBranchName, currentTower.Name)
		return nil
	}

	// --- Run rebase command on the rest of the tower ---
	if len(currentTower.Branches) > 0 {
		fmt.Println("\nRebasing remaining tower branches sequentially...")
		if err := rebaseTower(true, "", ""); err != nil {
			// The rebaseTower function handles aborting on failure
			return fmt.Errorf("failed during sequential rebase of remaining tower branches: %w", err)
		}
		fmt.Println("Remaining tower branches rebased successfully.")
	}

	fmt.Printf("\nBranch '%s' landed and removed from tower '%s'. Remaining branches rebased.\n", landedBranchName, currentTower.Name)
	return nil
}
