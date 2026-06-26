package main

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5/plumbing"
)

// CheckpointCmd snapshots the current commit of every branch in the current
// tower, plus the tower's base, under a name (defaulting to a timestamp).
type CheckpointCmd struct {
	Name string `arg:"" optional:"" help:"Name for the checkpoint (defaults to the current date and time)"`
}

func (c *CheckpointCmd) Run(_ *kong.Context) error {
	config, _, currentTower, _, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	if len(currentTower.Branches) == 0 {
		return fmt.Errorf("tower '%s' has no branches to checkpoint", currentTower.Name)
	}

	name := c.Name
	if name == "" {
		name = time.Now().Format("2006-01-02T15-04-05")
	}

	// Resolve each branch's current commit hash.
	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

	branches := make([]CheckpointBranch, 0, len(currentTower.Branches))
	for _, b := range currentTower.Branches {
		ref, err := getReference(gitRepo, plumbing.NewBranchReferenceName(b.Name))
		if err != nil {
			return fmt.Errorf("failed to resolve branch '%s': %w", b.Name, err)
		}
		branches = append(branches, CheckpointBranch{
			Name: b.Name,
			Hash: ref.Hash().String(),
		})
	}

	cp := Checkpoint{
		Name:     name,
		Created:  time.Now().Format(time.RFC3339),
		Base:     currentTower.Base,
		Branches: branches,
	}

	// Check for a collision with an existing checkpoint name.
	existingIdx := -1
	for i, existing := range currentTower.Checkpoints {
		if existing.Name == name {
			existingIdx = i
			fmt.Printf("Warning: a checkpoint named '%s' already exists (created %s).\n",
				name, existing.Created)
			fmt.Printf("Overwrite checkpoint '%s'? [y/N]: ", name)
			answer := strings.ToLower(readLine())
			if answer != "y" && answer != "yes" {
				fmt.Println("Checkpoint not overwritten.")
				return nil
			}
			break
		}
	}

	if existingIdx >= 0 {
		currentTower.Checkpoints[existingIdx] = cp
	} else {
		currentTower.Checkpoints = append(currentTower.Checkpoints, cp)
	}

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save checkpoint: %w", err)
	}

	fmt.Printf("Checkpoint '%s' saved for tower '%s':\n", name, currentTower.Name)
	for _, b := range branches {
		fmt.Printf("  %s @ %s\n", b.Name, b.Hash[:7])
	}
	return nil
}

// RestoreCmd lists the current tower's checkpoints and restores the selected
// one: it moves each recorded branch back to its saved commit and restores the
// tower's branch list and base.
type RestoreCmd struct{}

