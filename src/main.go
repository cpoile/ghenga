package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/fatih/color"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	kongcompletion "github.com/jotaen/kong-completion"
)

var Version = "0.1.0"

type VersionFlag string

func (v VersionFlag) Decode(ctx *kong.DecodeContext) error { return nil }
func (v VersionFlag) IsBool() bool                         { return true }
func (v VersionFlag) BeforeApply(app *kong.Kong, vars kong.Vars) error {
	fmt.Println(vars["version"])
	app.Exit(0)
	return nil
}

type Globals struct {
	Version    VersionFlag               `name:"version" help:"Print version information and quit"`
	Completion kongcompletion.Completion `cmd:"" help:"Outputs shell code for initialising tab completions" completion-shell-default:"false"`
}

type ListCmd struct {
	TowerName string `arg:"" optional:"" help:"Name of the tower to list. If not provided, all towers will be listed." predictor:"predictTowers"`
}

func (l *ListCmd) Run(_ *kong.Context) error {
	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find repository in config
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
	}

	// Open the repository
	r, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Get the HEAD reference
	headRef, err := r.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}

	// Create color formatters
	currentBranch := color.New(color.FgGreen).Add(color.Bold)
	towerColor := color.New(color.FgBlue).Add(color.Bold)
	branchColor := color.New(color.FgYellow)
	commitColor := color.New(color.FgWhite)
	baseCommitColor := color.New(color.FgCyan)
	divergedColor := color.New(color.FgRed).Add(color.Bold)

	// Filter towers based on TowerName
	var towers []*Tower
	if l.TowerName != "" {
		tower := FindTowerByName(repo, l.TowerName)
		if tower == nil {
			return fmt.Errorf("tower '%s' not found", l.TowerName)
		}
		towers = []*Tower{tower}
	} else {
		towers = repo.Towers
	}

	// Iterate through towers
	for _, tower := range towers {
		towerColor.Printf("Tower: %s", tower.Name)

		// Check if this is the current tower
		if repo.Current == tower.Name {
			currentBranch.Printf(" (current)")
		}

		// Show base commit if set
		if tower.Base != "" {
			baseCommitColor.Printf(" [base: %s]", tower.Base[:7])
		}

		fmt.Println()

		// Store branch hashes for limiting commit display
		branchHashes := make(map[int]plumbing.Hash)

		// Track divergence points for highlighting
		divergencePoints := make(map[string]bool)

		// First pass: collect branch hashes
		for i, branch := range tower.Branches {
			branchRefName := plumbing.NewBranchReferenceName(branch.Name)
			branchRef, err := r.Reference(branchRefName, true)
			if err == nil {
				branchHashes[i] = branchRef.Hash()
			}
		}

		// Iterate through branches in reverse order
		for i := len(tower.Branches) - 1; i >= 0; i-- {
			branch := tower.Branches[i]

			// Check if this is the current branch
			branchRefName := plumbing.NewBranchReferenceName(branch.Name)
			if headRef.Name().String() == branchRefName.String() {
				currentBranch.Printf("  %s (current)\n", branch.Name)
			} else {
				branchColor.Printf("  %s\n", branch.Name)
			}

			// Find branch reference and get commits
			branchRef, err := r.Reference(branchRefName, true)
			if err != nil {
				// Skip if branch doesn't exist in git
				continue
			}

			// Determine if we need to find the stop commit (branch below)
			var stopAtCommit plumbing.Hash
			var foundStopCommit bool
			var hasDiverged bool
			var divergencePoint plumbing.Hash

			// If this is not the first branch (base branch), use the branch below as stop point
			if i > 0 {
				if lowerHash, exists := branchHashes[i-1]; exists {
					stopAtCommit = lowerHash
					foundStopCommit = true

					// Find the merge base (common ancestor) between this branch and the one below
					mergeBase, err := findMergeBase(r, branchRef.Hash(), lowerHash)
					if err == nil {
						// If the merge base is not the head of the lower branch, they've diverged
						hasDiverged = mergeBase != lowerHash
						if hasDiverged {
							divergencePoint = mergeBase
							// Store the divergence point for highlighting in all branches
							divergencePoints[divergencePoint.String()] = true
						}
					}
				}
			}

			// Get commit history
			commitIter, err := r.Log(&git.LogOptions{From: branchRef.Hash()})
			if err != nil {
				continue
			}

			// Collect commits
			var commits []*object.Commit
			count := 0
			commitIter.ForEach(func(c *object.Commit) error {
				// For the base branch (i==0), stop at the tower base commit
				if i == 0 && tower.Base != "" && c.Hash.String() == tower.Base {
					commits = append(commits, c) // Include the base commit
					return fmt.Errorf("stop")
				}

				// For non-base branches, stop at the lower branch's commit
				if foundStopCommit && c.Hash == stopAtCommit {
					return fmt.Errorf("stop")
				}

				if count < 10 {
					commits = append(commits, c)
					count++
					return nil
				}
				return fmt.Errorf("stop")
			})

			// Display commits
			for j := 0; j < len(commits); j++ {
				commit := commits[j]

				// Show divergence warning just above the common ancestor
				if hasDiverged && commit.Hash == divergencePoint {
					divergedColor.Printf("    ⚠️ This branch has diverged ↓↓ here ↓↓ from the branch below\n")
				}

				message := strings.Split(commit.Message, "\n")[0]

				// Check if this is the base commit or a divergence point
				if i == 0 && tower.Base != "" && commit.Hash.String() == tower.Base {
					baseCommitColor.Printf("    %s %s (base)\n", commit.Hash.String()[:7], message)
				} else if divergencePoints[commit.Hash.String()] {
					// Highlight divergence points in all branches
					divergedColor.Printf("    %s %s\n", commit.Hash.String()[:7], message)
				} else {
					commitColor.Printf("    %s %s\n", commit.Hash.String()[:7], message)
				}
			}
		}
		fmt.Println()
	}

	return nil
}

