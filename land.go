package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5/plumbing"
)

// TODO: set remote so we don't need to ask for it
type LandCmd struct {
	Remote string `help:"Name of the remote to fetch from/check against" default:"origin"`
	// SkipSyncCheck bypasses the pre-land divergence check on tower branches. This is for landing
	// several branches in a row: each land rebases the upper branches, so they read as "diverged"
	// from their now-stale remotes, and re-syncing between every land would needlessly force-push
	// and churn CI on branches that are about to be rebased again. Only safe when any divergence is
	// from your own local rebasing rather than unpushed remote work.
	SkipSyncCheck bool `help:"Skip the check that tower branches are synced with the remote (for landing several branches before syncing)"`
}

func (l *LandCmd) Run(ctx *kong.Context) error {
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		cwd, _ := os.Getwd()
		return fmt.Errorf("working directory '%s' is not clean. Please commit or stash your changes before landing", cwd)
	}

	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return fmt.Errorf("failed to load repository info: %w", err)
	}

	if len(currentTower.Branches) == 0 {
		return fmt.Errorf("current tower '%s' has no branches to land", currentTower.Name)
	}

	// Verify every tower worktree is clean *before* doing anything destructive. Landing later rebases
	// the upper branches in their worktrees, and a dirty worktree would abort that rebase only after
	// the base branch has been updated and the bottom branch removed from the tower — a half-landed
	// state. The cwd check above only covers the current directory, not the other worktrees.
	if err := checkTowerWorktreesClean(repoPath, currentTower.Branches); err != nil {
		return err
	}

	r, err := openGitRepo()
	if err != nil {
		return fmt.Errorf("failed to open git repository in %s: %w", repoPath, err)
	}

	// --- Get current branch --- (needed for checkout restore)
	originalBranchName, err := getCurrentBranchName(r)
	if err != nil {
		fmt.Printf("Warning: could not determine current branch: %v\n", err)
	}
	defer func() {
		if originalBranchName != "" {
			// Check if a rebase or merge is paused — if so, don't restore the original branch
			// because the user needs to stay on the temporary/detached HEAD for 'continue'.
			_, _, reloadedTower, _, reloadErr := loadRepoInfoAndCurrentTower()
			if reloadErr == nil && reloadedTower.operationInProgress() {
				// Operation is paused; leave the user where the conflict resolution happens.
				return
			}

			fmt.Printf("\nRestoring original branch: %s\n", originalBranchName)
			checkoutCmd := exec.Command("git", "checkout", originalBranchName)
			checkoutCmd.Dir = repoPath
			if output, err := checkoutCmd.CombinedOutput(); err != nil {
				fmt.Printf("  Warning: failed to restore original branch '%s': %v\nOutput: %s\n", originalBranchName, err, string(output))
			}
		}
	}()

	if l.SkipSyncCheck {
		fmt.Println("Skipping tower branch sync check (--skip-sync-check). Remember to 'ghenga sync' once you're done landing.")
	} else if err := validateTowerBranchStatus(r, l.Remote, currentTower); err != nil {
		return err
	}

	fmt.Println("Checking merge status of bottom branch...")
	bottomBranch := currentTower.Branches[0]
	fmt.Printf("  Bottom branch: %s\n", bottomBranch.Name)

	// Update remote references before checking
	fmt.Printf("  Fetching from remote '%s' to update references...\n", l.Remote)
	fetchCmd := exec.Command("git", "fetch", l.Remote)
	fetchCmd.Dir = repoPath
	if output, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to fetch from remote '%s': %w\nOutput: %s", l.Remote, err, string(output))
	}

	remoteBranchRefName := plumbing.NewRemoteReferenceName(l.Remote, bottomBranch.Name)
	_, err = getReference(r, remoteBranchRefName)
	if err == nil {
		// Remote branch reference *exists*
		return fmt.Errorf("remote branch '%s/%s' still exists. Please ensure it is merged and deleted on the remote before landing", l.Remote, bottomBranch.Name)
	} else if err != plumbing.ErrReferenceNotFound {
		// Some other error occurred trying to check the reference
		return fmt.Errorf("failed to check remote branch '%s/%s' status: %w", l.Remote, bottomBranch.Name, err)
	}
	fmt.Printf("  Remote branch '%s/%s' not found, assuming merged and deleted.\n", l.Remote, bottomBranch.Name)

	// --- Validate base branch before confirmation ---
	if currentTower.Base == "" {
		return fmt.Errorf("tower '%s' has no base branch set. Use 'ghenga base <branch-name>' to set it", currentTower.Name)
	}

	// --- Confirmation step ---
	remainingBranchCount := len(currentTower.Branches) - 1
	fmt.Printf("\nLanding will perform the following actions:\n")
	fmt.Printf("  - Remove branch '%s' from tower '%s'\n", bottomBranch.Name, currentTower.Name)
	if remainingBranchCount > 0 {
		if currentTower.strategy() == StrategyMerge {
			fmt.Printf("  - Merge updated base '%s' down into %d remaining branch(es)\n", currentTower.Base, remainingBranchCount)
			fmt.Printf("  - This preserves existing commit hashes — no force-push required\n")
		} else {
			fmt.Printf("  - Rebase %d remaining branch(es) onto updated base '%s'\n", remainingBranchCount, currentTower.Base)
			fmt.Printf("  - This will change commit hashes and require force-push if branches exist on remote\n")
		}
	} else {
		fmt.Printf("  - No remaining branches to rebase (tower will be empty)\n")
	}
	if currentTower.strategy() == StrategyMerge {
		fmt.Printf("  - You can undo this operation with 'ghenga merge undo'\n")
	} else {
		fmt.Printf("  - You can undo this operation with 'ghenga rebase undo'\n")
	}

	fmt.Print("\nProceed with landing? [y/N]: ")
	var response string
	fmt.Scanln(&response)
	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Landing operation cancelled.")
		return nil
	}

	fmt.Println("\nStarting landing sequence...")

	// --- Update base branch ---
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

	// --- Update or merge remaining branches ---
	if len(currentTower.Branches) == 0 {
		fmt.Println("  No remaining branches in the tower to update.")
		fmt.Printf("\nBranch '%s' landed and removed from tower '%s'. Tower is now empty.\n", landedBranchName, currentTower.Name)
		return nil
	}

	if currentTower.strategy() == StrategyMerge {
		// Merge strategy: merge the updated base down into each remaining branch. This adds a merge
		// commit to each branch but preserves existing commit hashes so reviewers keep GitHub state.
		// Pass resetNewBase="" so mergeTowerWithMode uses tower.Base (already fast-forwarded above)
		// as the merge source for the first remaining branch, then chains upward.
		fmt.Printf("  Merging updated base into remaining branches in tower '%s'...\n", currentTower.Name)
		err = mergeTowerWithMode(MergeModeReset, true, "", "")
		if err == errMergePaused {
			fmt.Printf("\nLand operation paused due to merge conflicts.\n")
			fmt.Printf("Resolve conflicts and run 'ghenga merge continue' to complete the landing,\n")
			fmt.Printf("or run 'ghenga merge cancel' to abort and return to the previous state.\n")

			_, _, reloadedTower, _, reloadErr := loadRepoInfoAndCurrentTower()
			if reloadErr == nil && reloadedTower.MergeState != nil {
				targetBranch := reloadedTower.MergeState.TargetBranch
				baseBranch := reloadedTower.MergeState.BaseBranch
				return fmt.Errorf("failed during merge of updated base into remaining tower branches: failed to merge '%s' into '%s'", baseBranch, targetBranch)
			}
			return fmt.Errorf("failed during merge of updated base into remaining tower branches")
		}
		if err != nil {
			return fmt.Errorf("failed to merge updated base into remaining tower branches: %w", err)
		}
		fmt.Println("  Successfully merged updated base into all remaining branches.")

		fmt.Printf("\nBranch '%s' landed and removed from tower '%s'. Remaining branches updated via merge.\n", landedBranchName, currentTower.Name)
		return nil
	}

	// Rebase strategy: rebase each remaining branch onto the updated base, rewriting commit hashes.
	// Use RebaseModeReset with firstBranchExcludeBase to:
	// - Rebase first remaining branch onto currentTower.Base, excluding commits from landedBranchName
	// - Rebase subsequent branches onto their predecessors
	// This gives us state tracking, undo support, and conflict handling
	fmt.Printf("  Rebasing remaining branches in tower '%s'...\n", currentTower.Name)
	err = rebaseTowerWithMode(RebaseModeReset, true, "", "", currentTower.Base, landedBranchName)
	if err == errRebasePaused {
		// Rebase paused due to conflicts - delegate to rebase infrastructure for resolution
		fmt.Printf("\nLand operation paused due to rebase conflicts.\n")
		fmt.Printf("Resolve conflicts and run 'ghenga rebase continue' to complete the landing,\n")
		fmt.Printf("or run 'ghenga rebase cancel' to abort and return to the previous state.\n")

		// Reload config to get updated rebase state for error message
		_, _, reloadedTower, _, reloadErr := loadRepoInfoAndCurrentTower()
		if reloadErr == nil && reloadedTower.RebaseState != nil {
			targetBranch := reloadedTower.RebaseState.TargetBranch
			baseBranch := reloadedTower.RebaseState.BaseBranch
			return fmt.Errorf("failed during sequential rebase of remaining tower branches: failed to rebase '%s' onto '%s'", targetBranch, baseBranch)
		}
		return fmt.Errorf("failed during sequential rebase of remaining tower branches")
	}
	if err != nil {
		return fmt.Errorf("failed to rebase remaining tower branches: %w", err)
	}
	fmt.Println("  Successfully rebased all remaining branches.")

	fmt.Printf("\nBranch '%s' landed and removed from tower '%s'. Remaining branches rebased.\n", landedBranchName, currentTower.Name)
	return nil
}
