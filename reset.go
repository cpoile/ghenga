package main

import (
	"fmt"

	"github.com/alecthomas/kong"
)

type ResetCmd struct {
	Onto ResetOntoCmd `cmd:"onto" help:"Reset the entire tower onto a new base"`
	Undo ResetUndoCmd `cmd:"undo" help:"Undo the last reset operation for the current tower"`
}

// ResetOntoCmd implements the main reset command that rebases the entire tower onto a new base
type ResetOntoCmd struct {
	NewBase string `arg:"" help:"Branch or commit to reset the tower to" predictor:"predictBranches"`
}

// ResetUndoCmd implements the reset undo command
type ResetUndoCmd struct {
}

// Run executes the reset do command
func (r *ResetOntoCmd) Run(_ *kong.Context) error {
	// Basic validation only
	if r.NewBase == "" {
		return fmt.Errorf("new base argument is required")
	}

	// Delegate everything else to rebaseTowerWithMode
	err := rebaseTowerWithMode(RebaseModeReset, false, "", "", r.NewBase)
	if err == errRebasePaused {
		fmt.Println("Reset paused due to conflicts. Use 'ghenga rebase continue' to resume after resolving.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to reset tower: %w", err)
	}

	fmt.Println("Reset completed successfully!")
	return nil
}

// Run executes the reset undo command using the shared rebase undo infrastructure
func (r *ResetUndoCmd) Run(ctx *kong.Context) error {
	// Delegate to the shared rebase undo functionality
	rebaseUndo := &RebaseUndoCmd{}
	return rebaseUndo.Run(ctx)
}
