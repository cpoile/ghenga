package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/fatih/color"
	"github.com/go-git/go-git/v5/plumbing"
)

// errMergePaused signals that a merge stopped on a conflict and the user must
// resolve it and run 'ghenga merge continue'. It mirrors errRebasePaused.
var errMergePaused = fmt.Errorf("merge paused due to conflict")

// MergeMode mirrors RebaseMode: Normal only touches branches that have diverged
// from their base; Reset merges the base down into every branch in tower order
// (used by 'merge onto' and by 'land' under the merge strategy).
type MergeMode int

const (
	MergeModeNormal MergeMode = iota
	MergeModeReset
)

// ensureTowerStrategy returns an error if the current tower is not using the
// wanted strategy, pointing the user at the command that matches its strategy.
func ensureTowerStrategy(want string) error {
	_, _, currentTower, _, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}
	got := currentTower.strategy()
	if got == want {
		return nil
	}
	if got == StrategyMerge {
		return fmt.Errorf("tower '%s' uses the 'merge' strategy. Use 'ghenga merge'", currentTower.Name)
	}
	return fmt.Errorf("tower '%s' uses the 'rebase' strategy. Use 'ghenga rebase'", currentTower.Name)
}

type MergeCmd struct {
	Do       MergeDoCmd       `cmd:"" default:"1" hidden:"" help:"Merge each tower branch's base down into it for branches that have diverged"`
	From     MergeFromCmd     `cmd:"from" help:"Merge the base down through the tower starting at [branch]."`
	Onto     MergeOntoCmd     `cmd:"onto" help:"Merge a new base down through the entire tower"`
	Undo     MergeUndoCmd     `cmd:"undo" help:"Undo the last merge operation for the current tower"`
	Continue MergeContinueCmd `cmd:"continue" help:"Continue a paused merge operation after resolving conflicts"`
	Cancel   MergeCancelCmd   `cmd:"cancel" help:"Cancel an in-progress merge operation"`
}

type MergeDoCmd struct{}

func (m *MergeDoCmd) Run(_ *kong.Context) error {
	if err := ensureTowerStrategy(StrategyMerge); err != nil {
		return err
	}
	err := mergeTowerWithMode(MergeModeNormal, false, "", "")
	if err == errMergePaused {
		return nil // Successfully paused - return success to CLI
	}
	return err
}

type MergeContinueCmd struct{}