// findMergeBase finds the merge base (common ancestor) between two commits
func findMergeBase(r *git.Repository, commit1, commit2 plumbing.Hash) (plumbing.Hash, error) {
	// If commits are the same, return immediately
	if commit1 == commit2 {
		return commit1, nil
	}

	// Get commit objects
	c1, err := r.CommitObject(commit1)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	c2, err := r.CommitObject(commit2)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	// Find merge base using go-git's MergeBase function
	mergeBase, err := c1.MergeBase(c2)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	if len(mergeBase) == 0 {
		return plumbing.ZeroHash, fmt.Errorf("no common ancestor found")
	}

	return mergeBase[0].Hash, nil
}

type BranchCmd struct {
	Add AddCmd `cmd:"add" help:"Add a branch to a tower in current repository"`
	Rm  RmCmd  `cmd:"rm" help:"Remove a branch from the current tower"`
}

type AddCmd struct {
	Name  string `arg:"" help:"Name of the branch to add" predictor:"predictBranches"`
	Tower string `help:"Name of the tower to add the branch to (optional, defaults to current tower)"`
}

func (a *AddCmd) Run(_ *kong.Context) error {
	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find or create repo entry in config
	repo := FindOrCreateRepo(config, repoPath)

	// If Tower is empty and we have a current tower, use the current tower
	if a.Tower == "" && repo.Current != "" {
		a.Tower = repo.Current
	}
	if a.Tower == "" {
		return fmt.Errorf("no tower specified, use 'ghenga current <tower-name>' to set one")
	}

	// Find or create tower entry in repo
	tower := FindOrCreateTower(repo, a.Tower)

	// Check if branch already exists in tower
	if ContainsBranch(tower, a.Name) {
		return fmt.Errorf("branch '%s' already exists in tower '%s'", a.Name, a.Tower)
	}

	// Add branch to tower
	tower.Branches = append(tower.Branches, Branch{Name: a.Name})

	// Save configuration
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Added branch '%s' to tower '%s' in repository at '%s'\n", a.Name, tower.Name, repoPath)
	return nil
}

