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
	From     RebaseFromCmd     `cmd:"from" help:"Rebase [branch] from [commit] onto its base, then continue rebasing all latter branches in the current tower."`
	Onto     RebaseOntoCmd     `cmd:"onto" help:"Rebase the entire tower onto a new base branch or commit"`
	Undo     RebaseUndoCmd     `cmd:"undo" help:"Undo the last rebase operation for the current tower"`
	Continue RebaseContinueCmd `cmd:"continue" help:"Continue a paused rebase operation after resolving conflicts"`
	Cancel   RebaseCancelCmd   `cmd:"cancel" help:"Cancel an in-progress rebase operation"`
}

type RebaseDoCmd struct {
}

func (r *RebaseDoCmd) Run(_ *kong.Context) error {
	err := rebaseTower(false, "", "")
	if err == errRebasePaused {
		return nil // Successfully paused - return success to CLI
	}
	return err
}

type RebaseFromCmd struct {
	Branch     string `kong:"optional,arg,name='branch',help='Branch to start partial rebase from.'"`
	FromCommit string `kong:"optional,arg,name='commit',help='Short commit hash on the specified branch to rebase from.'"`
}

func (r *RebaseFromCmd) Run(_ *kong.Context) error {
	return rebaseTower(false, r.Branch, r.FromCommit)
}

type RebaseOntoCmd struct {
	NewBase string `arg:"" help:"Branch or commit to rebase the tower onto" predictor:"predictBranches"`
}

func (r *RebaseOntoCmd) Run(_ *kong.Context) error {
	// Basic validation only
	if r.NewBase == "" {
		return fmt.Errorf("new base argument is required")
	}

	// Delegate everything else to rebaseTowerWithMode
	err := rebaseTowerWithMode(RebaseModeReset, false, "", "", r.NewBase, "")
	if err == errRebasePaused {
		fmt.Println("Rebase onto paused due to conflicts. Use 'ghenga rebase continue' to resume after resolving.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to rebase tower onto new base: %w", err)
	}

	fmt.Println("Rebase onto completed successfully!")
	return nil
}

// RebaseMode defines the type of rebase operation
type RebaseMode int

const (
	RebaseModeNormal RebaseMode = iota // Only rebase diverged branches
	RebaseModeReset                    // Rebase all branches in tower order
)

// rebaseTower performs the core logic of rebasing a tower's branches.
// It checks for divergence, saves undo state, asks for confirmation (if skipConfirmation is false),
// and performs the sequential rebase, pausing if conflicts occur.
// If partialRebaseBranchName and partialRebaseCommit are provided, it starts rebasing from that specific commit on that branch.
func rebaseTower(skipConfirmation bool, partialRebaseBranchName string, partialRebaseCommit string) error {
	return rebaseTowerWithMode(RebaseModeNormal, skipConfirmation, partialRebaseBranchName, partialRebaseCommit, "", "")
}