func (m *MergeContinueCmd) Run(_ *kong.Context) error {
	if err := ensureTowerStrategy(StrategyMerge); err != nil {
		return err
	}

	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}
	if currentTower.MergeState == nil || !currentTower.MergeState.IsInProgress {
		return fmt.Errorf("no merge in progress for tower '%s'", currentTower.Name)
	}
	state := currentTower.MergeState

	// Check if conflicts are still present.
	stillConflicted, err := hasConflicts(repoPath)
	if err != nil {
		return err
	}
	if stillConflicted {
		conflictColor := color.New(color.FgRed).Add(color.Bold)
		conflictColor.Println("!! Conflicts still detected.")
		fmt.Println("Please resolve the merge conflicts, then stage the changes ('git add ...').")
		fmt.Println("Then run 'ghenga merge continue' again.")
		return nil
	}

	// Finish the paused merge. The user may have:
	//   (a) staged the resolved files and not yet committed, in which case
	//       'git merge --continue' will finalize the merge commit, or
	//   (b) manually created the merge commit already, in which case
	//       'git merge --continue' will say "no merge in progress" — we detect
	//       this and treat it as a success the same way rebase does.
	fmt.Printf("Finishing merge of '%s' into '%s'...\n", state.BaseBranch, state.TargetBranch)
	continueOutput, continueErr := runGitCommandWithRetry(repoPath, "merge", "--continue")
	needsManualCommitHandling := false
	if continueErr != nil {
		outStr := string(continueOutput)
		if strings.Contains(outStr, "no merge in progress") ||
			strings.Contains(outStr, "not currently merging") ||
			strings.Contains(outStr, "MERGE_HEAD missing") {
			// The user committed the merge manually — that's fine.
			fmt.Println("No merge in progress; assuming conflict was resolved and committed manually.")
			needsManualCommitHandling = true
		} else {
			conflictColor := color.New(color.FgRed).Add(color.Bold)
			conflictColor.Printf("!! Failed to continue merge.\nGit output:\n%s\n", outStr)
			fmt.Println("Please check 'git status', resolve any issues, stage changes, and run 'ghenga merge continue' again.")
			return nil
		}
	}

	if !needsManualCommitHandling {
		fmt.Printf("  Merge commit created for '%s'.\n", state.TargetBranch)
	}

	// Advance the real branch pointer to HEAD (the merge commit just created or
	// already committed manually), then continue with remaining branches.
	if err := updateRefToHead(repoPath, state.TargetBranch); err != nil {
		return err
	}
	fmt.Printf("  Updated branch '%s' to new merge commit.\n", state.TargetBranch)

	// Remove the just-completed branch from RemainingBranches and proceed.
	state.RemainingBranches = state.RemainingBranches[1:]

	originalBranch := state.OriginalBranch
	mainRepoBranch := state.MainRepoBranch

	if _, err := openGitRepo(); err != nil {
		return err
	}

	for len(state.RemainingBranches) > 0 {
		next := state.RemainingBranches[0]
		fmt.Println("----------------------------------------")
		fmt.Printf("Merging '%s' into '%s'...\n", next.BaseBranchName, next.Name)

		if err := checkoutCommitDetached(repoPath, next.Name); err != nil {
			state.IsInProgress = false
			_ = SaveConfig(config)
			return fmt.Errorf("failed to checkout '%s' (detached): %w", next.Name, err)
		}

		mergeOutput, mergeErr := runGitCommandWithRetry(repoPath, "merge", "--no-edit", next.BaseBranchName)
		if mergeErr != nil {
			conflictColor := color.New(color.FgRed).Add(color.Bold)
			conflictColor.Printf("\n!! Merge conflict merging '%s' into '%s'\n", next.BaseBranchName, next.Name)
			fmt.Printf("Git output:\n%s\n", string(mergeOutput))
			fmt.Printf("\nWorking directory: %s\n", repoPath)
			fmt.Println("Please resolve the conflicts, stage the changes, and run 'ghenga merge continue'.")

			state.TargetBranch = next.Name
			state.BaseBranch = next.BaseBranchName
			if err := SaveConfig(config); err != nil {
				return fmt.Errorf("merge conflict AND failed to save state: %w", err)
			}
			return errMergePaused
		}

		if err := updateRefToHead(repoPath, next.Name); err != nil {
			return err
		}
		fmt.Printf("  Updated branch '%s' to new merge commit.\n", next.Name)
		state.RemainingBranches = state.RemainingBranches[1:]
		if err := SaveConfig(config); err != nil {
			fmt.Printf("Warning: failed to save state after merging '%s': %v\n", next.Name, err)
		}
	}

	fmt.Println("----------------------------------------")
	successColor := color.New(color.FgGreen).Add(color.Bold)
	successColor.Println("\nTower merge completed successfully! Run 'ghenga sync' to update your remote branches.")

	// Prompt to reset worktrees that were part of this operation.
	worktreeBranches := detectWorktreeBranches(repoPath, currentTower.Branches)
	mergedWorktrees := filterWorktreesByNames(worktreeBranches, state.MergedBranches)
	promptAndResetWorktrees(mergedWorktrees)

	// Restore the main repo branch.
	if err := restoreRepoBranch(repoPath, mainRepoBranch, originalBranch); err != nil {
		fmt.Printf("Warning: %v\n", err)
	}

	// Clear merge state.
	currentTower.MergeState = nil
	if err := SaveConfig(config); err != nil {
		fmt.Printf("Warning: failed to clear merge state: %v\n", err)
	}
	return nil
}

type MergeCancelCmd struct{}