type RmCmd struct {
	Name string `arg:"" help:"Name of the branch to remove" predictor:"predictTowerBranches"`
}

func (r *RmCmd) Run(_ *kong.Context) error {
	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find repo entry in config
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
	}

	// Check if a current tower is set
	if repo.Current == "" {
		return fmt.Errorf("no current tower set, use 'ghenga current <tower-name>' to set one")
	}

	// Get current tower
	currentTower := FindTowerByName(repo, repo.Current)
	if currentTower == nil {
		return fmt.Errorf("current tower '%s' not found", repo.Current)
	}

	// Check if branch exists in tower
	branchIndex := -1
	for i, branch := range currentTower.Branches {
		if branch.Name == r.Name {
			branchIndex = i
			break
		}
	}

	if branchIndex == -1 {
		return fmt.Errorf("branch '%s' not found in tower '%s'", r.Name, currentTower.Name)
	}

	// Remove branch from tower (preserving order)
	currentTower.Branches = append(currentTower.Branches[:branchIndex], currentTower.Branches[branchIndex+1:]...)

	// Save configuration
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Removed branch '%s' from tower '%s' in repository at '%s'\n", r.Name, currentTower.Name, repoPath)
	return nil
}

type InitCmd struct {
	DefaultTower string `help:"Name of the default tower to create" default:"default"`
}

func (i *InitCmd) Run(_ *kong.Context) error {
	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Check if repo already exists in configuration
	existingRepo := FindRepoByPath(config, repoPath)
	if existingRepo != nil {
		fmt.Printf("Repository at '%s' already initialized in ghenga\n", repoPath)
		return nil
	}

	// Create new repo entry in config
	repo := FindOrCreateRepo(config, repoPath)

	// Create default tower
	tower := FindOrCreateTower(repo, i.DefaultTower)

	// Save configuration
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Initialized repository at '%s' with tower '%s'\n", repoPath, tower.Name)
	return nil
}

type NewCmd struct {
	Name string `arg:"" help:"Name of the tower to create"`
}

func (n *NewCmd) Run(_ *kong.Context) error {
	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find or create repo entry in config
	repo := FindOrCreateRepo(config, repoPath)

	// Check if tower already exists
	for _, tower := range repo.Towers {
		if tower.Name == n.Name {
			return fmt.Errorf("tower '%s' already exists in repository at '%s'", n.Name, repoPath)
		}
	}

	// Create tower
	tower := FindOrCreateTower(repo, n.Name)

	// Save configuration
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Created tower '%s' in repository at '%s'\n", tower.Name, repoPath)
	return nil
}

type CurrentCmd struct {
	Tower string `arg:"" help:"Name of the tower to set as current" predictor:"predictTowers"`
}

func (c *CurrentCmd) Run(_ *kong.Context) error {
	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find repo entry in config
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
	}

	// Set the current tower
	if err := SetCurrentTower(repo, c.Tower); err != nil {
		return err
	}

	// Save configuration
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Set current tower to '%s' in repository at '%s'\n", c.Tower, repoPath)
	return nil
}

type RenameCmd struct {
	NewName string `arg:"" help:"New name for the current tower"`
}

func (r *RenameCmd) Run(_ *kong.Context) error {
	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find repo entry in config
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
	}

	// Check if a current tower is set
	if repo.Current == "" {
		return fmt.Errorf("no current tower set, use 'ghenga current <tower-name>' to set one")
	}

	// Get current tower
	currentTower := FindTowerByName(repo, repo.Current)
	if currentTower == nil {
		return fmt.Errorf("current tower '%s' not found", repo.Current)
	}

	// Check if new name already exists
	for _, tower := range repo.Towers {
		if tower.Name == r.NewName {
			return fmt.Errorf("tower with name '%s' already exists", r.NewName)
		}
	}

	// Store the old name for the output message
	oldName := currentTower.Name

	// Update the tower name
	currentTower.Name = r.NewName

	// Update the current tower reference
	repo.Current = r.NewName

	// Save configuration
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Renamed tower from '%s' to '%s' in repository at '%s'\n", oldName, r.NewName, repoPath)
	return nil
}