// rebaseTowerWithMode performs the core logic of rebasing a tower's branches with a specified mode.
// resetNewBase is only used when mode is RebaseModeReset
// firstBranchExcludeBase, when set, is used instead of resetNewBase for calculating unique commits of the first branch (for landing)
func rebaseTowerWithMode(mode RebaseMode, skipConfirmation bool, partialRebaseBranchName string, partialRebaseCommit string, resetNewBase string, firstBranchExcludeBase string) error {
	// Check if a rebase is already in progress
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}
	if currentTower.RebaseState != nil && currentTower.RebaseState.IsInProgress {
		return fmt.Errorf("a rebase is already in progress for tower '%s'. Resolve conflicts and run 'ghenga rebase continue' or clear the state with 'ghenga rebase cancel'", currentTower.Name)
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

	if currentTower.Base == "" {
		return fmt.Errorf("tower '%s' has no base branch set. Use 'ghenga base <branch-name>' to set it first", currentTower.Name)
	}
	if len(currentTower.Branches) < 2 && firstBranchExcludeBase == "" {
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

	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

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

	// Determine original branch before starting any operations that might change it
	originalBranch, err := getCurrentBranchName(gitRepo)
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

	var branchesToPlan []BranchInfo // This will be the list of branches to rebase, in order.

	isPartialRebase := partialRebaseBranchName != "" && partialRebaseCommit != ""
	isInvalidPartialArgs := (partialRebaseBranchName != "" && partialRebaseCommit == "") || (partialRebaseBranchName == "" && partialRebaseCommit != "")
	if isInvalidPartialArgs {
		return fmt.Errorf("for partial rebase, both branch name and commit hash must be provided")
	}

	iterStartIndex := 1 // Default: start analyzing from the second branch in the tower
	var resolvedPartialCommitHash plumbing.Hash

	// For reset mode, we analyze all branches starting from the first one
	if mode == RebaseModeReset {
		iterStartIndex = 0
	}

	if isPartialRebase {
		targetBranchIdx := -1
		for i, b := range currentTower.Branches {
			if b.Name == partialRebaseBranchName {
				targetBranchIdx = i
				break
			}
		}
		if targetBranchIdx == -1 {
			return fmt.Errorf("branch '%s' not found in tower '%s'", partialRebaseBranchName, currentTower.Name)
		}
		if targetBranchIdx == 0 { // Cannot rebase the first branch onto "branch below it"
			return fmt.Errorf("cannot start partial rebase from the first branch '%s' of the tower; it has no branch below it", partialRebaseBranchName)
		}

		var err error
		resolvedPartialCommitHash, err = getFullCommitHashForBranch(gitRepo, repoPath, partialRebaseBranchName, partialRebaseCommit)
		if err != nil {
			return fmt.Errorf("failed to resolve commit '%s' on branch '%s': %w", partialRebaseCommit, partialRebaseBranchName, err)
		}
		fmt.Printf("Partial rebase requested: Branch '%s' from commit '%s' (%s)\n", partialRebaseBranchName, partialRebaseCommit, resolvedPartialCommitHash.String()[:7])
		iterStartIndex = targetBranchIdx // Start analysis *from* the target branch for the loop
	}

	firstBranchToRebaseFound := false // Flag to pull in all subsequent branches once the first is identified

	// Handle both normal rebase (divergence-based) and reset mode (all branches)
	if mode == RebaseModeReset {
		// For reset mode, rebase all branches in order
		for i := range currentTower.Branches {
			currentBranchInTower := currentTower.Branches[i]

			// Determine the base for this branch
			var baseBranchName string
			if i == 0 {
				// First branch gets rebased onto the new base
				baseBranchName = resetNewBase
			} else {
				// Subsequent branches get rebased onto the previous branch
				baseBranchName = currentTower.Branches[i-1].Name
			}

			branchRefName := plumbing.NewBranchReferenceName(currentBranchInTower.Name)
			branchHead, err := gitRepo.Reference(branchRefName, true)
			if err != nil {
				fmt.Printf("  Skipping branch '%s' from reset plan: does not exist locally.\n", currentBranchInTower.Name)
				continue
			}

			// For reset mode, get unique commits relative to the target base
			var baseHead plumbing.Hash
			if i == 0 {
				// First branch: get commits relative to resetNewBase or firstBranchExcludeBase
				baseToUse := resetNewBase
				if firstBranchExcludeBase != "" {
					baseToUse = firstBranchExcludeBase
				}
				newBaseRef, err := gitRepo.ResolveRevision(plumbing.Revision(baseToUse))
				if err != nil {
					if firstBranchExcludeBase != "" {
						return fmt.Errorf("failed to resolve exclude base '%s': %w", baseToUse, err)
					}
					return fmt.Errorf("failed to resolve rebase onto base '%s': %w", baseToUse, err)
				}
				baseHead = *newBaseRef
			} else {
				// Subsequent branches: get commits relative to previous branch
				prevBranchRef, err := gitRepo.Reference(plumbing.NewBranchReferenceName(baseBranchName), true)
				if err != nil {
					return fmt.Errorf("failed to get reference for base branch '%s': %w", baseBranchName, err)
				}
				baseHead = prevBranchRef.Hash()
			}

			cmd := exec.Command("git", "rev-list", "--reverse", baseHead.String()+".."+branchHead.Hash().String())
			output, err := cmdOutput(cmd, repoPath, fmt.Sprintf("list unique commits for '%s' in reset mode", currentBranchInTower.Name))
			if err != nil {
				return err
			}
			currentUniqueCommits := parseCommitList(output)

			// Always include branches in reset mode, even if no unique commits
			branchInfo := BranchInfo{
				Index:          i,
				Name:           currentBranchInTower.Name,
				BaseBranchName: baseBranchName,
				UniqueCommits:  currentUniqueCommits,
				IsDiverged:     true, // Always considered "diverged" in reset mode
			}
			branchesToPlan = append(branchesToPlan, branchInfo)
		}
	} else {
		// Original normal rebase logic
		for i := iterStartIndex; i < len(currentTower.Branches); i++ {
			currentBranchInTower := currentTower.Branches[i]
			baseBranchInTower := currentTower.Branches[i-1]

			branchRefName := plumbing.NewBranchReferenceName(currentBranchInTower.Name)
			branchHead, err := gitRepo.Reference(branchRefName, true)
			if err != nil {
				fmt.Printf("  Skipping branch '%s' from rebase plan: does not exist locally.\n", currentBranchInTower.Name)
				continue
			}

			baseRefName := plumbing.NewBranchReferenceName(baseBranchInTower.Name)
			baseHead, err := gitRepo.Reference(baseRefName, true)
			if err != nil {
				fmt.Printf("  Skipping branch '%s' from rebase plan: its base '%s' does not exist locally.\n", currentBranchInTower.Name, baseBranchInTower.Name)
				continue
			}

			branchIsTargetOfPartialRebase := isPartialRebase && currentBranchInTower.Name == partialRebaseBranchName
			currentUniqueCommits := []string{}
			isConsideredDivergedForPlanning := false

			if branchIsTargetOfPartialRebase {
				// Ensure resolvedPartialCommitHash is an ancestor of branchHead.Hash()
				isAncestorCmd := exec.Command("git", "merge-base", "--is-ancestor", resolvedPartialCommitHash.String(), branchHead.Hash().String())
				if _, err := cmdOutput(isAncestorCmd, repoPath, fmt.Sprintf("check ancestry for %s on %s", resolvedPartialCommitHash.String()[:7], currentBranchInTower.Name)); err != nil {
					return fmt.Errorf("commit '%s' (%s) is not an ancestor of the tip of branch '%s'. Cannot perform partial rebase: %w", partialRebaseCommit, resolvedPartialCommitHash.String()[:7], currentBranchInTower.Name, err)
				}

				cmd := exec.Command("git", "rev-list", "--reverse", resolvedPartialCommitHash.String()+"^"+".."+branchHead.Hash().String())
				output, err := cmdOutput(cmd, repoPath, fmt.Sprintf("list partial unique commits for '%s'", currentBranchInTower.Name))
				if err != nil {
					return err
				}
				currentUniqueCommits = parseCommitList(output)
				isConsideredDivergedForPlanning = true // Target of partial rebase is always "diverged" for planning
			} else {
				// Standard divergence check against baseBranchInTower
				mergeBase, err := findMergeBase(gitRepo, branchHead.Hash(), baseHead.Hash())
				if err != nil {
					return fmt.Errorf("failed to find merge base between '%s' and '%s': %w", currentBranchInTower.Name, baseBranchInTower.Name, err)
				}
				if mergeBase != baseHead.Hash() {
					isConsideredDivergedForPlanning = true
				}
				// Standard unique commits: baseBranchInTower.Tip .. currentBranchInTower.Tip
				cmd := exec.Command("git", "rev-list", "--reverse", baseHead.Hash().String()+".."+branchHead.Hash().String())
				output, err := cmdOutput(cmd, repoPath, fmt.Sprintf("list unique commits for '%s'", currentBranchInTower.Name))
				if err != nil {
					return err
				}
				currentUniqueCommits = parseCommitList(output)
			}

			if isConsideredDivergedForPlanning || firstBranchToRebaseFound {
				if !firstBranchToRebaseFound && isConsideredDivergedForPlanning {
					firstBranchToRebaseFound = true
				}

				// If this branch wasn't the target of partial or initially diverged,
				// but a previous one was (firstBranchToRebaseFound = true),
				// we need its standard unique commits (already calculated above if not partial target).
				// This explicit recalculation is only needed if it wasn't isConsideredDivergedForPlanning initially.
				if firstBranchToRebaseFound && !isConsideredDivergedForPlanning && !branchIsTargetOfPartialRebase {
					// This branch is being pulled into rebase due to a predecessor.
					// Its unique commits are already calculated against its original base.
					// We mark it as 'isDiverged' for the BranchInfo struct consistency if it wasn't already.
					isConsideredDivergedForPlanning = true // Effectively, it is part of the rebase chain.
				}

				branchInfo := BranchInfo{
					Index:          i, // Original index in tower
					Name:           currentBranchInTower.Name,
					BaseBranchName: baseBranchInTower.Name, // Its designated base from the tower structure
					UniqueCommits:  currentUniqueCommits,
					IsDiverged:     isConsideredDivergedForPlanning, // Store if it was the trigger or naturally diverged, or pulled in
				}
				branchesToPlan = append(branchesToPlan, branchInfo)
			}
		}
	} // End of normal rebase vs reset mode logic

	if len(branchesToPlan) == 0 {
		if isPartialRebase {
			fmt.Printf("No commits to rebase for branch '%s' from commit '%s'. Ensure the commit is not the tip or already rebased.\n", partialRebaseBranchName, partialRebaseCommit)
		} else {
			fmt.Println("No diverged branches found in the current tower. All branches are up to date.")
		}
		// Clear any potential leftover rebase state
		currentTower.RebaseState = nil
		_ = SaveConfig(config) // Best effort save
		return nil
	}

	branchColor := color.New(color.FgYellow)
	warningColor := color.New(color.FgRed).Add(color.Bold)

	fmt.Printf("Found %d branch(es) needing rebase in tower '%s':\n", len(branchesToPlan), currentTower.Name)
	for _, db := range branchesToPlan {
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

	// Skip confirmation if skipConfirmation is true
	if !skipConfirmation {
		fmt.Print("\nProceed with rebasing? [y/N]: ")

		var response string
		fmt.Scanln(&response)

		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Println("Rebase operation cancelled.")
			return nil
		}
	}

	// Update tower base for reset operation after user confirmation
	if mode == RebaseModeReset && resetNewBase != "" {
		fmt.Printf("Updating tower base from '%s' to '%s'...\n", currentTower.Base, resetNewBase)
		currentTower.Base = resetNewBase
		if err := SaveConfig(config); err != nil {
			return fmt.Errorf("failed to update tower base: %w", err)
		}
	}

	// Convert BranchInfo to BranchRebaseInfo for state saving
	branchRebaseInfos := make([]BranchRebaseInfo, len(branchesToPlan))
	for i, db := range branchesToPlan {
		branchRebaseInfos[i] = BranchRebaseInfo{
			Name:           db.Name,
			BaseBranchName: db.BaseBranchName,
			UniqueCommits:  db.UniqueCommits,
		}
	}

	// Perform rebases sequentially
	remainingBranchesToRebase := branchRebaseInfos
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
			return errRebasePaused // Propagate the pause error to caller
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
			CheckoutBranch(repoPath, originalBranch) // Best effort
			return fmt.Errorf("failed during rebase of '%s': %w", curBranchInfo.Name, rebaseStatus)
		}

		// Remove the successfully rebased branch from the list for the next iteration
		remainingBranchesToRebase = remainingBranchesToRebase[1:]
	}

	successColor := color.New(color.FgGreen).Add(color.Bold)
	successColor.Println("\nTower rebase completed successfully! Run 'ghenga sync' to update your remote branches.")

	fmt.Printf("Restoring original branch '%s'...\n", originalBranch)
	if err := CheckoutBranch(repoPath, originalBranch); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s': %v\n", originalBranch, err)
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
		return fmt.Errorf("internal state error: Expected to be on temporary rebase branch '%s', but currently on '%s'. This may happen if the rebase was interrupted and the branch was restored. Try 'ghenga rebase cancel' to clear the state, then restart the operation", state.TemporaryBranch, currentGitBranch)
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
			return nil // Successfully paused - return success to CLI
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
			CheckoutBranch(repoPath, originalBranch) // Best effort
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
	if err := CheckoutBranch(repoPath, state.OriginalBranch); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s': %v\n", state.OriginalBranch, err)
	}

	// Clear the rebase state
	fmt.Println("Clearing rebase state...")
	currentTower.RebaseState = nil
	if err := SaveConfig(config); err != nil {
		fmt.Printf("Warning: Failed to clear rebase state after successful rebase: %v\n", err)
	}
	return nil
}

