package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// PushStatus represents the state of a local branch relative to its remote counterpart.
type PushStatus int

const (
	StatusError PushStatus = iota // Error occurred during status check
	NoRemote                      // Remote branch does not exist
	UpToDate                      // Local and remote are the same
	LocalAhead                    // Local is ahead (fast-forward possible)
	RemoteAhead                   // Remote is ahead (pull needed)
	Diverged                      // Branches have diverged (force-push needed)
)

// getPushRemoteForBranch determines the actual push remote for a branch based on Git configuration.
// It follows Git's precedence rules:
// 1. branch.<name>.pushRemote
// 2. remote.pushDefault
// 3. branch.<name>.remote (upstream remote)
// 4. "origin" as fallback
func getPushRemoteForBranch(r *git.Repository, branchName string) (string, error) {
	// Get the worktree root to run git config commands in correct directory
	wt, err := r.Worktree()
	if err != nil {
		return "", fmt.Errorf("failed to get worktree: %w", err)
	}
	workDir := wt.Filesystem.Root()

	// Helper function to run git config command
	getConfig := func(key string) (string, error) {
		cmd := exec.Command("git", "config", "--get", key)
		cmd.Dir = workDir
		output, err := cmd.Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(output)), nil
	}

	// 1. Try branch-specific push remote: branch.<name>.pushRemote
	if pushRemote, err := getConfig(fmt.Sprintf("branch.%s.pushRemote", branchName)); err == nil && pushRemote != "" {
		return pushRemote, nil
	}

	// 2. Try global push default: remote.pushDefault
	if pushDefault, err := getConfig("remote.pushDefault"); err == nil && pushDefault != "" {
		return pushDefault, nil
	}

	// 3. Try branch upstream remote: branch.<name>.remote
	if upstreamRemote, err := getConfig(fmt.Sprintf("branch.%s.remote", branchName)); err == nil && upstreamRemote != "" {
		return upstreamRemote, nil
	}

	// 4. Fall back to "origin"
	return "origin", nil
}

// GetBranchPushStatus determines the push status of a local branch compared to its remote.
// It returns the status, the local commit hash, the remote commit hash, and any error during the check.
// If remoteName is empty, the function automatically determines the correct push remote based on Git configuration.
// If remoteName is provided, it uses the specified remote name directly.
func GetBranchPushStatus(r *git.Repository, remoteName string, branchName string) (PushStatus, plumbing.Hash, plumbing.Hash, error) {
	localRefName := plumbing.NewBranchReferenceName(branchName)
	localRef, err := r.Reference(localRefName, true)
	if err != nil {
		// Local branch doesn't exist, which shouldn't happen if it's in the tower config
		return StatusError, plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("failed to get local reference %s: %w", localRefName, err)
	}
	localCommit := localRef.Hash()

	// Use provided remote name, or auto-detect if empty
	actualRemoteName := remoteName
	if actualRemoteName == "" {
		actualRemoteName, err = getPushRemoteForBranch(r, branchName)
		if err != nil {
			return StatusError, localCommit, plumbing.ZeroHash, fmt.Errorf("failed to determine push remote for branch %s: %w", branchName, err)
		}
	}

	remoteRefName := plumbing.NewRemoteReferenceName(actualRemoteName, branchName)
	remoteRef, err := r.Reference(remoteRefName, true)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			// Remote branch doesn't exist
			return NoRemote, localCommit, plumbing.ZeroHash, nil
		}
		// Other error fetching remote ref
		return StatusError, localCommit, plumbing.ZeroHash, fmt.Errorf("failed to get remote reference %s: %w", remoteRefName, err)
	}
	remoteCommit := remoteRef.Hash()

	if localCommit == remoteCommit {
		return UpToDate, localCommit, remoteCommit, nil
	}

	// Find merge base to determine relationship
	mergeBaseCommit, err := findMergeBase(r, localCommit, remoteCommit)
	if err != nil {
		return StatusError, localCommit, remoteCommit, fmt.Errorf("failed to find merge base between %s and %s: %w", localCommit.String()[:7], remoteCommit.String()[:7], err)
	}

	if mergeBaseCommit == remoteCommit {
		// Local is ahead of remote
		return LocalAhead, localCommit, remoteCommit, nil
	} else if mergeBaseCommit == localCommit {
		// Remote is ahead of local
		return RemoteAhead, localCommit, remoteCommit, nil
	} else {
		// Local and remote have diverged
		return Diverged, localCommit, remoteCommit, nil
	}
}