type BaseCmd struct {
	Commit string `arg:"" help:"Commit hash or reference to set as the tower's base" predictor:"predictGitRefs"`
}

func (b *BaseCmd) Run(_ *kong.Context) error {
	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find repo entry in config
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
	}

	// Check if a current tower is set
	if repo.Current == "" {
		return fmt.Errorf("no current tower set, use 'ghenga current <tower-name>' to set one")
	}

	// Get current tower
	currentTower := FindTowerByName(repo, repo.Current)
	if currentTower == nil {
		return fmt.Errorf("current tower '%s' not found", repo.Current)
	}

	// Open the git repository to validate the commit exists
	r, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Try to resolve the commit reference
	hash, err := r.ResolveRevision(plumbing.Revision(b.Commit))
	if err != nil {
		return fmt.Errorf("failed to resolve commit '%s': %w", b.Commit, err)
	}

	// Set the base commit for the tower
	currentTower.Base = hash.String()

	// Save configuration
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Set base commit for tower '%s' to '%s' in repository at '%s'\n", currentTower.Name, currentTower.Base, repoPath)
	return nil
}

type RmTowerCmd struct {
	Name string `arg:"" help:"Name of the tower to remove" predictor:"predictTowers"`
}

func (r *RmTowerCmd) Run(_ *kong.Context) error {
	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find repo entry in config
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
	}

	// Find the tower in the repository
	towerIndex := -1
	for i, tower := range repo.Towers {
		if tower.Name == r.Name {
			towerIndex = i
			break
		}
	}

	if towerIndex == -1 {
		return fmt.Errorf("tower '%s' not found in repository", r.Name)
	}

	// Check if this is the current tower
	isCurrent := repo.Current == r.Name

	// Create a red color for the warning
	warningColor := color.New(color.FgRed).Add(color.Bold)

	// Display warning and prompt for confirmation
	if isCurrent {
		warningColor.Printf("WARNING: You are about to remove the current tower '%s'!\n", r.Name)
	} else {
		warningColor.Printf("WARNING: You are about to remove tower '%s'!\n", r.Name)
	}

	// Show branch count
	branchCount := len(repo.Towers[towerIndex].Branches)
	if branchCount > 0 {
		warningColor.Printf("This tower contains %d branch(es). The branches will still exist, but the tower that tracks them will be removed.\n", branchCount)
	}

	// Prompt for confirmation
	fmt.Print("Are you sure you want to continue? [y/N]: ")

	// Read response
	var response string
	fmt.Scanln(&response)

	// Check if user confirmed
	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Operation cancelled.")
		return nil
	}

	// Remove the tower from the repository
	repo.Towers = append(repo.Towers[:towerIndex], repo.Towers[towerIndex+1:]...)

	// If we removed the current tower, unset current
	if isCurrent {
		repo.Current = ""
	}

	// Save configuration
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Removed tower '%s' from repository at '%s'\n", r.Name, repoPath)
	if isCurrent {
		fmt.Println("Note: Removed the current tower. Use 'ghenga current <tower-name>' to set a new current tower.")
	}

	return nil
}

type RebaseCmd struct {
	Do   RebaseDoCmd   `cmd:"" help:"Rebase all branches in the current tower that have diverged from their base"`
	Undo RebaseUndoCmd `cmd:"undo" help:"Undo the last rebase operation for the current tower"`
}

type RebaseDoCmd struct {
}

