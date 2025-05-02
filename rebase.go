package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/fatih/color"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// BranchInfo is used internally during rebase planning
type BranchInfo struct {
	Index          int
	Name           string
	BaseBranchName string
	UniqueCommits  []string // List of unique commit hashes in reverse order (oldest first)
	IsDiverged     bool
}

type RebaseCmd struct {
	Do       RebaseDoCmd       `cmd:"" default:"1" hidden:"" help:"Rebase all branches in the current tower that have diverged from their base"`
	Undo     RebaseUndoCmd     `cmd:"undo" help:"Undo the last rebase operation for the current tower"`
	Continue RebaseContinueCmd `cmd:"continue" help:"Continue a paused rebase operation after resolving conflicts"`
}

type RebaseDoCmd struct {
}

func (r *RebaseDoCmd) Run(_ *kong.Context) error {
	// Check if a rebase is already in progress
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}
	if currentTower.RebaseState != nil && currentTower.RebaseState.IsInProgress {
		return fmt.Errorf("a rebase is already in progress for tower '%s'. Resolve conflicts and run 'ghenga rebase continue' or clear the state manually", currentTower.Name)
	}

	// Check Git Status first
	statusCmd := exec.Command("git", "status", "--porcelain")
	// TODO: needed?
	statusCmd.Dir = repoPath // Ensure command runs in the correct directory
	output, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	if len(strings.TrimSpace(string(output))) > 0 {
		// Check if the only changes are due to an in-progress rebase (e.g., conflicts)
		isConflict, _ := hasConflicts(repoPath)
		if !isConflict {
			return fmt.Errorf("working directory is not clean. Please commit or stash your changes before starting a rebase")
		}
		// If it IS a conflict state, we should still prevent starting a *new* rebase
		return fmt.Errorf("working directory has changes, possibly from a previous rebase attempt. Please resolve conflicts and run 'ghenga rebase continue' or clean the directory")
	}

	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

	return rebaseTower(config, currentTower, repoPath, gitRepo, false) // Call the refactored function
}