func (m *MergeCancelCmd) Run(_ *kong.Context) error {
	if err := ensureTowerStrategy(StrategyMerge); err != nil {
		return err
	}

	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}
	if currentTower.MergeState == nil || !currentTower.MergeState.IsInProgress {
		fmt.Printf("No merge operation in progress for tower '%s'. Nothing to cancel.\n", currentTower.Name)
		return nil
	}

	fmt.Println("Cancelling in-progress merge operation...")

	// Abort the in-progress git merge (safe to run even if no merge is active).
	abortOutput, abortErr := runGitCommandWithRetry(repoPath, "merge", "--abort")
	if abortErr != nil {
		fmt.Printf("  Note: 'git merge --abort' failed or had nothing to abort: %v\nOutput:\n%s\n",
			abortErr, string(abortOutput))
	} else {
		fmt.Println("  Successfully aborted git merge operation.")
	}

	originalBranch := currentTower.MergeState.OriginalBranch
	mainRepoBranch := currentTower.MergeState.MainRepoBranch

	// Clear the merge state.
	currentTower.MergeState = nil
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to clear merge state in configuration: %w", err)
	}
	fmt.Println("  Cleared ghenga merge state from configuration.")

	// Restore the main repo branch (or the original branch as fallback).
	if err := restoreRepoBranch(repoPath, mainRepoBranch, originalBranch); err != nil {
		fmt.Printf("  Warning: %v\n", err)
	}

	fmt.Println("Merge operation cancelled.")
	return nil
}

type MergeUndoCmd struct{}

func (m *MergeUndoCmd) Run(_ *kong.Context) error {
	if err := ensureTowerStrategy(StrategyMerge); err != nil {
		return err
	}
	return restoreTowerToReflog(StrategyMerge)
}

