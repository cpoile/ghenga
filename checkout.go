package main

import (
	"fmt"
	"os"
	"os/exec"

	"strings"
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

	// Change to repo directory for git operations
	if err := os.Chdir(repoPath); err != nil {
		return fmt.Errorf("failed to change to repo directory '%s': %w", repoPath, err)
	}

	// Check if working directory is clean before attempting checkout
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusOutput, err := statusCmd.Output()
	if err != nil {
		// Restore directory before returning error
		if restoreErr := os.Chdir(originalDir); restoreErr != nil {
			fmt.Printf("Warning: failed to restore original directory '%s': %v\n", originalDir, restoreErr)
		}
		return fmt.Errorf("failed to check git status: %w", err)
	}

	if len(strings.TrimSpace(string(statusOutput))) > 0 {
		// Restore directory before returning error
		if restoreErr := os.Chdir(originalDir); restoreErr != nil {
			fmt.Printf("Warning: failed to restore original directory '%s': %v\n", originalDir, statusOutput)
		}
		return fmt.Errorf("working directory is not clean. Please commit or stash your changes before switching branches")
	}

	// Try normal git checkout first (with retry logic for lock file errors)
	output, err := runGitCommandWithRetry(".", "checkout", branchName)

	if err == nil {
		// Normal checkout succeeded - restore original directory
		if restoreErr := os.Chdir(originalDir); restoreErr != nil {
			fmt.Printf("Warning: failed to restore original directory '%s': %v\n", originalDir, restoreErr)
		}
		return nil
	}

	// Check if the error is due to the branch being checked out in a worktree
	errorOutput := string(output)
	if strings.Contains(errorOutput, "already checked out") ||
		strings.Contains(errorOutput, "checked out at") {
		// Branch is in a worktree, try to switch to it
		// Note: switchToWorktree will change directories and we DON'T restore original
		return switchToWorktree(branchName)
	}

	// Some other checkout error occurred - restore directory before returning error
	if restoreErr := os.Chdir(originalDir); restoreErr != nil {
		fmt.Printf("Warning: failed to restore original directory '%s': %v\n", originalDir, restoreErr)
	}
	return fmt.Errorf("failed to checkout branch '%s': %w\nOutput: %s", branchName, err, errorOutput)
}

// switchToWorktree finds the worktree directory for the given branch and changes to it
func switchToWorktree(branchName string) error {
	// Get list of worktrees
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list worktrees: %w", err)
	}

	// Parse worktree list to find the directory for our branch
	worktreeDir, err := parseWorktreeDir(string(output), branchName)
	if err != nil {
		return fmt.Errorf("failed to find worktree for branch '%s': %w", branchName, err)
	}

	// Change to the worktree directory
	if err := os.Chdir(worktreeDir); err != nil {
		return fmt.Errorf("failed to change to worktree directory '%s': %w", worktreeDir, err)
	}

	fmt.Printf("Switched to worktree directory: %s (branch: %s)\n", worktreeDir, branchName)
	return nil
}

// parseWorktreeDir parses the output of 'git worktree list --porcelain' to find
// the directory for the specified branch
func parseWorktreeDir(output, targetBranch string) (string, error) {
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
				return currentWorktreeDir, nil
			}
		} else if strings.HasPrefix(line, "HEAD ") || strings.HasPrefix(line, "bare ") || strings.HasPrefix(line, "detached ") {
			// Known git worktree list fields - continue processing
		} else {
			// Unknown/invalid line - reset context
			currentWorktreeDir = ""
		}
	}

	return "", fmt.Errorf("no worktree found for branch '%s'", targetBranch)
}
