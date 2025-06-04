package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// CheckoutBranch attempts to checkout the specified branch in the given repository.
// If the checkout fails because the branch is already checked out in a worktree,
// it will change the working directory to that worktree instead.
func CheckoutBranch(repoPath, branchName string) error {
	// Save current directory
	originalDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	// Open repository using go-git
	r, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return fmt.Errorf("failed to open repository at '%s': %w", repoPath, err)
	}

	// Get worktree
	wt, err := r.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	// Check if working directory is clean
	if err := checkWorkingDirectoryClean(wt); err != nil {
		return err
	}

	// Check if branch exists in any worktree
	worktreeDir, inWorktree, err := findBranchInWorktrees(wt, branchName)
	if err != nil {
		return fmt.Errorf("failed to check worktrees: %w", err)
	}

	if inWorktree {
		// Branch is in a worktree, switch to it
		return switchToWorktreeDir(worktreeDir, branchName)
	}

	// Try to checkout the branch normally
	branchRef := plumbing.NewBranchReferenceName(branchName)
	checkoutOpts := &git.CheckoutOptions{
		Branch: branchRef,
	}

	if err := wt.Checkout(checkoutOpts); err != nil {
		// Restore original directory before returning error
		if restoreErr := os.Chdir(originalDir); restoreErr != nil {
			fmt.Printf("Warning: failed to restore original directory '%s': %v\n", originalDir, restoreErr)
		}
		return fmt.Errorf("failed to checkout branch '%s': %w", branchName, err)
	}

	// Restore original directory on success
	if restoreErr := os.Chdir(originalDir); restoreErr != nil {
		fmt.Printf("Warning: failed to restore original directory '%s': %v\n", originalDir, restoreErr)
	}
	return nil
}

// checkWorkingDirectoryClean verifies the working directory has no uncommitted changes
func checkWorkingDirectoryClean(wt *git.Worktree) error {
	status, err := wt.Status()
	if err != nil {
		return fmt.Errorf("failed to get working directory status: %w", err)
	}

	if !status.IsClean() {
		return fmt.Errorf("working directory is not clean. Please commit or stash your changes before switching branches")
	}
	return nil
}

// findBranchInWorktrees checks if a branch is checked out in any worktree
func findBranchInWorktrees(wt *git.Worktree, branchName string) (string, bool, error) {
	// Use git command to list worktrees since go-git doesn't have this functionality
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = wt.Filesystem.Root()
	output, err := cmd.Output()
	if err != nil {
		// If worktree command fails, assume no worktrees exist
		return "", false, nil
	}

	// Parse worktree list to find the directory for our branch
	worktreeDir, found := parseWorktreeDir(string(output), branchName)
	return worktreeDir, found, nil
}

// parseWorktreeDir parses the output of 'git worktree list --porcelain' to find
// the directory for the specified branch
func parseWorktreeDir(output, targetBranch string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(output), "\n")

	var currentWorktreeDir string
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if line == "" {
			// Empty line resets context
			currentWorktreeDir = ""
		} else if strings.HasPrefix(line, "worktree ") {
			currentWorktreeDir = strings.TrimPrefix(line, "worktree ")
		} else if strings.HasPrefix(line, "branch ") {
			branch := strings.TrimPrefix(line, "branch ")
			// Remove refs/heads/ prefix if present
			branch = strings.TrimPrefix(branch, "refs/heads/")

			if branch == targetBranch && currentWorktreeDir != "" {
				return currentWorktreeDir, true
			}
		} else if strings.HasPrefix(line, "HEAD ") || strings.HasPrefix(line, "bare ") || strings.HasPrefix(line, "detached ") {
			// Known git worktree list fields - continue processing
		} else {
			// Unknown/invalid line - reset context
			currentWorktreeDir = ""
		}
	}

	return "", false
}

// switchToWorktreeDir changes to the specified worktree directory
func switchToWorktreeDir(worktreeDir, branchName string) error {
	// Change to the worktree directory
	if err := os.Chdir(worktreeDir); err != nil {
		return fmt.Errorf("failed to change to worktree directory '%s': %w", worktreeDir, err)
	}

	fmt.Printf("Switched to worktree directory: %s (branch: %s)\n", worktreeDir, branchName)
	return nil
}