// restoreTowerToReflog restores all tower branches that have a non-empty
// LastReflogID back to that hash, then clears the undo bookkeeping. It is
// shared by RebaseUndoCmd and MergeUndoCmd.
//
// strategy is the string used only in user-facing error messages
// ("merge"/"rebase") so the caller does not have to duplicate the guard logic.
func restoreTowerToReflog(strategy string) error {
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	// Block if an operation is paused (undo while paused is ambiguous).
	if strategy == StrategyMerge {
		if currentTower.MergeState != nil && currentTower.MergeState.IsInProgress {
			return fmt.Errorf("a merge operation is currently paused. Complete it with 'ghenga merge continue' or abort it with 'ghenga merge cancel' before undoing")
		}
	} else {
		if currentTower.RebaseState != nil && currentTower.RebaseState.IsInProgress {
			return fmt.Errorf("a rebase operation is currently paused. Please either complete it using 'ghenga rebase continue' or abort it ('git cherry-pick --abort') before attempting to undo the *previous* completed rebase")
		}
	}

	// Require a clean working tree.
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repoPath
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		return fmt.Errorf("working directory '%s' is not clean. Please commit or stash your changes before undoing", repoPath)
	}

	if currentTower.LastRebased == "" {
		return fmt.Errorf("no previous completed %s found to undo for tower '%s'", strategy, currentTower.Name)
	}

	branchesToRestore := 0
	for _, branch := range currentTower.Branches {
		if branch.LastReflogID != "" {
			branchesToRestore++
		}
	}
	if branchesToRestore == 0 {
		fmt.Printf("Warning: found %s timestamp (%s) but no branch states to restore in tower '%s'. Clearing timestamp.\n",
			strategy, currentTower.LastRebased, currentTower.Name)
		currentTower.LastRebased = ""
		if err := SaveConfig(config); err != nil {
			return fmt.Errorf("failed to clear inconsistent timestamp: %w", err)
		}
		return fmt.Errorf("no branch states found to restore in tower '%s'", currentTower.Name)
	}

	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

	originalBranch, err := getCurrentBranchName(gitRepo)
	if err != nil {
		fmt.Printf("Warning: Could not determine current branch during undo: %v\n", err)
		headRef, headErr := getHead(gitRepo)
		if headErr == nil && headRef != nil && headRef.Name().IsBranch() {
			originalBranch = headRef.Name().Short()
		} else {
			originalBranch = "HEAD"
		}
	}

	fmt.Println("Attempting to restore branches...")
	restoredCount := 0
	failedCount := 0

	for i := len(currentTower.Branches) - 1; i >= 0; i-- {
		branch := &currentTower.Branches[i]
		if branch.LastReflogID == "" {
			continue
		}

		fmt.Printf("  Restoring branch '%s' to commit %s...\n", branch.Name, branch.LastReflogID[:7])

		// Verify the target commit still exists.
		verifyCmd := exec.Command("git", "cat-file", "-t", branch.LastReflogID)
		verifyCmd.Dir = repoPath
		if err := verifyCmd.Run(); err != nil {
			fmt.Printf("  Warning: Cannot restore branch '%s': saved commit %s not found in repository.\n",
				branch.Name, branch.LastReflogID)
			failedCount++
			branch.LastReflogID = ""
			continue
		}

		branchRefName := plumbing.NewBranchReferenceName(branch.Name)
		_, branchLookupErr := getReference(gitRepo, branchRefName)
		branchExists := branchLookupErr == nil

		var cmd *exec.Cmd
		if branchExists {
			cmd = exec.Command("git", "update-ref", branchRefName.String(), branch.LastReflogID)
		} else {
			cmd = exec.Command("git", "branch", branch.Name, branch.LastReflogID)
		}
		cmd.Dir = repoPath

		if output, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("  Warning: Failed to restore branch '%s': %v\nOutput:\n%s",
				branch.Name, err, string(output))
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

// mergeTowerWithMode is the merge engine.
//
// Contract for callers (land, onto/from):
//   - mode: MergeModeNormal merges only diverged branches; MergeModeReset merges
//     every branch in tower order.
//   - skipConfirmation: skip the interactive confirmation prompt.
//   - partialBranch: when set, start processing at this branch (for 'merge from').
//   - resetNewBase: in MergeModeReset, the ref to merge into the first branch
//     (e.g. a new base for 'merge onto', or the updated tower.Base for 'land').
//     When empty, tower.Base is used as the merge source for the first branch.
//
// Returns errMergePaused when a merge conflict stops the operation.
func mergeTowerWithMode(mode MergeMode, skipConfirmation bool, partialBranch string, resetNewBase string) error {
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	// Block if a merge is already in progress.
	if currentTower.MergeState != nil && currentTower.MergeState.IsInProgress {
		return fmt.Errorf("a merge is already in progress for tower '%s'. Resolve conflicts and run 'ghenga merge continue' or clear the state with 'ghenga merge cancel'", currentTower.Name)
	}

	// Dirty-worktree check.
	worktreeBranches := detectWorktreeBranches(repoPath, currentTower.Branches)
	dirtyDirs, err := checkDirtyDirectories(repoPath, worktreeBranches)
	if err != nil {
		return fmt.Errorf("failed to check for uncommitted changes: %w", err)
	}
	if len(dirtyDirs) > 0 {
		errColor := color.New(color.FgRed).Add(color.Bold)
		var sb strings.Builder
		sb.WriteString("Cannot start merge: uncommitted changes detected in:\n")
		for path, desc := range dirtyDirs {
			sb.WriteString(fmt.Sprintf("  - %s (%s)\n", path, desc))
		}
		sb.WriteString("\nPlease commit or stash changes in all directories before starting a merge.")
		errColor.Print("") // Force color initialization
		return fmt.Errorf("%s", sb.String())
	}

	if currentTower.Base == "" {
		return fmt.Errorf("tower '%s' has no base branch set. Use 'ghenga base <branch-name>' to set it first", currentTower.Name)
	}

	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

	// Clear any stale previous undo state and warn the user.
	if !skipConfirmation && currentTower.LastRebased != "" {
		warningColor := color.New(color.FgRed).Add(color.Bold)
		warningColor.Printf("WARNING: Found previous merge undo state from %s.\n", currentTower.LastRebased)
		fmt.Print("Do you want to clear the previous undo state and continue with the merge? [y/N]: ")
		var clearResponse string
		fmt.Scanln(&clearResponse)
		if strings.ToLower(clearResponse) != "y" && strings.ToLower(clearResponse) != "yes" {
			fmt.Println("Merge operation cancelled.")
			return nil
		}
	}

	// Always clear previous undo state before saving new state.
	fmt.Println("Clearing/preparing merge undo state...")
	for i := range currentTower.Branches {
		currentTower.Branches[i].LastReflogID = ""
	}
	currentTower.LastRebased = ""
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to clear previous merge undo state in config: %w", err)
	}

	// --- Plan which branches need merging ---
	type mergeItem struct {
		branchName string
		base       string // ref to merge into branchName
	}

	var plan []mergeItem

	if mode == MergeModeReset {
		// Reset mode: merge the given base (or tower.Base if empty) into b0, then
		// chain upward through every branch.
		firstBase := resetNewBase
		if firstBase == "" {
			firstBase = currentTower.Base
		}
		for i, b := range currentTower.Branches {
			base := firstBase
			if i > 0 {
				base = currentTower.Branches[i-1].Name
			}
			plan = append(plan, mergeItem{branchName: b.Name, base: base})
		}
	} else {
		// Normal mode: merge only branches whose base has diverged (and all
		// branches above the first diverged one, since their effective base also
		// moved once the predecessor is updated).

		// Determine the start index for divergence checking. When partialBranch is
		// set the caller wants to start at that branch; otherwise start at b0 (index
		// 0) so that b0's divergence from tower.Base is also checked.
		startIdx := 0
		if partialBranch != "" {
			for idx, b := range currentTower.Branches {
				if b.Name == partialBranch {
					startIdx = idx
					break
				}
			}
		}

		firstDivergedFound := false
		for i := startIdx; i < len(currentTower.Branches); i++ {
			b := currentTower.Branches[i]

			// Determine this branch's base ref name: index 0 uses tower.Base, the
			// rest use the previous branch.
			var baseName string
			if i == 0 {
				baseName = currentTower.Base
			} else {
				baseName = currentTower.Branches[i-1].Name
			}

			bHeadRef, err := getReference(gitRepo, plumbing.NewBranchReferenceName(b.Name))
			if err != nil {
				fmt.Printf("  Skipping branch '%s': does not exist locally.\n", b.Name)
				continue
			}

			// Resolve the base: prefer a branch ref, fall back to any revision
			// (e.g. a commit hash used as tower.Base).
			baseHash, err := func() (plumbing.Hash, error) {
				ref, err := getReference(gitRepo, plumbing.NewBranchReferenceName(baseName))
				if err == nil {
					return ref.Hash(), nil
				}
				h, err2 := resolveRevision(gitRepo, plumbing.Revision(baseName))
				if err2 != nil {
					return plumbing.ZeroHash, fmt.Errorf("failed to resolve base '%s': %w", baseName, err)
				}
				return *h, nil
			}()
			if err != nil {
				return err
			}

			mb, err := findMergeBase(gitRepo, bHeadRef.Hash(), baseHash)
			if err != nil {
				return fmt.Errorf("failed to find merge base between '%s' and '%s': %w", b.Name, baseName, err)
			}
			isDiverged := mb != baseHash
			if isDiverged || firstDivergedFound {
				plan = append(plan, mergeItem{branchName: b.Name, base: baseName})
				firstDivergedFound = true
			}
		}
	}

	if len(plan) == 0 {
		fmt.Println("No diverged branches found in the current tower. All branches are up to date.")
		currentTower.MergeState = nil
		_ = SaveConfig(config)
		return nil
	}

	// --- Show plan and prompt ---
	branchColor := color.New(color.FgYellow)
	fmt.Printf("Found %d branch(es) needing merge in tower '%s':\n", len(plan), currentTower.Name)
	for _, item := range plan {
		branchColor.Printf("  %s (merging %s into it)\n", item.branchName, item.base)
	}

	if len(worktreeBranches) > 0 {
		warnColor := color.New(color.FgYellow)
		warnColor.Println("\nNote: The following branches are checked out in worktrees:")
		for branch, path := range worktreeBranches {
			fmt.Printf("  - %s -> %s\n", branch, path)
		}
		warnColor.Println("After merge, you'll need to run 'git reset --hard' in each worktree to sync.")
	}

	if !skipConfirmation {
		fmt.Println("You may undo the merge with 'ghenga merge undo'.")
		fmt.Println("You may continue a paused merge with 'ghenga merge continue'.")
		fmt.Print("\nProceed with merging? [y/N]: ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			fmt.Println("Merge operation cancelled.")
			return nil
		}
	}

	// Update tower base when given a new base in reset mode.
	if mode == MergeModeReset && resetNewBase != "" {
		fmt.Printf("Updating tower base from '%s' to '%s'...\n", currentTower.Base, resetNewBase)
		currentTower.Base = resetNewBase
		if err := SaveConfig(config); err != nil {
			return fmt.Errorf("failed to update tower base: %w", err)
		}
	}

	// --- Save undo bookkeeping ---
	fmt.Println("Saving pre-merge state for undo...")
	currentTower.LastRebased = time.Now().Format(time.RFC3339)
	// Only save LastReflogID for branches that are in the merge plan; clear it
	// on branches that are not being touched so undo doesn't try to restore them
	// to a stale hash from a previous run.
	planSet := make(map[string]bool, len(plan))
	for _, item := range plan {
		planSet[item.branchName] = true
	}
	for i := range currentTower.Branches {
		branch := &currentTower.Branches[i]
		if !planSet[branch.Name] {
			branch.LastReflogID = ""
			continue
		}
		branchRef, err := getReference(gitRepo, plumbing.NewBranchReferenceName(branch.Name))
		if err != nil {
			fmt.Printf("  Warning: Could not get current ref for branch '%s' to save undo state: %v\n",
				branch.Name, err)
			continue
		}
		branch.LastReflogID = branchRef.Hash().String()
	}
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration with branch states: %w", err)
	}

	// --- Determine original / main-repo branches ---
	originalBranch, err := getCurrentBranchName(gitRepo)
	if err != nil {
		fmt.Printf("Warning: Could not determine current branch, defaulting to HEAD: %v\n", err)
		head, headErr := getHead(gitRepo)
		if headErr == nil && head != nil {
			originalBranch = head.Name().Short()
		} else {
			originalBranch = "HEAD"
		}
	}

	mainRepoBranchCmd := exec.Command("git", "branch", "--show-current")
	mainRepoBranchCmd.Dir = repoPath
	mainRepoBranchOutput, _ := mainRepoBranchCmd.Output()
	mainRepoBranch := strings.TrimSpace(string(mainRepoBranchOutput))

	// Build the list of all branch names in this operation (for worktree reset filtering).
	mergedBranches := make([]string, len(plan))
	for i, item := range plan {
		mergedBranches[i] = item.branchName
	}

	// Convert plan to BranchMergeInfo slice used by state (RemainingBranches).
	remaining := make([]BranchMergeInfo, len(plan))
	for i, item := range plan {
		remaining[i] = BranchMergeInfo{Name: item.branchName, BaseBranchName: item.base}
	}

	fmt.Println("----------------------------------------")

	// --- Execute merges sequentially ---
	for len(remaining) > 0 {
		cur := remaining[0]
		fmt.Printf("Merging '%s' into '%s'...\n", cur.BaseBranchName, cur.Name)

		// Check out the target branch in detached HEAD mode so that update-ref
		// can advance the branch pointer even when it is checked out in a worktree.
		if err := checkoutCommitDetached(repoPath, cur.Name); err != nil {
			return fmt.Errorf("failed to checkout '%s' (detached): %w", cur.Name, err)
		}

		mergeOutput, mergeErr := runGitCommandWithRetry(repoPath, "merge", "--no-edit", cur.BaseBranchName)
		if mergeErr != nil {
			// Conflict: save state and pause.
			conflictColor := color.New(color.FgRed).Add(color.Bold)
			conflictColor.Printf("\n!! Merge conflict merging '%s' into '%s'\n", cur.BaseBranchName, cur.Name)
			fmt.Printf("Git output:\n%s\n", string(mergeOutput))
			fmt.Printf("\nWorking directory: %s\n", repoPath)
			fmt.Println("Please resolve the conflicts, stage the changes ('git add <file>...').")
			fmt.Println("Once resolved, run 'ghenga merge continue'.")
			fmt.Println("To abort, run 'ghenga merge cancel'.")

			currentTower.MergeState = &MergeState{
				IsInProgress:      true,
				TargetBranch:      cur.Name,
				BaseBranch:        cur.BaseBranchName,
				OriginalBranch:    originalBranch,
				MainRepoBranch:    mainRepoBranch,
				MergedBranches:    mergedBranches,
				RemainingBranches: remaining, // includes the current one at index 0
			}
			if err := SaveConfig(config); err != nil {
				return fmt.Errorf("merge conflict AND failed to save state: %w", err)
			}
			return errMergePaused
		}

		// Advance the real branch pointer to HEAD (the new merge commit, or the
		// same commit if git fast-forwarded).
		if err := updateRefToHead(repoPath, cur.Name); err != nil {
			return err
		}
		fmt.Printf("  Updated branch '%s' to new merge commit.\n", cur.Name)
		fmt.Println("----------------------------------------")

		remaining = remaining[1:]
	}

	successColor := color.New(color.FgGreen).Add(color.Bold)
	successColor.Println("\nTower merge completed successfully! Run 'ghenga sync' to update your remote branches.")

	// Prompt to reset worktrees.
	mergedWorktrees := filterWorktreesByNames(worktreeBranches, mergedBranches)
	promptAndResetWorktrees(mergedWorktrees)

	// Restore the main repo's branch (merge operations left HEAD detached).
	if err := restoreRepoBranch(repoPath, mainRepoBranch, originalBranch); err != nil {
		fmt.Printf("Warning: %v\n", err)
	}

	// Clear merge state.
	currentTower.MergeState = nil
	if err := SaveConfig(config); err != nil {
		fmt.Printf("Warning: Failed to clear merge state after successful merge: %v\n", err)
	}

	return nil
}