// RebaseCancelCmd defines the command for cancelling an in-progress rebase.
type RebaseCancelCmd struct{}

// Run executes the logic to cancel an in-progress rebase.
func (c *RebaseCancelCmd) Run(_ *kong.Context) error {
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	if currentTower.RebaseState == nil || !currentTower.RebaseState.IsInProgress {
		fmt.Printf("No rebase operation in progress for tower '%s'. Nothing to cancel.\n", currentTower.Name)
		return nil
	}

	fmt.Println("Cancelling in-progress rebase operation...")

	// Attempt to abort any ongoing git operation (e.g., cherry-pick)
	// Check if we are in a cherry-pick sequence first.
	// We can infer this if state.TemporaryBranch is set and potentially state.CurrentCommitIndex > 0 or specific git files exist.
	// A simple `git cherry-pick --abort` is generally safe to run even if not strictly in a cherry-pick.
	// Similarly, `git rebase --abort` could be considered if the rebase mechanism was different.
	// Given our current implementation uses cherry-pick:
	abortCmd := exec.Command("git", "cherry-pick", "--abort")
	abortCmd.Dir = repoPath
	if output, err := abortCmd.CombinedOutput(); err != nil {
		// Not a fatal error if abort fails (e.g., no cherry-pick in progress), but worth noting.
		fmt.Printf("  Note: 'git cherry-pick --abort' failed or had nothing to abort: %v\nOutput:\n%s\n", err, string(output))
		// We might also be in a state where `git rebase --abort` is the correct command if something went very wrong,
		// but ghenga's rebase is cherry-pick based. For now, we'll stick to cherry-pick abort.
	} else {
		fmt.Println("  Successfully aborted git cherry-pick operation.")
	}

	originalBranch := currentTower.RebaseState.OriginalBranch
	temporaryBranchToDelete := currentTower.RebaseState.TemporaryBranch // Store before clearing

	// Clear the rebase state from the configuration
	currentTower.RebaseState = nil
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to clear rebase state in configuration: %w", err)
	}
	fmt.Println("  Cleared ghenga rebase state from configuration.")

	// Attempt to checkout the original branch
	if originalBranch != "" {
		fmt.Printf("  Attempting to checkout original branch '%s'...\n", originalBranch)
		if err := CheckoutBranch(repoPath, originalBranch); err != nil {
			fmt.Printf("  Warning: Failed to checkout original branch '%s': %v\n", originalBranch, err)
			fmt.Println("  You may need to manually checkout your desired branch.")
		} else {
			fmt.Printf("  Successfully checked out branch '%s'.\n", originalBranch)
		}
	} else {
		fmt.Println("  No original branch recorded in rebase state. Please checkout your desired branch manually.")
	}

	// Clean up any temporary branches if one was recorded and still exists
	if temporaryBranchToDelete != "" {
		fmt.Printf("  Attempting to delete temporary branch '%s'...\n", temporaryBranchToDelete)
		deleteTempCmd := exec.Command("git", "branch", "-D", temporaryBranchToDelete)
		deleteTempCmd.Dir = repoPath
		if output, err := deleteTempCmd.CombinedOutput(); err != nil {
			// This is not a critical failure, as the main goal (cancelling rebase state) is achieved.
			fmt.Printf("  Warning: Failed to delete temporary branch '%s': %v\nOutput:\n%s", temporaryBranchToDelete, err, string(output))
		} else {
			fmt.Printf("  Successfully deleted temporary branch '%s'.\n", temporaryBranchToDelete)
		}
	}

	fmt.Println("Rebase operation cancelled.")
	return nil
}