func (r *RebaseDoCmd) Run(_ *kong.Context) error {
	// Check if working directory is clean. If not, return error.
	statusCmd := exec.Command("git", "status", "--porcelain")
	output, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check git status: %w", err)
	}

	fmt.Println("status output:")
	fmt.Println(string(output))

	if len(strings.TrimSpace(string(output))) > 0 {
		return fmt.Errorf("working directory is not clean. Please commit or stash your changes before rebasing")
	}

	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find repo entry in config
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
	}

	// Check if a current tower is set
	if repo.Current == "" {
		return fmt.Errorf("no current tower set, use 'ghenga current <tower-name>' to set one")
	}

	// Get current tower
	currentTower := FindTowerByName(repo, repo.Current)
	if currentTower == nil {
		return fmt.Errorf("current tower '%s' not found", repo.Current)
	}

	// Open the repository
	gitRepo, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Store current timestamp for the rebase operation
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

	// Save the configuration with the updated reflog hashes
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

		// Get branch references
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

		// Find the merge base (common ancestor)
		mergeBase, err := findMergeBase(gitRepo, branchHead.Hash(), baseHead.Hash())
		if err != nil {
			return fmt.Errorf("failed to find merge base between '%s' and '%s': %w", branch.Name, baseBranch.Name, err)
		}

		// Create a DivergedBranch entry
		db := BranchInfo{
			Index:          i,
			Name:           branch.Name,
			BaseBranchName: baseBranch.Name,
			UniqueCommits:  []string{},
			IsDiverged:     mergeBase != baseHead.Hash(),
		}

		// Collect the unique commits
		// Use git rev-list to find unique commits
		cmd := exec.Command("git", "rev-list", "--reverse", baseHead.Hash().String()+".."+branchHead.Hash().String())
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("failed to list unique commits for '%s': %w", branch.Name, err)
		}

		// Parse the commit hashes
		commits := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(commits) > 0 && commits[0] != "" {
			db.UniqueCommits = commits
		}

		branchInfos = append(branchInfos, db)
	}

	// Keep every branch after (and including) the first diverged branch
	var realDivergedBranches []BranchInfo
	for i := range branchInfos {
		if branchInfos[i].IsDiverged {
			realDivergedBranches = branchInfos[i:]
			break
		}
	}

	// If no diverged branches found, exit early
	if len(realDivergedBranches) == 0 {
		fmt.Println("No diverged branches found in the current tower. All branches are up to date.")
		return nil
	}

	// Create color formatters
	branchColor := color.New(color.FgYellow)
	warningColor := color.New(color.FgRed).Add(color.Bold)

	// Print information about diverged branches and ask for confirmation
	fmt.Printf("Found %d diverged branch(es) in tower '%s':\n", len(realDivergedBranches), currentTower.Name)
	for _, db := range realDivergedBranches {
		branchColor.Printf("  %s (based on %s) - %d unique commits\n",
			db.Name, db.BaseBranchName, len(db.UniqueCommits))
	}

	// Show warning about rebase
	warningColor.Println("\nWARNING: Rebasing will change commit hashes and you will need to force-push to remote branches if they exist.")
	warningColor.Println("Make sure you understand the implications of rebasing published branches.")
	fmt.Println("You may undo the rebase with 'ghenga rebase undo' if you make a mistake.")

	// Prompt for confirmation
	fmt.Print("\nDo you want to proceed with rebasing these branches? [y/N]: ")

	// Read response
	var response string
	fmt.Scanln(&response)

	// Check if user confirmed
	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Rebase operation cancelled.")
		return nil
	}

	// Second pass: perform rebases
	for _, db := range realDivergedBranches {
		fmt.Printf("Rebasing '%s' onto '%s'...\n", db.Name, db.BaseBranchName)

		// If there are no unique commits, skip
		if len(db.UniqueCommits) == 0 {
			fmt.Printf("No unique commits found for '%s', skipping\n", db.Name)
			continue
		}

		// Checkout the base branch
		checkoutBaseCmd := exec.Command("git", "checkout", db.BaseBranchName)
		checkoutBaseCmd.Stdout = os.Stdout
		checkoutBaseCmd.Stderr = os.Stderr
		if err := checkoutBaseCmd.Run(); err != nil {
			return fmt.Errorf("failed to checkout base branch '%s': %w", db.BaseBranchName, err)
		}

		// Create and checkout a temporary branch
		tempBranch := fmt.Sprintf("temp-rebase-%s", db.Name)
		createTempCmd := exec.Command("git", "checkout", "-b", tempBranch)
		createTempCmd.Stdout = os.Stdout
		createTempCmd.Stderr = os.Stderr
		if err := createTempCmd.Run(); err != nil {
			return fmt.Errorf("failed to create temporary branch: %w", err)
		}

		// Cherry-pick each unique commit onto the temporary branch
		for _, commit := range db.UniqueCommits {
			cherryPickCmd := exec.Command("git", "cherry-pick", commit)
			cherryPickCmd.Stdout = os.Stdout
			cherryPickCmd.Stderr = os.Stderr
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
		forceUpdateCmd.Stdout = os.Stdout
		forceUpdateCmd.Stderr = os.Stderr
		if err := forceUpdateCmd.Run(); err != nil {
			return fmt.Errorf("failed to update branch '%s': %w", db.Name, err)
		}

		// Checkout the updated branch
		checkoutUpdatedCmd := exec.Command("git", "checkout", db.Name)
		checkoutUpdatedCmd.Stdout = os.Stdout
		checkoutUpdatedCmd.Stderr = os.Stderr
		if err := checkoutUpdatedCmd.Run(); err != nil {
			return fmt.Errorf("failed to checkout updated branch '%s': %w", db.Name, err)
		}

		// Delete the temporary branch
		deleteTempCmd := exec.Command("git", "branch", "-D", tempBranch)
		deleteTempCmd.Stdout = os.Stdout
		deleteTempCmd.Stderr = os.Stderr
		if err := deleteTempCmd.Run(); err != nil {
			fmt.Printf("Warning: Failed to delete temporary branch '%s'\n", tempBranch)
		}

		fmt.Printf("Successfully rebased '%s' onto '%s'\n", db.Name, db.BaseBranchName)
	}

	// Restore the original branch
	checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
	checkoutOriginalCmd.Stdout = os.Stdout
	checkoutOriginalCmd.Stderr = os.Stderr
	if err := checkoutOriginalCmd.Run(); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s'\n", originalBranch)
	}

	fmt.Println("All diverged branches have been successfully rebased!")
	return nil
}