// rebaseTower performs the core logic of rebasing a tower's branches.
// It checks for divergence, saves undo state, asks for confirmation (if skipConfirmation is false),
// and performs the sequential rebase, pausing if conflicts occur.
func rebaseTower(config *Config, currentTower *Tower, repoPath string, gitRepo *git.Repository, skipConfirmation bool) error {
	if currentTower.Base == "" {
		return fmt.Errorf("tower '%s' has no base branch set. Use 'ghenga base <branch-name>' to set it first", currentTower.Name)
	}
	if len(currentTower.Branches) < 2 {
		fmt.Printf("Tower '%s' has less than two branches, nothing to rebase relative to each other.\n", currentTower.Name)
		return nil
	}

	// Check for existing RebaseState and clear if user confirms
	if currentTower.RebaseState != nil && currentTower.RebaseState.IsInProgress {
		warningColor := color.New(color.FgRed).Add(color.Bold)
		warningColor.Printf("WARNING: Found paused rebase state from a previous attempt.\n")
		fmt.Print("Do you want to clear the previous ghenga rebase state and start a new one? [y/N]: ")
		var clearResponse string
		fmt.Scanln(&clearResponse)
		if strings.ToLower(clearResponse) != "y" && strings.ToLower(clearResponse) != "yes" {
			fmt.Println("Rebase operation cancelled. Run 'ghenga rebase continue' to resume the previous rebase.")
			return nil
		}
		fmt.Println("Clearing previous rebase state...")
		currentTower.RebaseState = nil
		// Save cleared state
		if err := SaveConfig(config); err != nil {
			return fmt.Errorf("failed to clear previous rebase state in config: %w", err)
		}
	}

	if !skipConfirmation && currentTower.LastRebased != "" {
		warningColor := color.New(color.FgRed).Add(color.Bold)
		warningColor.Printf("WARNING: Found previous rebase undo state from %s.\n", currentTower.LastRebased)
		fmt.Print("Do you want to clear the previous undo state and continue with the rebase? [y/N]: ")
		var clearResponse string
		fmt.Scanln(&clearResponse)
		if strings.ToLower(clearResponse) != "y" && strings.ToLower(clearResponse) != "yes" {
			fmt.Println("Rebase operation cancelled.")
			return nil
		}
	}

	// Always clear previous undo state if proceeding
	fmt.Println("Clearing/preparing rebase undo state...")
	for i := range currentTower.Branches {
		currentTower.Branches[i].LastReflogID = ""
	}
	currentTower.LastRebased = ""
	// Save cleared state first before adding new state
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to clear previous rebase undo state in config: %w", err)
	}

	fmt.Println("Saving pre-rebase state for undo...")
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
	}

	if err := SaveConfig(config); err != nil {
		// If this save fails, the timestamp might be set but not the hashes
		// TODO: Should we attempt to clear the timestamp if hashes weren't saved?
		return fmt.Errorf("failed to save configuration with branch states: %w", err)
	}

	// Use internal BranchInfo struct for analysis
	var branchInfos []BranchInfo

	originalBranch, err := getCurrentBranchName(gitRepo) // Get original branch name
	if err != nil {
		fmt.Printf("Warning: Could not determine current branch, defaulting to HEAD: %v\n", err)
		head, headErr := gitRepo.Head()
		if headErr == nil && head != nil {
			originalBranch = head.Name().Short() // Best effort
		} else {
			originalBranch = "HEAD" // Fallback
		}
	}

	// First pass: collect all branches, their unique commits, and determine which have diverged
	fmt.Println("Analyzing branches for rebase...")
	for i := 1; i < len(currentTower.Branches); i++ {
		branch := currentTower.Branches[i]
		baseBranch := currentTower.Branches[i-1]

		branchRefName := plumbing.NewBranchReferenceName(branch.Name)
		branchHead, err := gitRepo.Reference(branchRefName, true)
		if err != nil {
			fmt.Printf("  Skipping branch '%s': does not exist locally.\n", branch.Name)
			continue
		}

		baseRefName := plumbing.NewBranchReferenceName(baseBranch.Name)
		baseHead, err := gitRepo.Reference(baseRefName, true)
		if err != nil {
			fmt.Printf("  Skipping branch '%s': its base '%s' does not exist locally.\n", branch.Name, baseBranch.Name)
			continue
		}

		// Find common ancestor
		mergeBase, err := findMergeBase(gitRepo, branchHead.Hash(), baseHead.Hash())
		if err != nil {
			return fmt.Errorf("failed to find merge base between '%s' and '%s': %w", branch.Name, baseBranch.Name, err)
		}

		branchInfo := BranchInfo{
			Index:          i,
			Name:           branch.Name,
			BaseBranchName: baseBranch.Name,
			UniqueCommits:  []string{},
			IsDiverged:     mergeBase != baseHead.Hash(),
		}

		// Use git rev-list to find unique commits
		cmd := exec.Command("git", "rev-list", "--reverse", baseHead.Hash().String()+".."+branchHead.Hash().String())
		cmd.Dir = repoPath
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to list unique commits for '%s': %w", branch.Name, err)
		}

		commits := strings.Split(strings.TrimSpace(string(output)), "\n")
		// Filter out empty strings which can happen if output is empty
		filteredCommits := []string{}
		for _, c := range commits {
			if c != "" {
				filteredCommits = append(filteredCommits, c)
			}
		}
		branchInfo.UniqueCommits = filteredCommits

		branchInfos = append(branchInfos, branchInfo)
	}

	// Keep every branch after (and including) the first diverged branch
	var divergedBranches []BranchInfo
	firstDivergedFound := false
	for i := range branchInfos {
		if branchInfos[i].IsDiverged {
			firstDivergedFound = true
		}
		if firstDivergedFound {
			divergedBranches = append(divergedBranches, branchInfos[i])
		}
	}

	if len(divergedBranches) == 0 {
		fmt.Println("No diverged branches found in the current tower. All branches are up to date.")
		// Clear any potential leftover rebase state
		currentTower.RebaseState = nil
		_ = SaveConfig(config) // Best effort save
		return nil
	}

	branchColor := color.New(color.FgYellow)
	warningColor := color.New(color.FgRed).Add(color.Bold)

	fmt.Printf("Found %d branch(es) needing rebase in tower '%s':\n", len(divergedBranches), currentTower.Name)
	for _, db := range divergedBranches {
		commitCount := len(db.UniqueCommits)
		commitStr := "commits"
		if commitCount == 1 {
			commitStr = "commit"
		}
		branchColor.Printf("  %s (onto %s) - %d unique %s\n",
			db.Name, db.BaseBranchName, commitCount, commitStr)
	}

	warningColor.Println("\nWARNING: Rebasing will change commit hashes and you will need to force-push to remote branches if they exist.")
	warningColor.Println("Make sure you understand the implications of rebasing published branches.")
	fmt.Println("You may undo the rebase with 'ghenga rebase undo'.")
	fmt.Println("You may continue a paused rebase with 'ghenga rebase continue'.")

	fmt.Print("\nProceed with rebasing? [y/N]: ")

	var response string
	fmt.Scanln(&response)

	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Rebase operation cancelled.")
		return nil
	}

	// Convert BranchInfo to BranchRebaseInfo for state saving
	divergedBranchRebaseInfos := make([]BranchRebaseInfo, len(divergedBranches))
	for i, db := range divergedBranches {
		divergedBranchRebaseInfos[i] = BranchRebaseInfo{
			Name:           db.Name,
			BaseBranchName: db.BaseBranchName,
			UniqueCommits:  db.UniqueCommits,
		}
	}

	// Perform rebases sequentially
	remainingBranchesToRebase := divergedBranchRebaseInfos
	fmt.Println("----------------------------------------")

	var currentTempBranch string // Track the temp branch used for the current branch rebase

	for len(remainingBranchesToRebase) > 0 {
		curBranchInfo := remainingBranchesToRebase[0]

		fmt.Printf("Rebasing '%s' onto '%s'...\n", curBranchInfo.Name, curBranchInfo.BaseBranchName)

		if len(curBranchInfo.UniqueCommits) == 0 {
			fmt.Printf("  No unique commits found for '%s', ensuring it points to '%s'\n", curBranchInfo.Name, curBranchInfo.BaseBranchName)
			updateCmd := exec.Command("git", "branch", "-f", curBranchInfo.Name, curBranchInfo.BaseBranchName)
			updateCmd.Dir = repoPath
			if output, err := updateCmd.CombinedOutput(); err != nil {
				fmt.Printf("  Warning: Failed to force-update branch '%s' to '%s': %v\nOutput:\n%s", curBranchInfo.Name, curBranchInfo.BaseBranchName, err, string(output))
			} else {
				fmt.Printf("  Branch '%s' updated to '%s'\n", curBranchInfo.Name, curBranchInfo.BaseBranchName)
			}
			remainingBranchesToRebase = remainingBranchesToRebase[1:]
			fmt.Println("----------------------------------------")
			continue
		}

		var err error
		currentTempBranch, err = prepareForBranchRebase(repoPath, curBranchInfo.BaseBranchName, curBranchInfo.Name, originalBranch)
		if err != nil {
			return err
		}

		rebaseStatus := applyCommitsAndHandlePause(config, currentTower, repoPath, curBranchInfo, currentTempBranch, originalBranch, remainingBranchesToRebase, 0)

		switch rebaseStatus {
		case errRebasePaused:
			return nil // Rebase paused, state saved by helper
		case nil:
			fmt.Printf("  Successfully applied all %d commits for branch '%s'.\n", len(curBranchInfo.UniqueCommits), curBranchInfo.Name)
			if err := finalizeSuccessfulBranchRebase(repoPath, curBranchInfo.Name, currentTempBranch, originalBranch); err != nil {
				currentTower.RebaseState = nil // Clear potential partial state
				_ = SaveConfig(config)         // Best effort
				return fmt.Errorf("rebase of branch '%s' succeeded, but finalization failed: %w", curBranchInfo.Name, err)
			}
			fmt.Printf("Successfully finished rebase for '%s'\n", curBranchInfo.Name)
			fmt.Println("----------------------------------------")
		default:
			// Unexpected error during cherry-pick (not a pause)
			fmt.Printf("Error during rebase of '%s': %v\n", curBranchInfo.Name, rebaseStatus)
			fmt.Println("Attempting to return to original branch...")
			checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
			checkoutOriginalCmd.Dir = repoPath
			checkoutOriginalCmd.Run() // Best effort
			return fmt.Errorf("failed during rebase of '%s': %w", curBranchInfo.Name, rebaseStatus)
		}

		// Remove the successfully rebased branch from the list for the next iteration
		remainingBranchesToRebase = remainingBranchesToRebase[1:]
	}

	successColor := color.New(color.FgGreen).Add(color.Bold)
	successColor.Println("\nTower rebase completed successfully! Run 'ghenga sync' to update your remote branches.")

	fmt.Printf("Restoring original branch '%s'...\n", originalBranch)
	checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
	checkoutOriginalCmd.Dir = repoPath
	if output, err := checkoutOriginalCmd.CombinedOutput(); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s': %v\nOutput:\n%s", originalBranch, err, string(output))
	}

	// Clear any potential rebase state since we finished successfully
	currentTower.RebaseState = nil
	if err := SaveConfig(config); err != nil {
		fmt.Printf("Warning: Failed to clear rebase state after successful rebase: %v\n", err)
	}

	return nil
}