// --- Helper Functions ---

// Specific error to indicate a pause request
var errRebasePaused = fmt.Errorf("rebase paused due to conflict")

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

// prepareForBranchRebase checks out the base branch and creates a new temporary branch for cherry-picking.
func prepareForBranchRebase(repoPath, baseBranchName, targetBranchName, originalBranch string) (string, error) {
	fmt.Printf("  Checking out base '%s'...\n", baseBranchName)
	if err := checkoutBranchOrCommit(repoPath, baseBranchName); err != nil {
		return "", fmt.Errorf("failed to checkout base branch '%s': %w", baseBranchName, err)
	}

	tempBranch := fmt.Sprintf("temp-rebase-%s-%d", targetBranchName, time.Now().UnixNano())
	fmt.Printf("  Creating temporary branch '%s'...\n", tempBranch)
	createTempCmd := exec.Command("git", "checkout", "-b", tempBranch)
	createTempCmd.Dir = repoPath
	if output, err := createTempCmd.CombinedOutput(); err != nil {
		// Attempt to checkout original branch before returning (best effort)
		CheckoutBranch(repoPath, originalBranch)
		return "", fmt.Errorf("failed to create temporary branch '%s': %w\nOutput:\n%s", tempBranch, err, string(output))
	}
	return tempBranch, nil
}