type RebaseUndoCmd struct {
}

func (r *RebaseUndoCmd) Run(_ *kong.Context) error {
	// Get the repository path
	repoPath, err := GetCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	// Load configuration
	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Find repo entry in config
	repo := FindRepoByPath(config, repoPath)
	if repo == nil {
		return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
	}

	// Check if a current tower is set
	if repo.Current == "" {
		return fmt.Errorf("no current tower set, use 'ghenga current <tower-name>' to set one")
	}

	// Get current tower
	currentTower := FindTowerByName(repo, repo.Current)
	if currentTower == nil {
		return fmt.Errorf("current tower '%s' not found", repo.Current)
	}

	// Check if there's a stored rebase timestamp
	if currentTower.LastRebased == "" {
		return fmt.Errorf("no previous rebase found for tower '%s'", currentTower.Name)
	}

	// Check if any branches have stored reflog IDs
	branchesToRestore := 0
	for _, branch := range currentTower.Branches {
		if branch.LastReflogID != "" {
			branchesToRestore++
		}
	}

	if branchesToRestore == 0 {
		return fmt.Errorf("no branch states found to restore in tower '%s'", currentTower.Name)
	}

	// Open the repository
	gitRepo, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Create color formatters
	warningColor := color.New(color.FgRed).Add(color.Bold)

	// Show warning about undo
	warningColor.Println("\nWARNING: Undoing a rebase will reset your branches to their previous state.")
	warningColor.Printf("This will restore %d branches to their state before the rebase on %s.\n",
		branchesToRestore, currentTower.LastRebased)
	warningColor.Println("Any changes made after the rebase will be lost.")

	// Prompt for confirmation
	fmt.Print("\nDo you want to proceed with undoing the last rebase? [y/N]: ")

	// Read response
	var response string
	fmt.Scanln(&response)

	// Check if user confirmed
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

		// Skip branches without a stored reflog ID
		if branch.LastReflogID == "" {
			continue
		}

		// Try to restore the branch
		fmt.Printf("Attempting to restore branch '%s'...\n", branch.Name)

		// Check if branch exists
		branchRefName := plumbing.NewBranchReferenceName(branch.Name)
		_, err := gitRepo.Reference(branchRefName, true)
		branchExists := err == nil

		// Reset the branch to its stored state
		var cmd *exec.Cmd
		if branchExists {
			// Force-reset an existing branch
			cmd = exec.Command("git", "update-ref", branchRefName.String(), branch.LastReflogID)
		} else {
			// Create a new branch at the stored commit point
			cmd = exec.Command("git", "branch", branch.Name, branch.LastReflogID)
		}

		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("Warning: Failed to restore branch '%s'\n", branch.Name)
		} else {
			fmt.Printf("Successfully restored branch '%s'\n", branch.Name)
			// Clear the stored reflog ID
			branch.LastReflogID = ""
		}
	}

	// Clear the stored rebase timestamp
	currentTower.LastRebased = ""

	// Save the configuration with cleared reflog IDs
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	// Restore the original branch
	checkoutOriginalCmd := exec.Command("git", "checkout", originalBranch)
	checkoutOriginalCmd.Stdout = os.Stdout
	checkoutOriginalCmd.Stderr = os.Stderr
	if err := checkoutOriginalCmd.Run(); err != nil {
		fmt.Printf("Warning: Failed to checkout original branch '%s'\n", originalBranch)
	}

	fmt.Println("Successfully undid the last rebase!")
	return nil
}