type RebaseContinueCmd struct{}

func (c *RebaseContinueCmd) Run(_ *kong.Context) error {
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower() // Use existing loader
	if err != nil {
		return err
	}
	if currentTower.RebaseState == nil || !currentTower.RebaseState.IsInProgress {
		return fmt.Errorf("no rebase in progress for tower '%s'", currentTower.Name)
	}
	state := currentTower.RebaseState

	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

	hasConflicts, err := hasConflicts(repoPath)
	if err != nil {
		return err
	}
	if hasConflicts {
		conflictColor := color.New(color.FgRed).Add(color.Bold)
		conflictColor.Println("!! Conflicts still detected.")
		fmt.Println("Please resolve the conflicts and stage the changes ('git add ...').")
		fmt.Println("Then run 'ghenga rebase continue' again.")
		return nil
	}

	// Check if we are on the temporary branch. If not, something is wrong.
	currentGitBranch, err := getCurrentBranchName(gitRepo)
	if err != nil {
		// If HEAD is detached, maybe during conflict resolution? Allow it for now.
		// But if it's on a *different* named branch, that's unexpected.
		if !strings.Contains(err.Error(), "HEAD is detached") {
			return fmt.Errorf("failed to get current branch: %w", err)
		}
		fmt.Println("Warning: HEAD is detached, proceeding with continue operation.")
	} else if currentGitBranch != state.TemporaryBranch {
		// If we are on the target branch, it might mean the user aborted and checked it out.
		// This state is ambiguous. Let's prevent continuation for now.
		if currentGitBranch == state.TargetBranch {
			return fmt.Errorf("currently on target branch '%s', expected temporary branch '%s'. Aborting continue. Did you 'git cherry-pick --abort'?", state.TargetBranch, state.TemporaryBranch)
		}
		// Otherwise, it's some other branch, which is definitely wrong.
		return fmt.Errorf("internal state error: Expected to be on temporary rebase branch '%s', but currently on '%s'. Aborting continue.", state.TemporaryBranch, currentGitBranch)
	}

	// Try to continue the cherry-pick operation
	fmt.Printf("Attempting to continue cherry-pick on branch '%s'...\n", state.TemporaryBranch)
	needsManualCommitHandling := false
	continueCmd := exec.Command("git", "cherry-pick", "--continue")
	continueCmd.Dir = repoPath
	if output, err := continueCmd.CombinedOutput(); err != nil {
		// Check if the error is "no cherry-pick in progress"
		// This might happen if the user resolved conflicts and *committed* manually instead of just staging.
		// Or if they run continue twice after resolving.
		if strings.Contains(string(output), "no cherry-pick or revert in progress") || strings.Contains(string(output), "no cherry-pick in progress") {
			fmt.Println("No cherry-pick operation to continue directly. Assuming conflict was resolved and committed.")
			needsManualCommitHandling = true
			// The commit at state.CurrentCommitIndex was handled manually.
			// We need to start the loop from the *next* index.
		} else {
			// A real error occurred during --continue
			conflictColor := color.New(color.FgRed).Add(color.Bold)
			conflictColor.Printf("!! Failed to continue cherry-pick.\n")
			fmt.Printf("Git output:\n%s\n", string(output))
			conflictColor.Println("There might still be unresolved conflicts, or the commit failed.")
			fmt.Println("Please check 'git status', resolve any issues, stage changes, and run 'ghenga rebase continue' again.")
			return nil // Informational exit, state remains paused.
		}
	} else {
		fmt.Println("Successfully continued cherry-pick.")
		// The commit at state.CurrentCommitIndex is now successfully applied.
		// Increment the index for the *next* commit to be applied in the loop.
		state.CurrentCommitIndex++
		// Save the incremented index? Yes, ensures we don't retry the commit if continue fails again somehow.
	}

	// Loop through remaining branches and commits
	fmt.Println("----------------------------------------")
	originalBranch := state.OriginalBranch // Get from loaded state

	for len(state.RemainingBranchInfos) > 0 {
		currentBranchInfo := state.RemainingBranchInfos[0] // Info for the branch being processed
		currentTempBranch := state.TemporaryBranch         // Temp branch associated with this attempt
		startIndex := state.CurrentCommitIndex             // Where the *previous* attempt left off

		// If we detected a manual commit, the commit at startIndex was handled.
		// We must start applying from the *next* commit.
		if needsManualCommitHandling {
			startIndex++
			needsManualCommitHandling = false // Reset flag for subsequent branches/loops
		}

		fmt.Printf("Continuing rebase for branch '%s' onto '%s'...\n", currentBranchInfo.Name, currentBranchInfo.BaseBranchName)

		// Apply remaining commits for this branch
		rebaseStatus := applyCommitsAndHandlePause(config, currentTower, repoPath, currentBranchInfo, currentTempBranch, originalBranch, state.RemainingBranchInfos, startIndex)

		switch rebaseStatus {
		case errRebasePaused:
			return nil // Rebase paused, state saved by helper
		case nil:
			// Success for this branch
			fmt.Printf("  Successfully applied all remaining commits for branch '%s'.\n", currentBranchInfo.Name)
			if err := finalizeSuccessfulBranchRebase(repoPath, currentBranchInfo.Name, currentTempBranch, originalBranch); err != nil {
				state.IsInProgress = false // Mark rebase as failed/aborted
				_ = SaveConfig(config)     // Attempt to save cancellation state
				return fmt.Errorf("continue succeeded for branch '%s', but finalization failed: %w", currentBranchInfo.Name, err)
			}
			fmt.Printf("Successfully finished rebase for '%s'\n", currentBranchInfo.Name)
			fmt.Println("----------------------------------------")
		default:
			// Unexpected error during cherry-pick (not a pause)
			fmt.Printf("Error during rebase continue for '%s': %v\n", currentBranchInfo.Name, rebaseStatus)
			fmt.Println("Attempting to return to original branch...")
			checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
			checkoutOriginalCmd.Dir = repoPath
			checkoutOriginalCmd.Run()
			state.IsInProgress = false
			_ = SaveConfig(config) // Best effort
			return fmt.Errorf("failed during rebase continue for '%s': %w", currentBranchInfo.Name, rebaseStatus)
		}

		// Move to the next branch in the state
		state.RemainingBranchInfos = state.RemainingBranchInfos[1:]
		state.CurrentCommitIndex = 0 // Reset commit index for the next branch

		if len(state.RemainingBranchInfos) == 0 {
			// All branches processed!
			break // Exit the branch loop
		}

		// Prepare for the *next* branch rebase
		nextBranchInfo := state.RemainingBranchInfos[0]
		fmt.Printf("Preparing to rebase next branch '%s' onto '%s'...\n", nextBranchInfo.Name, nextBranchInfo.BaseBranchName)
		state.TargetBranch = nextBranchInfo.Name // Update state for consistency
		state.BaseBranch = nextBranchInfo.BaseBranchName
		state.RemainingCommits = nextBranchInfo.UniqueCommits // Update state

		newTempBranch, err := prepareForBranchRebase(repoPath, nextBranchInfo.BaseBranchName, nextBranchInfo.Name, originalBranch)
		if err != nil {
			state.IsInProgress = false // Mark as failed
			_ = SaveConfig(config)
			return fmt.Errorf("failed to prepare for next branch '%s': %w", nextBranchInfo.Name, err)
		}
		state.TemporaryBranch = newTempBranch // IMPORTANT: Update state with the *new* temp branch

		// Save state before starting the cherry-picks for the next branch
		if err := SaveConfig(config); err != nil {
			// Don't stop the whole process, but warn
			fmt.Printf("Warning: failed to save rebase state before processing next branch: %v\n", err)
		}
		// The outer loop will now continue with the next branch
	}

	successColor := color.New(color.FgGreen).Add(color.Bold)
	successColor.Println("\nTower rebase completed successfully!")

	// Restore the original branch
	fmt.Printf("Restoring original branch '%s'...\n", state.OriginalBranch)
	checkoutOriginalCmd := exec.Command("git", "checkout", state.OriginalBranch)
	checkoutOriginalCmd.Dir = repoPath
	if output, err := checkoutOriginalCmd.CombinedOutput(); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s': %v\nOutput:\n%s", state.OriginalBranch, err, string(output))
	}

	// Clear the rebase state
	fmt.Println("Clearing rebase state...")
	currentTower.RebaseState = nil
	if err := SaveConfig(config); err != nil {
		fmt.Printf("Warning: Failed to clear rebase state after successful rebase: %v\n", err)
	}
	return nil
}