// isGitLockFileError checks if the error output indicates a git lock file issue
func isGitLockFileError(output string) bool {
	return strings.Contains(output, "index.lock") &&
		(strings.Contains(output, "File exists") || strings.Contains(output, "Another git process"))
}

// cherryPickWithRetry attempts to cherry-pick a commit with retry logic for lock file errors
func cherryPickWithRetry(repoPath, commit string) ([]byte, error) {
	maxRetries := 10
	baseSleepMs := 100

	for attempt := 0; attempt <= maxRetries; attempt++ {
		cherryPickCmd := exec.Command("git", "cherry-pick", commit)
		cherryPickCmd.Dir = repoPath
		output, err := cherryPickCmd.CombinedOutput()

		if err == nil {
			return output, nil // Success
		}

		// Check if this is a lock file error and we haven't exhausted retries
		if attempt < maxRetries && isGitLockFileError(string(output)) {
			sleepMs := baseSleepMs * (1 << attempt) // Exponential backoff
			if sleepMs > 2000 {
				sleepMs = 2000 // Cap at 2 seconds
			}

			fmt.Printf("    Git lock file detected, retrying in %dms (attempt %d/%d)...\n",
				sleepMs, attempt+1, maxRetries+1)
			time.Sleep(time.Duration(sleepMs) * time.Millisecond)
			continue
		}

		// Either not a lock file error, or we've exhausted retries
		if isGitLockFileError(string(output)) {
			return output, fmt.Errorf("git lock file persisted after %d retries: %w", maxRetries+1, err)
		}

		return output, err // Return original error
	}

	// Should never reach here, but just in case
	return nil, fmt.Errorf("unexpected error in cherry-pick retry logic")
}

