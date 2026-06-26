package main

import (
	"fmt"

	"github.com/alecthomas/kong"
	"github.com/go-git/go-git/v5/plumbing"
)

// MergeOntoCmd merges a new base down through the entire tower (the merge-strategy
// analogue of 'rebase onto'). It is the merge equivalent of:
//
//	ghenga rebase onto <new-base>
//
// The engine merges <new-base> into the first branch, then chains upward so every
// branch in the tower picks up the new base. Commit hashes on existing branches are
// preserved (only a new merge commit is added on top of each).
type MergeOntoCmd struct {
	NewBase string `arg:"" help:"Branch or commit to merge down through the tower" predictor:"predictBranches"`
}

func (m *MergeOntoCmd) Run(_ *kong.Context) error {
	if err := ensureTowerStrategy(StrategyMerge); err != nil {
		return err
	}

	if m.NewBase == "" {
		return fmt.Errorf("new base argument is required")
	}

	// Validate that NewBase resolves to a real ref before calling the engine.
	// The engine updates tower.Base to resetNewBase BEFORE running the merges, so an
	// invalid ref would corrupt the stored base. We therefore check up front and return
	// a clean error rather than leaving the config in a half-updated state.
	gitRepo, err := openGitRepo()
	if err != nil {
		return err
	}

	// Try branch ref first, then fall back to any revision (commit hash, tag, etc.).
	_, refErr := getReference(gitRepo, plumbing.NewBranchReferenceName(m.NewBase))
	if refErr != nil {
		if _, revErr := resolveRevision(gitRepo, plumbing.Revision(m.NewBase)); revErr != nil {
			return fmt.Errorf("failed to resolve new base '%s': not a valid branch or revision", m.NewBase)
		}
	}

	err = mergeTowerWithMode(MergeModeReset, false, "", m.NewBase)
	if err == errMergePaused {
		fmt.Println("Merge onto paused due to conflicts. Resolve them, then run 'ghenga merge continue' to resume.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to merge tower onto new base: %w", err)
	}

	fmt.Println("Merge onto completed successfully!")
	return nil
}

// MergeFromCmd merges the base down through the tower starting at a given branch
// (the merge-strategy analogue of 'rebase from'). Branches below Branch are left
// untouched; Branch and all branches above it in the tower are updated.
//
// FromCommit is accepted on the CLI for symmetry with 'rebase from' but is unused:
// unlike rebase, merge does not need a specific commit to start from — the engine
// finds divergence automatically from the branch's current tip.
type MergeFromCmd struct {
	Branch     string `kong:"optional,arg,name='branch',help='Branch to start the merge-down from.'"`
	FromCommit string `kong:"optional,arg,name='commit',help='Unused for merge; accepted for symmetry with rebase from.'"`
}

func (m *MergeFromCmd) Run(_ *kong.Context) error {
	if err := ensureTowerStrategy(StrategyMerge); err != nil {
		return err
	}

	// MergeModeNormal with partialBranch starts divergence-checking at Branch and
	// propagates upward through the tower from there.
	err := mergeTowerWithMode(MergeModeNormal, false, m.Branch, "")
	if err == errMergePaused {
		fmt.Println("Merge from paused due to conflicts. Resolve them, then run 'ghenga merge continue' to resume.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to merge tower from branch: %w", err)
	}

	fmt.Println("Merge from completed successfully!")
	return nil
}
