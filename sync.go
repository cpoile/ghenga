package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/fatih/color"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// SyncCmd synchronizes the local tower branches with their remote counterparts.
type SyncCmd struct {
	Do   SyncDoCmd   `cmd:"" default:"1" hidden:"" help:"Synchronize branches with remote"`
	Undo SyncUndoCmd `cmd:"undo" help:"Undo the last sync operation for the current tower"`

	// Common fields for both subcommands (like Remote) might go here if needed,
	// but keeping them separate for now.
}

// SyncDoCmd handles the actual synchronization logic.
type SyncDoCmd struct {
	Remote string `help:"Name of the remote to synchronize with" default:"origin"`
}

// Run executes the sync command.
func (cmd *SyncDoCmd) Run(ctx *kong.Context) error {
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		cwd, _ := os.Getwd()
		return fmt.Errorf("working directory '%s' is not clean. Please commit or stash your changes before syncing", cwd)
	}

	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return fmt.Errorf("failed to load repository info: %w", err)
	}

	r, err := openGitRepo()
	if err != nil {
		return fmt.Errorf("failed to open git repository in %s: %w", repoPath, err)
	}

	// Check if we need to fetch and prompt user
	needsFetch, err := checkIfFetchNeeded(r, cmd.Remote, currentTower.Branches)
	if err != nil {
		fmt.Printf("Warning: Could not determine if fetch is needed: %v\n", err)
		fmt.Println("Proceeding with existing remote tracking branch information.")
	} else if needsFetch {
		fmt.Printf("Remote tracking branches for '%s' appear stale.\n", cmd.Remote)
		fmt.Print("Fetch latest remote state for accurate status detection? [Y/n]: ")
		var response string
		fmt.Scanln(&response)

		if strings.ToLower(response) != "n" && strings.ToLower(response) != "no" {
			fmt.Printf("Fetching latest state from remote '%s'...\n", cmd.Remote)
			fetchCmd := exec.Command("git", "fetch", cmd.Remote)
			fetchCmd.Dir = repoPath
			if output, err := fetchCmd.CombinedOutput(); err != nil {
				// Don't fail hard on fetch errors - remote might not exist or be unreachable
				// But warn the user that status detection may be inaccurate
				fmt.Printf("Warning: Failed to fetch from remote '%s': %v\n", cmd.Remote, err)
				fmt.Printf("Git output: %s\n", string(output))
				fmt.Println("Status detection may be based on stale remote information.")
			}
		} else {
			fmt.Println("Proceeding with existing remote tracking branch information.")
			fmt.Println("Note: Status detection may be based on stale remote information.")
		}
	}

	fmt.Printf("Checking branches in tower '%s' for remote '%s'...\n", currentTower.Name, cmd.Remote)

	var branchesToForcePush []string
	var branchesToNormalPush []string
	var branchesToPull []string
	processedCount := 0
	skippedCount := 0
	errorCount := 0

	// --- Pass 1: Check status of all branches ---
	for _, branch := range currentTower.Branches {
		processedCount++

		status, localCommit, remoteCommit, err := GetBranchPushStatus(r, cmd.Remote, branch.Name)

		if err != nil {
			fmt.Printf("  Branch '%s': Error checking status: %v. Skipping.\n", branch.Name, err)
			errorCount++
			skippedCount++
			continue
		}

		switch status {
		case StatusError:
			fmt.Printf("  Branch '%s': Internal error checking status. Skipping.\n", branch.Name)
			errorCount++
			skippedCount++
		case NoRemote:
			fmt.Printf("  Branch '%s': Does not exist on remote '%s'. Skipping push actions.\n", branch.Name, cmd.Remote)
			skippedCount++
		case UpToDate:
			fmt.Printf("  Branch '%s': Up-to-date with remote '%s'. Skipping.\n", branch.Name, cmd.Remote)
			skippedCount++
		case LocalAhead:
			fmt.Printf("  Branch '%s': Marked for normal push (ahead of remote '%s': %s -> %s).\n", branch.Name, cmd.Remote, remoteCommit.String()[:7], localCommit.String()[:7])
			branchesToNormalPush = append(branchesToNormalPush, branch.Name)
		case RemoteAhead:
			fmt.Printf("  Branch '%s': Marked for pull (remote '%s' is ahead: %s vs local %s).\n", branch.Name, cmd.Remote, remoteCommit.String()[:7], localCommit.String()[:7])
			branchesToPull = append(branchesToPull, branch.Name)
		case Diverged:
			fmt.Printf("  Branch '%s': Marked for force-push (diverged from remote '%s': local %s, remote %s).\n", branch.Name, cmd.Remote, localCommit.String()[:7], remoteCommit.String()[:7])
			branchesToForcePush = append(branchesToForcePush, branch.Name)
		}
	}

	// --- Confirmation Step ---
	if len(branchesToNormalPush) == 0 && len(branchesToForcePush) == 0 && len(branchesToPull) == 0 {
		fmt.Println("\nNo branches require pulling or pushing.")
		return nil
	}

	warningColor := color.New(color.FgRed).Add(color.Bold)
	infoColor := color.New(color.FgCyan)
	fmt.Println()
	warningColor.Println("WARNING: The sync command will attempt the following actions:")
	if len(branchesToPull) > 0 {
		infoColor.Printf("  - Pull (fast-forward only) for %d branch(es) where the remote is ahead.\n", len(branchesToPull))
	}
	if len(branchesToNormalPush) > 0 {
		infoColor.Printf("  - Normal push %d branch(es) that are ahead of the remote.\n", len(branchesToNormalPush))
	}
	if len(branchesToForcePush) > 0 {
		warningColor.Printf("  - Force-push (with lease) %d branch(es) that have diverged from the remote.\n", len(branchesToForcePush))
	}
	infoColor.Println("  - Branches that are up-to-date or have no remote will be skipped.")
	infoColor.Println("  - This operation will clear the previous sync undo state.")
	warningColor.Println("Force-pushing modifies history. Pulling may fail if not a fast-forward.")
	warningColor.Println("Ensure you understand the consequences and have a clean working directory.")

	fmt.Print("\nDo you want to proceed with synchronizing these branches? [y/N]: ")
	var response string
	fmt.Scanln(&response)

	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Sync operation cancelled.")
		return nil
	}

	fmt.Println("Clearing previous sync undo state...")
	for i := range currentTower.Branches {
		currentTower.Branches[i].PreSyncReflogID = ""
	}
	currentTower.LastSynced = ""
	// Save immediately after clearing, in case the next step fails
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to clear previous sync undo state in config: %w", err)
	}

	undoStateChanged := false
	branchesToModify := slices.Concat(branchesToPull, branchesToNormalPush, branchesToForcePush)

	for _, branchName := range branchesToModify {
		found := false
		for i := range currentTower.Branches {
			if currentTower.Branches[i].Name == branchName {
				branchRefName := plumbing.NewBranchReferenceName(branchName)
				branchRef, err := getReference(r, branchRefName)
				if err != nil {
					fmt.Printf("Warning: Could not get current ref for branch '%s' to save undo state: %v\n", branchName, err)
					continue
				}
				currentTower.Branches[i].PreSyncReflogID = branchRef.Hash().String()
				undoStateChanged = true
				found = true
				break
			}
		}
		if !found {
			fmt.Printf("Warning: Branch '%s' marked for modification but not found in tower config. Cannot save undo state.\n", branchName)
		}
	}

	if undoStateChanged {
		currentTower.LastSynced = time.Now().Format(time.RFC3339)
		if err := SaveConfig(config); err != nil {
			// TODO: If this save fails, the undo state might be inconsistent (IDs saved but not timestamp)
			// Should we try to revert the ID saves?
			return fmt.Errorf("failed to save configuration with sync undo state: %w", err)
		}
		fmt.Println("Saved pre-sync state for modified branches.")
	}

	// --- Pass 2: Execute Pushes and Pulls ---
	successNormalCount := 0
	failedNormalCount := 0
	successForceCount := 0
	failedForceCount := 0
	successPullCount := 0
	failedPullCount := 0

	// Get original branch to restore later
	headRef, err := getHead(r)
	if err != nil {
		fmt.Printf("Warning: could not determine current branch: %v\n", err)
	}
	originalBranchName := ""
	if headRef != nil && headRef.Name().IsBranch() {
		originalBranchName = headRef.Name().Short()
	}

	successColor := color.New(color.FgGreen).Add(color.Bold)
	errorColor := color.New(color.FgRed).Add(color.Bold)

	// Execute Pulls
	if len(branchesToPull) > 0 {
		fmt.Printf("\nExecuting pulls (fast-forward only) for %d branches...\n", len(branchesToPull))
		for _, branchName := range branchesToPull {
			fmt.Printf("  Pulling branch '%s'...\n", branchName)

			// Checkout branch first
			if err := CheckoutBranch(repoPath, branchName); err != nil {
				failedPullCount++
				errorColor.Printf("    Error checking out branch '%s' before pull: %s\n", branchName, err)
				errorCount++
				continue
			}

			// Attempt fast-forward pull
			pullCmd := exec.Command("git", "pull", cmd.Remote, branchName, "--ff-only")
			pullCmd.Dir = repoPath
			output, err := pullCmd.CombinedOutput()
			pullOutput := strings.TrimSpace(string(output))

			if err != nil {
				failedPullCount++
				errorColor.Printf("    Error pulling branch '%s': %s\n    Output: %s\n", branchName, err, pullOutput)
				errorCount++
			} else {
				successPullCount++
				successColor.Printf("    Successfully pulled branch '%s'\n    Output: %s\n", branchName, pullOutput)
			}
		}
	}

	// Execute Normal Pushes
	if len(branchesToNormalPush) > 0 {
		fmt.Printf("\nExecuting normal pushes for %d branches...\n", len(branchesToNormalPush))
		for _, branchName := range branchesToNormalPush {
			fmt.Printf("  Pushing branch '%s'...\n", branchName)
			gitPushCmd := exec.Command("git", "push", cmd.Remote, branchName)
			gitPushCmd.Dir = repoPath
			var pushStdout, pushStderr bytes.Buffer
			gitPushCmd.Stdout = &pushStdout
			gitPushCmd.Stderr = &pushStderr

			err = gitPushCmd.Run()
			if err != nil {
				failedNormalCount++
				errMsg := strings.TrimSpace(pushStderr.String())
				if errMsg == "" {
					errMsg = strings.TrimSpace(pushStdout.String())
				}
				if errMsg == "" {
					errMsg = err.Error()
				}
				errorColor.Printf("    Error pushing branch '%s': %s\n", branchName, errMsg)
				errorCount++
			} else {
				successNormalCount++
				successColor.Printf("    Successfully pushed branch '%s'\n", branchName)
			}
		}
	}

	// Execute Force Pushes
	if len(branchesToForcePush) > 0 {
		fmt.Printf("\nExecuting force-pushes (with lease) for %d branches...\n", len(branchesToForcePush))
		for _, branchName := range branchesToForcePush {
			fmt.Printf("  Force-pushing branch '%s'...\n", branchName)

			// Use simple force-with-lease (relies on remote tracking branches)
			gitCmd := exec.Command("git", "push", "--force-with-lease", cmd.Remote, branchName)
			gitCmd.Dir = repoPath

			var stdout, stderr bytes.Buffer
			gitCmd.Stdout = &stdout
			gitCmd.Stderr = &stderr

			err = gitCmd.Run()

			if err != nil {
				failedForceCount++
				errMsg := strings.TrimSpace(stderr.String())
				if errMsg == "" {
					errMsg = strings.TrimSpace(stdout.String())
				}
				if errMsg == "" {
					errMsg = err.Error()
				}
				errorColor.Printf("    Error force-pushing branch '%s': %s\n", branchName, errMsg)
				errorCount++
			} else {
				successForceCount++
				successColor.Printf("    Successfully force-pushed branch '%s'\n", branchName)
			}
		}
	}

	// Restore original branch if possible
	if originalBranchName != "" {
		fmt.Printf("\nRestoring original branch '%s'...\n", originalBranchName)
		if err := CheckoutBranch(repoPath, originalBranchName); err != nil {
			fmt.Printf("Warning: Failed to restore original branch '%s': %v\n", originalBranchName, err)
		}
	}

	// --- Final Summary ---
	fmt.Printf("\nSync summary:\n")
	fmt.Printf("  Total branches checked: %d\n", processedCount)
	fmt.Printf("  Skipped (up-to-date, no remote): %d\n", skippedCount)
	fmt.Printf("  Successfully pulled (ff-only): %d\n", successPullCount)
	fmt.Printf("  Successfully pushed (normal): %d\n", successNormalCount)
	fmt.Printf("  Successfully pushed (force): %d\n", successForceCount)
	fmt.Printf("  Failed to pull (ff-only): %d\n", failedPullCount)
	fmt.Printf("  Failed to push (normal): %d\n", failedNormalCount)
	fmt.Printf("  Failed to push (force): %d\n", failedForceCount)
	fmt.Printf("  Errors during checks/operations: %d\n", errorCount)

	// Save config (in case any repo/tower creation happened, though unlikely here)
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	if failedPullCount > 0 || failedNormalCount > 0 || failedForceCount > 0 || errorCount > 0 {
		return fmt.Errorf("some operations failed during sync")
	}

	return nil
}

