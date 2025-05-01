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

type RebaseCmd struct {
	Do   RebaseDoCmd   `cmd:"" default:"1" hidden:"" help:"Rebase all branches in the current tower that have diverged from their base"`
	Undo RebaseUndoCmd `cmd:"undo" help:"Undo the last rebase operation for the current tower"`
}

type RebaseDoCmd struct {
}

func (r *RebaseDoCmd) Run(_ *kong.Context) error {
	statusCmd := exec.Command("git", "status", "--porcelain")
	output, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	if len(strings.TrimSpace(string(output))) > 0 {
		return fmt.Errorf("working directory is not clean. Please commit or stash your changes before rebasing")
	}

	config, _, currentTower, _, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

	currentTime := time.Now().Format(time.RFC3339)
	currentTower.LastRebased = currentTime

	// Store the current commit hash for each branch before rebasing
	for i := range currentTower.Branches {
		branch := &currentTower.Branches[i]
		branchRefName := plumbing.NewBranchReferenceName(branch.Name)
		branchRef, err := gitRepo.Reference(branchRefName, true)
		if err != nil {
			// If branch doesn't exist in git, skip storing its hash
			continue
		}
		branch.LastReflogID = branchRef.Hash().String()
	}

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration with branch states: %w", err)
	}

	// Store information about diverged branches and their unique commits
	type BranchInfo struct {
		Index          int
		Name           string
		BaseBranchName string
		UniqueCommits  []string // List of unique commit hashes in reverse order (oldest first)
		IsDiverged     bool
	}

	var branchInfos []BranchInfo

	// Get the current branch to restore it at the end
	head, err := gitRepo.Head()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	originalBranch := head.Name().Short()

	// First pass: collect all branches, their unique commits, and determine which have diverged
	for i := 1; i < len(currentTower.Branches); i++ {
		branch := currentTower.Branches[i]
		baseBranch := currentTower.Branches[i-1]

		branchRefName := plumbing.NewBranchReferenceName(branch.Name)
		branchHead, err := gitRepo.Reference(branchRefName, true)
		if err != nil {
			// Skip if branch doesn't exist in git
			continue
		}

		baseRefName := plumbing.NewBranchReferenceName(baseBranch.Name)
		baseHead, err := gitRepo.Reference(baseRefName, true)
		if err != nil {
			// Skip if base branch doesn't exist in git
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
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to list unique commits for '%s': %w", branch.Name, err)
		}

		commits := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(commits) > 0 && commits[0] != "" {
			branchInfo.UniqueCommits = commits
		}

		branchInfos = append(branchInfos, branchInfo)
	}

	// Keep every branch after (and including) the first diverged branch
	var divergedBranches []BranchInfo
	for i := range branchInfos {
		if branchInfos[i].IsDiverged {
			divergedBranches = branchInfos[i:]
			break
		}
	}

	if len(divergedBranches) == 0 {
		fmt.Println("No diverged branches found in the current tower. All branches are up to date.")
		return nil
	}

	branchColor := color.New(color.FgYellow)
	warningColor := color.New(color.FgRed).Add(color.Bold)

	fmt.Printf("Found %d diverged branch(es) in tower '%s':\n", len(divergedBranches), currentTower.Name)
	for _, db := range divergedBranches {
		branchColor.Printf("  %s (based on %s) - %d unique commits\n",
			db.Name, db.BaseBranchName, len(db.UniqueCommits))
	}

	warningColor.Println("\nWARNING: Rebasing will change commit hashes and you will need to force-push to remote branches if they exist.")
	warningColor.Println("Make sure you understand the implications of rebasing published branches.")
	fmt.Println("You may undo the rebase with 'ghenga rebase undo' if you make a mistake.")

	fmt.Print("\nDo you want to proceed with rebasing these branches? [y/N]: ")

	var response string
	fmt.Scanln(&response)

	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Rebase operation cancelled.")
		return nil
	}

	// Second pass: perform rebases
	for _, db := range divergedBranches {
		fmt.Printf("Rebasing '%s' onto '%s'...\n", db.Name, db.BaseBranchName)

		if len(db.UniqueCommits) == 0 {
			fmt.Printf("No unique commits found for '%s', skipping\n", db.Name)
			continue
		}

		// Checkout the base branch
		checkoutBaseCmd := exec.Command("git", "checkout", db.BaseBranchName)
		if err := checkoutBaseCmd.Run(); err != nil {
			return fmt.Errorf("failed to checkout base branch '%s': %w", db.BaseBranchName, err)
		}

		// Create and checkout a temporary branch
		tempBranch := fmt.Sprintf("temp-rebase-%s", db.Name)
		createTempCmd := exec.Command("git", "checkout", "-b", tempBranch)
		if err := createTempCmd.Run(); err != nil {
			return fmt.Errorf("failed to create temporary branch: %w", err)
		}

		// Cherry-pick each unique commit onto the temporary branch
		for _, commit := range db.UniqueCommits {
			cherryPickCmd := exec.Command("git", "cherry-pick", commit)
			if err := cherryPickCmd.Run(); err != nil {
				// Clean up by deleting the temporary branch
				deleteTempCmd := exec.Command("git", "checkout", originalBranch)
				deleteTempCmd.Run()
				deleteTempCmd = exec.Command("git", "branch", "-D", tempBranch)
				deleteTempCmd.Run()

				return fmt.Errorf("failed to cherry-pick commit '%s': %w", commit, err)
			}
		}

		// Force-update the original branch to point to our temporary branch
		forceUpdateCmd := exec.Command("git", "branch", "-f", db.Name, tempBranch)
		if err := forceUpdateCmd.Run(); err != nil {
			return fmt.Errorf("failed to update branch '%s': %w", db.Name, err)
		}

		// Checkout the updated branch
		checkoutUpdatedCmd := exec.Command("git", "checkout", db.Name)
		if err := checkoutUpdatedCmd.Run(); err != nil {
			return fmt.Errorf("failed to checkout updated branch '%s': %w", db.Name, err)
		}

		// Delete the temporary branch
		deleteTempCmd := exec.Command("git", "branch", "-D", tempBranch)
		if err := deleteTempCmd.Run(); err != nil {
			fmt.Printf("Warning: Failed to delete temporary branch '%s'\n", tempBranch)
		}

		fmt.Printf("Successfully rebased '%s' onto '%s'\n", db.Name, db.BaseBranchName)
	}

	// Restore the original branch
	checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
	if err := checkoutOriginalCmd.Run(); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s'\n", originalBranch)
	}

	fmt.Println("All diverged branches have been successfully rebased!")
	return nil
}