type CLI struct {
	Globals

	List    ListCmd    `cmd:"" help:"List all towers in current repository"`
	Branch  BranchCmd  `cmd:"branch" help:"Operate on branches in the current working tower: add, remove, etc."`
	Init    InitCmd    `cmd:"init" help:"Initialize the current repository in ghenga config"`
	New     NewCmd     `cmd:"new" help:"Create a new tower in current repository"`
	Current CurrentCmd `cmd:"current" help:"Set the current tower"`
	Rename  RenameCmd  `cmd:"rename" help:"Rename the current tower"`
	Base    BaseCmd    `cmd:"base" help:"Set the tower's base commit"`
	Rm      RmTowerCmd `cmd:"rm" help:"Remove the specified tower"`
	Rebase  RebaseCmd  `cmd:"rebase" help:"Rebase operations for the current tower"`
}

func main() {
	// Load configuration at startup
	_, err := LoadConfig()
	if err != nil {
		fmt.Printf("Error loading configuration: %v\n", err)
	}

	// Create kong app, but don't run arg parsing yet.
	cli := CLI{
		Globals: Globals{
			Version: VersionFlag(Version),
		},
	}
	parser := kong.Must(&cli,
		kong.Name("ghenga"),
		kong.Description("A tool to manage stacked pull requests on Github"),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
		}),
		kong.Vars{
			"version": Version,
		})

	// Register completions. This must happen before the parsing step, so that
	// tab completion invocations can be intercepted.
	kongcompletion.Register(parser, predictTowers, predictBranches, predictGitRefs, predictTowerBranches)

	// Proceed as usual with parsing arguments and running the app.
	ctx, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err)

	err = ctx.Run(&cli.Globals)
	ctx.FatalIfErrorf(err)
}