// --- Helper Functions ---

// Specific error to indicate a pause request
var errRebasePaused = fmt.Errorf("rebase paused due to conflict")

// prepareForBranchRebase checks out the base branch and creates a new temporary branch for cherry-picking.
func prepareForBranchRebase(repoPath, baseBranchName, targetBranchName, originalBranch string) (string, error) {
	fmt.Printf("  Checking out base '%s'...\n", baseBranchName)
	checkoutBaseCmd := exec.Command("git", "checkout", baseBranchName)
	checkoutBaseCmd.Dir = repoPath
	if output, err := checkoutBaseCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to checkout base branch '%s': %w\nOutput:\n%s", baseBranchName, err, string(output))
	}

	tempBranch := fmt.Sprintf("temp-rebase-%s-%d", targetBranchName, time.Now().UnixNano())
	fmt.Printf("  Creating temporary branch '%s'...\n", tempBranch)
	createTempCmd := exec.Command("git", "checkout", "-b", tempBranch)
	createTempCmd.Dir = repoPath
	if output, err := createTempCmd.CombinedOutput(); err != nil {
		// Attempt to checkout original branch before returning
		checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
		checkoutOriginalCmd.Dir = repoPath
		checkoutOriginalCmd.Run() // Best effort cleanup
		return "", fmt.Errorf("failed to create temporary branch '%s': %w\nOutput:\n%s", tempBranch, err, string(output))
	}
	return tempBranch, nil
}