func (r *RestoreCmd) Run(_ *kong.Context) error {
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	if currentTower.operationInProgress() {
		return fmt.Errorf("tower '%s' has an operation in progress (paused rebase/merge). "+
			"Complete or cancel it before restoring a checkpoint", currentTower.Name)
	}

	// Require a clean working tree — restore mutates refs.
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = repoPath
	statusOutput, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}
	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		return fmt.Errorf("working directory '%s' is not clean. "+
			"Please commit or stash your changes before restoring a checkpoint", repoPath)
	}

	if len(currentTower.Checkpoints) == 0 {
		return fmt.Errorf("no checkpoints found for tower '%s'", currentTower.Name)
	}

	// Sort a copy of the checkpoints newest-first. When Created timestamps are
	// equal, later-appended checkpoints sort first (they are "newer" by position).
	// We capture the original indices before sorting to implement this tiebreaker.
	type indexedCheckpoint struct {
		cp  Checkpoint
		idx int // original index in the slice (higher = appended later = "newer")
	}
	indexed := make([]indexedCheckpoint, len(currentTower.Checkpoints))
	for i, cp := range currentTower.Checkpoints {
		indexed[i] = indexedCheckpoint{cp: cp, idx: i}
	}
	sort.SliceStable(indexed, func(i, j int) bool {
		if indexed[i].cp.Created != indexed[j].cp.Created {
			return indexed[i].cp.Created > indexed[j].cp.Created
		}
		return indexed[i].idx > indexed[j].idx // higher index = newer
	})
	sorted := make([]Checkpoint, len(indexed))
	for i, ic := range indexed {
		sorted[i] = ic.cp
	}

	// Display the list.
	fmt.Printf("Checkpoints for tower '%s':\n", currentTower.Name)
	for i, cp := range sorted {
		label := ""
		if i == 0 {
			label = " [latest]"
		}
		t, parseErr := time.Parse(time.RFC3339, cp.Created)
		humanTime := cp.Created
		if parseErr == nil {
			humanTime = t.Format("2006-01-02 15:04:05")
		}
		fmt.Printf("  %d. %-30s  (%s)  %d branches%s\n",
			i+1, cp.Name, humanTime, len(cp.Branches), label)
	}

	// Prompt for selection.
	fmt.Printf("Pick a checkpoint to restore [1]: ")
	inputLine := readLine()
	if inputLine == "" {
		inputLine = "1"
	}

	var pick int
	if _, parseErr := fmt.Sscanf(inputLine, "%d", &pick); parseErr != nil || pick < 1 || pick > len(sorted) {
		return fmt.Errorf("invalid selection '%s': choose a number between 1 and %d",
			inputLine, len(sorted))
	}

	chosen := sorted[pick-1]

	// Show what will happen and confirm.
	fmt.Printf("\nRestoring checkpoint '%s' (created %s):\n", chosen.Name, chosen.Created)
	fmt.Printf("  Tower base: %s\n", chosen.Base)
	for _, b := range chosen.Branches {
		fmt.Printf("  %s -> %s\n", b.Name, b.Hash[:7])
	}
	fmt.Printf("\nThis will move %d branch ref(s) and reset the tower's membership and base. Proceed? [y/N]: ",
		len(chosen.Branches))
	confirm := strings.ToLower(readLine())
	if confirm != "y" && confirm != "yes" {
		fmt.Println("Restore cancelled.")
		return nil
	}

	// Record the current branch so we can restore it at the end.
	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}
	originalBranch, err := getCurrentBranchName(gitRepo)
	if err != nil {
		fmt.Printf("Warning: could not determine current branch: %v\n", err)
		originalBranch = ""
	}

	// Restore each branch ref from the checkpoint.
	// Build a list of affected branch names for the worktree prompt later.
	affectedNames := make([]string, 0, len(chosen.Branches))
	for _, cb := range chosen.Branches {
		// Verify the saved commit still exists.
		verifyCmd := exec.Command("git", "cat-file", "-t", cb.Hash)
		verifyCmd.Dir = repoPath
		if err := verifyCmd.Run(); err != nil {
			fmt.Printf("Warning: saved commit %s for branch '%s' not found in repository — skipping.\n",
				cb.Hash[:7], cb.Name)
			continue
		}

		branchRefName := plumbing.NewBranchReferenceName(cb.Name)
		_, branchLookupErr := getReference(gitRepo, branchRefName)
		branchExists := branchLookupErr == nil

		var cmd *exec.Cmd
		if branchExists {
			cmd = exec.Command("git", "update-ref", branchRefName.String(), cb.Hash)
		} else {
			cmd = exec.Command("git", "branch", cb.Name, cb.Hash)
		}
		cmd.Dir = repoPath

		if output, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("Warning: failed to restore branch '%s' to %s: %v\nOutput:\n%s",
				cb.Name, cb.Hash[:7], err, string(output))
		} else {
			action := "restored"
			if !branchExists {
				action = "recreated"
			}
			fmt.Printf("  %s branch '%s' to %s\n", action, cb.Name, cb.Hash[:7])
			affectedNames = append(affectedNames, cb.Name)
		}
	}

	// Restore tower membership and base.
	restoredBranches := make([]Branch, 0, len(chosen.Branches))
	for _, cb := range chosen.Branches {
		restoredBranches = append(restoredBranches, Branch{Name: cb.Name})
	}
	currentTower.Branches = restoredBranches
	currentTower.Base = chosen.Base

	// Prompt-reset any worktrees that hold affected branches.
	worktreeBranches := detectWorktreeBranches(repoPath, currentTower.Branches)
	affectedWorktrees := filterWorktreesByNames(worktreeBranches, affectedNames)
	promptAndResetWorktrees(affectedWorktrees)

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save config after restore: %w", err)
	}

	// Return to the original branch.
	if originalBranch != "" && originalBranch != "HEAD" {
		if err := CheckoutBranch(repoPath, originalBranch); err != nil {
			fmt.Printf("Warning: failed to checkout original branch '%s': %v\n", originalBranch, err)
		}
	}

	fmt.Printf("\nCheckpoint '%s' restored successfully.\n", chosen.Name)
	return nil
}

// readLine reads one token from stdin via fmt.Scanln, matching the interactive
// input pattern used throughout this codebase.
func readLine() string {
	var s string
	fmt.Scanln(&s)
	return s
}