// --- Undo Command ---

type SyncUndoCmd struct {
}

func (cmd *SyncUndoCmd) Run(ctx *kong.Context) error {
	// Check Git Status first
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		cwd, _ := os.Getwd()
		return fmt.Errorf("working directory '%s' is not clean. Please commit or stash your changes before undoing sync", cwd)
	}

	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	if currentTower.LastSynced == "" {
		return fmt.Errorf("no previous sync operation found to undo for tower '%s'", currentTower.Name)
	}

	branchesToRestore := 0
	for _, branch := range currentTower.Branches {
		if branch.PreSyncReflogID != "" {
			branchesToRestore++
		}
	}

	if branchesToRestore == 0 {
		// This might happen if the last sync didn't modify any branches or state wasn't saved correctly
		fmt.Printf("No specific branch states found to restore from the sync at %s for tower '%s'.\n",
			currentTower.LastSynced, currentTower.Name)
		fmt.Println("Clearing the last sync timestamp.")
		currentTower.LastSynced = ""
		if err := SaveConfig(config); err != nil {
			return fmt.Errorf("failed to clear sync timestamp: %w", err)
		}
		return nil
	}

	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

	warningColor := color.New(color.FgRed).Add(color.Bold)

	warningColor.Println("\nWARNING: Undoing the last sync will reset LOCAL branches to their previous state.")
	warningColor.Printf("This will attempt to restore %d branches to their state before the sync on %s.\n",
		branchesToRestore, currentTower.LastSynced)
	warningColor.Println("Any changes made to these branches after the sync might be lost.")
	warningColor.Println("This operation modifies local branches only. You will need to push/force-push them to the remote manually.")

	fmt.Print("\nDo you want to proceed with undoing the last sync? [y/N]: ")
	var response string
	fmt.Scanln(&response)

	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Undo operation cancelled.")
		return nil
	}

	// Get the current branch to restore it at the end
	head, err := getHead(gitRepo)
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	originalBranch := head.Name().Short()

	fmt.Println("\nRestoring branches...")
	for i := len(currentTower.Branches) - 1; i >= 0; i-- {
		branch := &currentTower.Branches[i]

		if branch.PreSyncReflogID == "" {
			continue
		}

		fmt.Printf("Attempting to restore branch '%s' to %s...\n", branch.Name, branch.PreSyncReflogID[:7])

		branchRefName := plumbing.NewBranchReferenceName(branch.Name)
		_, err := getReference(gitRepo, branchRefName)
		branchExistsLocally := err == nil

		var restoreCmd *exec.Cmd
		if branchExistsLocally {
			// Branch exists, update its ref
			restoreCmd = exec.Command("git", "update-ref", branchRefName.String(), branch.PreSyncReflogID)
		} else {
			// Branch doesn't exist locally, create it pointing to the old commit
			restoreCmd = exec.Command("git", "branch", branch.Name, branch.PreSyncReflogID)
		}
		restoreCmd.Dir = repoPath

		if output, err := restoreCmd.CombinedOutput(); err != nil {
			fmt.Printf("    Warning: Failed to restore branch '%s': %v\n    Output: %s\n", branch.Name, err, string(output))
		} else {
			fmt.Printf("    Successfully restored branch '%s'\n", branch.Name)
			// Clear the stored reflog ID for this branch
			branch.PreSyncReflogID = ""
		}
	}

	// Clear the overall sync timestamp
	currentTower.LastSynced = ""

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to update configuration after undo: %w", err)
	}

	// Restore the original branch
	checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
	checkoutOriginalCmd.Dir = repoPath
	if err := checkoutOriginalCmd.Run(); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s': %v\n", originalBranch, err)
	}

	fmt.Println("\nSuccessfully undid the last sync operation!")
	return nil
}

// checkIfFetchNeeded determines if remote tracking branches are stale and need updating
func checkIfFetchNeeded(r *git.Repository, remoteName string, branches []Branch) (bool, error) {
	// Simple heuristic: check if any remote tracking branches exist but are old
	// This is a reasonable approximation - in practice, if remote tracking branches
	// exist, they should be relatively recent if the user has been fetching

	hasRemoteTrackingBranches := false
	for _, branch := range branches {
		remoteRefName := plumbing.NewRemoteReferenceName(remoteName, branch.Name)
		if _, err := getReference(r, remoteRefName); err == nil {
			hasRemoteTrackingBranches = true
			break
		}
	}

	// If no remote tracking branches exist, we definitely need to fetch
	if !hasRemoteTrackingBranches {
		return true, nil
	}

	// If remote tracking branches exist, we'll assume they're reasonably current
	// This avoids the complexity of checking timestamps or making actual network calls
	// Users can always choose to fetch anyway when prompted
	return false, nil
}