// applyCommitsAndHandlePause performs the cherry-pick loop for a given branch.
// It saves state and returns errRebasePaused if a conflict occurs.
func applyCommitsAndHandlePause(config *Config, tower *Tower, repoPath string, branchInfo BranchRebaseInfo, tempBranch, originalBranch string, remainingBranchInfos []BranchRebaseInfo, startIndex int) error {
	for i := startIndex; i < len(branchInfo.UniqueCommits); i++ {
		commit := branchInfo.UniqueCommits[i]
		fmt.Printf("  Applying commit %s (%d/%d)...\n", commit[:7], i+1, len(branchInfo.UniqueCommits))
		cherryPickCmd := exec.Command("git", "cherry-pick", commit)
		cherryPickCmd.Dir = repoPath
		if output, err := cherryPickCmd.CombinedOutput(); err != nil {
			// CONFLICT / ERROR during cherry-pick
			conflictColor := color.New(color.FgRed).Add(color.Bold)
			conflictColor.Printf("\n!! Cherry-pick failed for commit %s\n", commit)
			fmt.Printf("Git output:\n%s\n", string(output))
			conflictColor.Println("Rebase paused.")
			fmt.Println("Please resolve the conflicts, then stage the changes using 'git add <file>...'.")
			fmt.Println("Once resolved and all files are staged, run 'ghenga rebase continue'.")
			fmt.Println("To abort the rebase, run 'git cherry-pick --abort' and then manually clean up branches if needed.")

			// Save state
			tower.RebaseState = &RebaseState{
				IsInProgress:         true,
				TargetBranch:         branchInfo.Name,
				BaseBranch:           branchInfo.BaseBranchName,
				TemporaryBranch:      tempBranch,
				CurrentCommitIndex:   i,                        // Index of the failed commit
				RemainingCommits:     branchInfo.UniqueCommits, // Commits for the current branch being attempted
				OriginalBranch:       originalBranch,
				RemainingBranchInfos: remainingBranchInfos, // Pass the current list including the one being processed
			}
			if errSave := SaveConfig(config); errSave != nil {
				// This is bad, state might be lost. Return a combined error.
				return fmt.Errorf("cherry-pick failed for commit '%s' AND failed to save rebase state: %w (original git error: %v)", commit, errSave, err)
			}
			return errRebasePaused // Signal pause
		}
		// Cherry-pick success
	}
	return nil // All commits applied successfully
}

