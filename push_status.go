package main

import (
	"fmt"

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

// GetBranchPushStatus determines the push status of a local branch compared to its remote.
// It returns the status, the local commit hash, the remote commit hash, and any error during the check.
func GetBranchPushStatus(r *git.Repository, remoteName string, branchName string) (PushStatus, plumbing.Hash, plumbing.Hash, error) {
	localRefName := plumbing.NewBranchReferenceName(branchName)
	localRef, err := r.Reference(localRefName, true)
	if err != nil {
		// Local branch doesn't exist, which shouldn't happen if it's in the tower config
		return StatusError, plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("failed to get local reference %s: %w", localRefName, err)
	}
	localCommit := localRef.Hash()

	remoteRefName := plumbing.NewRemoteReferenceName(remoteName, branchName)
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