// applyCommitsAndHandlePause performs the cherry-pick loop for a given branch.
// It saves state and returns errRebasePaused if a conflict occurs.
func applyCommitsAndHandlePause(config *Config, tower *Tower, repoPath string, branchInfo BranchRebaseInfo, tempBranch, originalBranch string, remainingBranchInfos []BranchRebaseInfo, startIndex int) error {
	for i := startIndex; i < len(branchInfo.UniqueCommits); i++ {
		commit := branchInfo.UniqueCommits[i]
		fmt.Printf("  Applying commit %s (%d/%d)...\n", commit[:7], i+1, len(branchInfo.UniqueCommits))

		output, err := cherryPickWithRetry(repoPath, commit)
		if err != nil {
			// CONFLICT / ERROR during cherry-pick
			conflictColor := color.New(color.FgRed).Add(color.Bold)
			conflictColor.Printf("\n!! Cherry-pick failed for commit %s\n", commit)
			fmt.Printf("Git output:\n%s\n", string(output))

			// Provide different guidance based on error type
			if isGitLockFileError(string(output)) {
				conflictColor.Println("Git lock file error persisted after retries.")
				fmt.Println("Another git process may be running or crashed. Check for:")
				fmt.Println("  - Other git commands running in this repository")
				fmt.Println("  - IDE/editor git operations in progress")
				fmt.Println("  - If all else fails, manually remove .git/index.lock")
			} else {
				conflictColor.Println("Rebase paused.")
				fmt.Println("Please resolve the conflicts, then stage the changes using 'git add <file>...'.")
			}

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
		// Attempt checkout original branch before returning (best effort)
		CheckoutBranch(repoPath, originalBranch)
		return fmt.Errorf("failed to update branch '%s' from temp branch '%s': %w\nOutput:\n%s", targetBranch, tempBranch, err, string(output))
	}

	// Checkout the updated branch (necessary before deleting temp branch)
	if err := CheckoutBranch(repoPath, targetBranch); err != nil {
		// Attempt checkout original branch before returning (best effort)
		CheckoutBranch(repoPath, originalBranch)
		return fmt.Errorf("failed to checkout updated branch '%s': %w", targetBranch, err)
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

	// Determine original branch before attempting to restore other branches
	originalBranch, err := getCurrentBranchName(gitRepo)
	if err != nil {
		fmt.Printf("Warning: Could not determine current branch during undo: %v\n", err)
		// Attempt to get HEAD as a fallback, but it might not be a branch name
		headRef, headErr := gitRepo.Head()
		if headErr == nil && headRef != nil && headRef.Name().IsBranch() {
			originalBranch = headRef.Name().Short()
		} else {
			originalBranch = "HEAD" // Fallback, may not be a checkoutable branch
		}
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
		if err := CheckoutBranch(repoPath, originalBranch); err != nil {
			fmt.Printf("Warning: Failed to checkout original branch '%s': %v\n", originalBranch, err)
		}
	}

	fmt.Printf("\nUndo operation finished. Restored %d branches.\n", restoredCount)
	if failedCount > 0 {
		warningColor := color.New(color.FgRed).Add(color.Bold)
		warningColor.Printf("Failed to restore %d branches (see warnings above).\n", failedCount)
	}
	return nil
}

// cmdOutput executes a command and returns its stdout or an error.
func cmdOutput(cmd *exec.Cmd, dir string, actionDesc string) (string, error) {
	cmd.Dir = dir
	outputBytes, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		if stderr != "" {
			return "", fmt.Errorf("failed to %s: %w\nStderr: %s", actionDesc, err, stderr)
		}
		return "", fmt.Errorf("failed to %s: %w", actionDesc, err)
	}
	return string(outputBytes), nil
}

// parseCommitList splits a string of newline-separated commit hashes into a slice.
func parseCommitList(output string) []string {
	commits := strings.Split(strings.TrimSpace(string(output)), "\n")
	filteredCommits := []string{}
	for _, c := range commits {
		if c != "" {
			filteredCommits = append(filteredCommits, c)
		}
	}
	return filteredCommits
}

// getFullCommitHashForBranch resolves a short or full commit hash string to a full plumbing.Hash,
// ensuring the commit exists on the specified branch.
func getFullCommitHashForBranch(repo *git.Repository, repoPath string, branchName string, commitHashStr string) (plumbing.Hash, error) {
	// 1. Resolve the commitHashStr to a full plumbing.Hash (could be on any branch initially)
	fullHash, err := repo.ResolveRevision(plumbing.Revision(commitHashStr))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("could not resolve commit hash '%s': %w", commitHashStr, err)
	}

	// 2. Get the tip of the branch
	branchRefName := plumbing.NewBranchReferenceName(branchName)
	branchHeadRef, err := repo.Reference(branchRefName, true)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("could not get reference for branch '%s': %w", branchName, err)
	}

	// 3. Verify the resolved commit is an ancestor of the branch's tip using `git merge-base --is-ancestor`
	cmd := exec.Command("git", "merge-base", "--is-ancestor", fullHash.String(), branchHeadRef.Hash().String())
	// repoPath is needed if the git.Repository object doesn't have its worktree correctly set for cmd.Dir
	// Assuming repoPath is the root of the worktree.
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		// cmd.Run() returns ExitError if command exits non-zero.
		return plumbing.ZeroHash, fmt.Errorf("commit '%s' (%s) is not an ancestor of branch '%s' tip (%s)", commitHashStr, fullHash.String()[:7], branchName, branchHeadRef.Hash().String()[:7])
	}

	return *fullHash, nil
}