// finalizeSuccessfulBranchRebase updates the target branch, checks it out, and deletes the temporary branch.
func finalizeSuccessfulBranchRebase(repoPath, targetBranch, tempBranch, originalBranch string) error {
	fmt.Printf("  Updating branch '%s' to new commit sequence...\n", targetBranch)
	forceUpdateCmd := exec.Command("git", "branch", "-f", targetBranch, tempBranch)
	forceUpdateCmd.Dir = repoPath
	if output, err := forceUpdateCmd.CombinedOutput(); err != nil {
		// Attempt checkout original branch before returning
		checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
		checkoutOriginalCmd.Dir = repoPath
		checkoutOriginalCmd.Run() // Best effort cleanup
		return fmt.Errorf("failed to update branch '%s' from temp branch '%s': %w\nOutput:\n%s", targetBranch, tempBranch, err, string(output))
	}

	// Checkout the updated branch (necessary before deleting temp branch)
	checkoutUpdatedCmd := exec.Command("git", "checkout", targetBranch)
	checkoutUpdatedCmd.Dir = repoPath
	if output, err := checkoutUpdatedCmd.CombinedOutput(); err != nil {
		// Attempt checkout original branch before returning
		checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
		checkoutOriginalCmd.Dir = repoPath
		checkoutOriginalCmd.Run() // Best effort cleanup
		return fmt.Errorf("failed to checkout updated branch '%s': %w\nOutput:\n%s", targetBranch, err, string(output))
	}

	fmt.Printf("  Cleaning up temporary branch '%s'...\n", tempBranch)
	deleteTempCmd := exec.Command("git", "branch", "-D", tempBranch)
	deleteTempCmd.Dir = repoPath
	if output, err := deleteTempCmd.CombinedOutput(); err != nil {
		fmt.Printf("Warning: Failed to delete temporary branch '%s': %v\nOutput:\n%s", tempBranch, err, string(output))
		// Don't fail the whole operation for this.
	}
	return nil
}