type RebaseUndoCmd struct {
}

func (r *RebaseUndoCmd) Run(_ *kong.Context) error {
	config, _, currentTower, _, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	if currentTower.LastRebased == "" {
		return fmt.Errorf("no previous rebase found for tower '%s'", currentTower.Name)
	}

	branchesToRestore := 0
	for _, branch := range currentTower.Branches {
		if branch.LastReflogID != "" {
			branchesToRestore++
		}
	}

	if branchesToRestore == 0 {
		return fmt.Errorf("no branch states found to restore in tower '%s'", currentTower.Name)
	}

	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

	warningColor := color.New(color.FgRed).Add(color.Bold)

	warningColor.Println("\nWARNING: Undoing a rebase will reset your branches to their previous state.")
	warningColor.Printf("This will restore %d branches to their state before the rebase on %s.\n",
		branchesToRestore, currentTower.LastRebased)
	warningColor.Println("Any changes made after the rebase will be lost.")

	fmt.Print("\nDo you want to proceed with undoing the last rebase? [y/N]: ")

	var response string
	fmt.Scanln(&response)

	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Undo operation cancelled.")
		return nil
	}

	// Get the current branch to restore it at the end
	head, err := gitRepo.Head()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	originalBranch := head.Name().Short()

	// For each branch in the tower, try to restore it using its stored reflog ID
	for i := len(currentTower.Branches) - 1; i >= 0; i-- {
		branch := &currentTower.Branches[i]

		if branch.LastReflogID == "" {
			continue
		}

		fmt.Printf("Attempting to restore branch '%s'...\n", branch.Name)

		branchRefName := plumbing.NewBranchReferenceName(branch.Name)
		_, err := gitRepo.Reference(branchRefName, true)
		branchExists := err == nil

		var cmd *exec.Cmd
		if branchExists {
			cmd = exec.Command("git", "update-ref", branchRefName.String(), branch.LastReflogID)
		} else {
			cmd = exec.Command("git", "branch", branch.Name, branch.LastReflogID)
		}

		if err := cmd.Run(); err != nil {
			fmt.Printf("Warning: Failed to restore branch '%s'\n", branch.Name)
		} else {
			fmt.Printf("Successfully restored branch '%s'\n", branch.Name)
			// Clear the stored reflog ID
			branch.LastReflogID = ""
		}
	}

	currentTower.LastRebased = ""

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
	if err := checkoutOriginalCmd.Run(); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s'\n", originalBranch)
	}

	fmt.Println("Successfully undid the last rebase!")
	return nil
}