// updateRefToHead advances the named branch pointer to HEAD using git update-ref.
// This works even when the branch is checked out in another worktree (unlike
// git branch -f, which refuses to move a branch that is checked out elsewhere).
func updateRefToHead(repoPath, branchName string) error {
	// Resolve HEAD to the current commit hash.
	revParseCmd := exec.Command("git", "rev-parse", "HEAD")
	revParseCmd.Dir = repoPath
	hashOutput, err := revParseCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to resolve HEAD for branch update: %w", err)
	}
	commitHash := strings.TrimSpace(string(hashOutput))

	updateRefOutput, err := runGitCommandWithRetry(repoPath, "update-ref",
		"refs/heads/"+branchName, commitHash)
	if err != nil {
		return fmt.Errorf("failed to update branch '%s' to HEAD (%s): %w\nOutput: %s",
			branchName, commitHash[:7], err, string(updateRefOutput))
	}
	return nil
}

// restoreRepoBranch checks out mainRepoBranch in the main repo directory,
// falling back to originalBranch if mainRepoBranch is empty.
func restoreRepoBranch(repoPath, mainRepoBranch, originalBranch string) error {
	branchToRestore := mainRepoBranch
	if branchToRestore == "" {
		branchToRestore = originalBranch
	}
	if branchToRestore == "" || branchToRestore == "HEAD" {
		return nil
	}
	fmt.Printf("Restoring main repo to branch '%s'...\n", branchToRestore)
	if err := CheckoutBranch(repoPath, branchToRestore); err != nil {
		if worktreePath, _ := isBranchInWorktree(repoPath, branchToRestore); worktreePath != "" {
			fmt.Printf("Note: Branch '%s' is in worktree at '%s'.\n", branchToRestore, worktreePath)
			fmt.Printf("You may want to: cd %s\n", worktreePath)
			return nil
		}
		return fmt.Errorf("failed to checkout '%s': %v", branchToRestore, err)
	}
	return nil
}