// Helper function: hasConflicts checks git status for unmerged paths
func hasConflicts(repoPath string) (bool, error) {
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repoPath
	output, err := statusCmd.Output()
	if err != nil {
		// Check if the error is because we are mid-rebase (often non-zero exit code)
		// but still want to parse the output for 'U' markers.
		// If no output AND error, then it's likely a real error.
		if len(output) == 0 {
			return false, fmt.Errorf("failed to get git status: %w", err)
		}
		// Otherwise, proceed to parse the output even if exit code was non-zero
	}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		// Look for standard conflict markers (Unmerged) or Added/Deleted by both
		if len(line) >= 2 && (line[0] == 'U' || (line[0] == 'A' && line[1] == 'A') || (line[0] == 'D' && line[1] == 'D') || (line[0] == 'R' && line[1] == 'U') || (line[0] == 'U' && line[1] == 'R')) {
			return true, nil
		}
	}
	return false, nil
}

type RebaseUndoCmd struct {
}

func (r *RebaseUndoCmd) Run(_ *kong.Context) error {
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		// If the error is specifically about rebase state, offer to clear it?
		// No, undo should work independently of paused state.
		return err
	}

	// Check if a rebase is *paused* - undoing while paused is confusing.
	if currentTower.RebaseState != nil && currentTower.RebaseState.IsInProgress {
		return fmt.Errorf("a rebase operation is currently paused. Please either complete it using 'ghenga rebase continue' or abort it ('git cherry-pick --abort') before attempting to undo the *previous* completed rebase")
	}

	// Check Git Status first
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repoPath
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		return fmt.Errorf("working directory is not clean. Please commit or stash your changes before undoing rebase")
	}

	if currentTower.LastRebased == "" {
		return fmt.Errorf("no previous completed rebase found to undo for tower '%s'", currentTower.Name)
	}

	branchesToRestore := 0
	for _, branch := range currentTower.Branches {
		if branch.LastReflogID != "" {
			branchesToRestore++
		}
	}

	if branchesToRestore == 0 {
		// This might happen if the save failed after setting LastRebased timestamp
		fmt.Printf("Warning: Found rebase timestamp (%s) but no branch states to restore in tower '%s'. Clearing timestamp.\n", currentTower.LastRebased, currentTower.Name)
		currentTower.LastRebased = ""
		if err := SaveConfig(config); err != nil {
			return fmt.Errorf("failed to clear inconsistent rebase timestamp: %w", err)
		}
		return fmt.Errorf("no branch states found to restore in tower '%s'", currentTower.Name)
	}

	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

	warningColor := color.New(color.FgRed).Add(color.Bold)

	warningColor.Println("\nWARNING: Undoing a rebase will reset branches to their state before the last completed rebase.")
	warningColor.Printf("This will attempt to restore %d branches using stored commit hashes from %s.\n",
		branchesToRestore, currentTower.LastRebased)
	warningColor.Println("Any changes made after the rebase will be lost.")

	fmt.Print("\nDo you want to proceed with undoing the last rebase? [y/N]: ")

	var response string
	fmt.Scanln(&response)

	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Undo operation cancelled.")
		return nil
	}

	originalBranch, err := getCurrentBranchName(gitRepo)
	if err != nil {
		fmt.Printf("Warning: Could not determine current branch: %v\n", err)
		originalBranch = "HEAD" // Fallback
	}

	fmt.Println("Attempting to restore branches...")
	// For each branch in the tower, try to restore it using its stored reflog ID
	restoredCount := 0
	failedCount := 0
	// Iterate backwards to handle potential dependencies? Maybe not necessary here.
	for i := len(currentTower.Branches) - 1; i >= 0; i-- {
		branch := &currentTower.Branches[i]

		if branch.LastReflogID == "" {
			continue
		}

		fmt.Printf("  Restoring branch '%s' to commit %s...\n", branch.Name, branch.LastReflogID[:7])

		branchRefName := plumbing.NewBranchReferenceName(branch.Name)
		_, err := gitRepo.Reference(branchRefName, true)
		branchExists := err == nil

		var cmd *exec.Cmd
		targetCommit := branch.LastReflogID
		// Verify the target commit exists before trying to use it
		_, errCommit := gitRepo.CommitObject(plumbing.NewHash(targetCommit))
		if errCommit != nil {
			fmt.Printf("  Warning: Cannot restore branch '%s': saved commit %s not found in repository.\n", branch.Name, targetCommit)
			failedCount++
			branch.LastReflogID = "" // Clear invalid reflog ID
			continue
		}

		if branchExists {
			cmd = exec.Command("git", "update-ref", branchRefName.String(), branch.LastReflogID)
		} else {
			cmd = exec.Command("git", "branch", branch.Name, branch.LastReflogID)
		}
		cmd.Dir = repoPath

		if output, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("  Warning: Failed to restore branch '%s': %v\nOutput:\n%s", branch.Name, err, string(output))
			failedCount++
		} else {
			fmt.Printf("  Successfully restored branch '%s'\n", branch.Name)
			restoredCount++
			branch.LastReflogID = ""
		}
	}

	currentTower.LastRebased = ""

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	// Restore original branch if possible
	if originalBranch != "HEAD" {
		fmt.Printf("Restoring original branch '%s'...\n", originalBranch)
		checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
		checkoutOriginalCmd.Dir = repoPath
		if output, err := checkoutOriginalCmd.CombinedOutput(); err != nil {
			fmt.Printf("Warning: Failed to checkout original branch '%s': %v\nOutput:\n%s", originalBranch, err, string(output))
		}
	}

	fmt.Printf("\nUndo operation finished. Restored %d branches.\n", restoredCount)
	if failedCount > 0 {
		warningColor.Printf("Failed to restore %d branches (see warnings above).\n", failedCount)
	}
	return nil
}
