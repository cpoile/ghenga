package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"slices"

	"github.com/alecthomas/kong"
	"github.com/fatih/color"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	kongcompletion "github.com/jotaen/kong-completion"
	"github.com/posener/complete"
)

const MAX_COMMITS_PER_BRANCH_TO_DISPLAY = 100

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

type LsCmd struct {
	TowerName string `arg:"" optional:"" help:"Name of the tower to list. If not provided, all towers will be listed." predictor:"predictTowers"`
}

func (l *LsCmd) Run(_ *kong.Context) error {
	_, repo, _, err := loadRepoConfig()
	if err != nil {
		// Handle the specific error where the repo is not found but config loaded
		if repo == nil && strings.Contains(err.Error(), "not found in configuration") {
			parts := strings.SplitN(err.Error(), "'", 3)
			if len(parts) == 3 {
				return fmt.Errorf("repository at '%s' not found in configuration", parts[1])
			}
			return err
		}
		return err
	}

	r, err := openGitRepo()
	if err != nil {
		return err
	}

	headRef, err := r.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}

	currentBranch := color.New(color.FgGreen).Add(color.Bold)
	towerColor := color.New(color.FgBlue).Add(color.Bold)
	branchColor := color.New(color.FgYellow)
	commitColor := color.New(color.FgWhite)
	baseCommitColor := color.New(color.FgCyan)
	divergedColor := color.New(color.FgRed).Add(color.Bold)
	warningColor := color.New(color.FgYellow).Add(color.Bold)

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

	for _, tower := range towers {
		towerColor.Printf("Tower: %s", tower.Name)

		if repo.Current == tower.Name {
			currentBranch.Printf(" (current)")
		}

		if tower.Base != "" {
			baseCommitColor.Printf(" [base: %s]", tower.Base[:7])
		}

		fmt.Println()

		// Check if the tower base branch exists
		if len(tower.Branches) > 0 {
			baseBranchName := tower.Branches[0].Name
			baseBranchRefName := plumbing.NewBranchReferenceName(baseBranchName)
			_, err := r.Reference(baseBranchRefName, true)
			if err != nil && errors.Is(err, plumbing.ErrReferenceNotFound) {
				warningColor.Println("  ⚠️ Warning: The base branch is no longer valid -- if it has been merged,")
				warningColor.Println("           you need to run 'ghenga land' to mark the base branch as merged.")
				warningColor.Println("           The rest of the tower will then be rebased.")
			}
		}

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

		// Display most recent branches first (like git log)
		for i := len(tower.Branches) - 1; i >= 0; i-- {
			branch := tower.Branches[i]

			branchRefName := plumbing.NewBranchReferenceName(branch.Name)
			if headRef.Name().String() == branchRefName.String() {
				currentBranch.Printf("  %s (current)\n", branch.Name)
			} else {
				branchColor.Printf("  %s\n", branch.Name)
			}

			// Find branch reference and get commits
			branchRef, err := r.Reference(branchRefName, true)
			if err != nil && errors.Is(err, plumbing.ErrReferenceNotFound) {
				warningColor.Println("  ⚠️ Warning: ^^^ Branch not found ^^^")
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

			// Display commits
			commitIter, err := r.Log(&git.LogOptions{From: branchRef.Hash()})
			if err != nil {
				continue
			}

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

				if count < MAX_COMMITS_PER_BRANCH_TO_DISPLAY {
					commits = append(commits, c)
					count++
					return nil
				}
				return fmt.Errorf("stop")
			})

			for j := range commits {
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

type BranchCmd struct {
	Add AddCmd `cmd:"add" help:"Add a branch to a tower in current repository"`
	Rm  RmCmd  `cmd:"rm" help:"Remove a branch from the current tower"`
}

type AddCmd struct {
	Name  string `arg:"" help:"Name of the branch to add" predictor:"predictBranches"`
	Tower string `help:"Name of the tower to add the branch to (optional, defaults to current tower)"`
}

func (a *AddCmd) Run(_ *kong.Context) error {
	config, repo, repoPath, err := loadOrCreateRepoConfig()
	if err != nil {
		return err
	}

	// Determine the target tower
	targetTowerName := a.Tower
	if targetTowerName == "" && repo.Current != "" {
		targetTowerName = repo.Current
	}
	if targetTowerName == "" {
		return fmt.Errorf("no tower specified, use 'ghenga current <tower-name>' to set one or specify --tower")
	}

	// Find or create tower entry in repo
	tower := FindOrCreateTower(repo, targetTowerName)

	// Check if branch already exists in tower
	if ContainsBranch(tower, a.Name) {
		return fmt.Errorf("branch '%s' already exists in tower '%s'", a.Name, targetTowerName)
	}

	// Open the repository
	r, err := openGitRepo()
	if err != nil {
		return err
	}

	// Verify the branch to add exists
	addBranchRefName := plumbing.NewBranchReferenceName(a.Name)
	addBranchRef, err := r.Reference(addBranchRefName, true)
	if err != nil {
		return fmt.Errorf("branch '%s' not found in repository", a.Name)
	}

	// If this is the first branch, prompt for a base branch
	if len(tower.Branches) == 0 {
		// Detect default branch
		defaultBranch := detectDefaultBranch(r)

		// Get all branch names
		branchLister := BranchLister{}
		branches := branchLister.Predict(complete.Args{})

		if len(branches) == 0 {
			return fmt.Errorf("no branches found in repository")
		}

		// Move default branch to the beginning of the list
		for i, branch := range branches {
			if branch == defaultBranch {
				// Remove default branch from its position
				branches = slices.Delete(branches, i, i+1)
				// Insert at the beginning
				branches = append([]string{defaultBranch}, branches...)
				break
			}
		}

		// Show available branches
		fmt.Printf("This is the first branch in tower '%s'. Please select a base branch.\n", tower.Name)
		fmt.Println("Available branches:")

		// Display branches with numbers
		for i, branch := range branches {
			if i < 9 {
				// Highlight the default branch
				if branch == defaultBranch {
					fmt.Printf(" %d: %s (default)\n", i+1, branch)
				} else {
					fmt.Printf(" %d: %s\n", i+1, branch)
				}
			} else if i == 9 {
				fmt.Printf("%d: %s\n", i+1, branch)
			} else {
				fmt.Printf("%d: %s\n", i+1, branch)
			}

			// Only show up to 20 branches to avoid overwhelming the user
			if i >= 19 {
				fmt.Println("... and more")
				break
			}
		}

		// Prompt for selection
		fmt.Printf("\nEnter branch number or name (default is 1, %s): ", defaultBranch)

		var input string
		fmt.Scanln(&input)

		var baseBranch string

		// Handle empty input (use default)
		if input == "" {
			baseBranch = defaultBranch
		} else {
			// Try to interpret input as a number
			var selection int
			_, err := fmt.Sscanf(input, "%d", &selection)
			if err == nil && selection > 0 && selection <= len(branches) && selection <= 20 {
				// Input was a valid number
				baseBranch = branches[selection-1]
			} else {
				// Input was not a number or out of range, treat as branch name
				baseBranch = input
			}
		}

		// Verify the base branch exists
		baseBranchRefName := plumbing.NewBranchReferenceName(baseBranch)
		baseBranchRef, err := r.Reference(baseBranchRefName, true)
		if err != nil {
			return fmt.Errorf("base branch '%s' not found in repository", baseBranch)
		}

		// Find merge base between base branch and branch to add
		mergeBase, err := findMergeBase(r, addBranchRef.Hash(), baseBranchRef.Hash())
		if err != nil {
			// If we can't find merge base, just use the base branch HEAD
			fmt.Printf("Could not find merge base, using HEAD of %s as tower base\n", baseBranch)
			tower.Base = baseBranchRef.Hash().String()
		} else {
			// Use merge base as tower base
			tower.Base = mergeBase.String()
			fmt.Printf("Found merge base between %s and %s, using it as tower base\n", baseBranch, a.Name)
			fmt.Println("If this is not what you want, you can set the base branch manually using 'ghenga base <commit>'")
		}
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
	// Load config, repo, and current tower
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
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
	currentTower.Branches = slices.Delete(currentTower.Branches, branchIndex, branchIndex+1)

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
	// Get the repository path first
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

	// Set default tower as current
	repo.Current = tower.Name

	// Save configuration
	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Initialized repository at '%s' with tower '%s' and set it as current\n", repoPath, tower.Name)
	return nil
}

type NewCmd struct {
	Name string `arg:"" help:"Name of the tower to create"`
}

func (n *NewCmd) Run(_ *kong.Context) error {
	config, repo, repoPath, err := loadOrCreateRepoConfig()
	if err != nil {
		return err
	}

	for _, tower := range repo.Towers {
		if tower.Name == n.Name {
			return fmt.Errorf("tower '%s' already exists in repository at '%s'", n.Name, repoPath)
		}
	}

	tower := FindOrCreateTower(repo, n.Name)

	repo.Current = n.Name

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Created tower '%s' in repository at '%s' and set it as the current tower\n", tower.Name, repoPath)
	return nil
}

type CurrentCmd struct {
	Tower string `arg:"" help:"Name of the tower to set as current" predictor:"predictTowers"`
}

func (c *CurrentCmd) Run(_ *kong.Context) error {
	config, repo, repoPath, err := loadRepoConfig()
	if err != nil {
		// Handle the specific error where the repo is not found but config loaded
		if repo == nil && strings.Contains(err.Error(), "not found in configuration") {
			return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
		}
		return err
	}

	if err := SetCurrentTower(repo, c.Tower); err != nil {
		return err
	}

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
	config, repoInfo, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	for _, tower := range repoInfo.Towers {
		if tower.Name == r.NewName {
			return fmt.Errorf("tower with name '%s' already exists", r.NewName)
		}
	}

	oldName := currentTower.Name
	currentTower.Name = r.NewName
	repoInfo.Current = r.NewName

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
	config, _, currentTower, repoPath, err := loadRepoInfoAndCurrentTower()
	if err != nil {
		return err
	}

	r, err := openGitRepo()
	if err != nil {
		return err
	}

	hash, err := r.ResolveRevision(plumbing.Revision(b.Commit))
	if err != nil {
		return fmt.Errorf("failed to resolve commit '%s': %w", b.Commit, err)
	}

	currentTower.Base = hash.String()

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
	config, repo, repoPath, err := loadRepoConfig()
	if err != nil {
		// Handle the specific error where the repo is not found but config loaded
		if repo == nil && strings.Contains(err.Error(), "not found in configuration") {
			return fmt.Errorf("repository at '%s' not found in configuration", repoPath)
		}
		return err
	}

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

	isCurrent := repo.Current == r.Name

	warningColor := color.New(color.FgRed).Add(color.Bold)

	if isCurrent {
		warningColor.Printf("WARNING: You are about to remove the current tower '%s'!\n", r.Name)
	} else {
		warningColor.Printf("WARNING: You are about to remove tower '%s'!\n", r.Name)
	}

	branchCount := len(repo.Towers[towerIndex].Branches)
	if branchCount > 0 {
		warningColor.Printf("This tower contains %d branch(es). The branches will still exist, but the tower that tracks them will be removed.\n", branchCount)
	}

	fmt.Print("Are you sure you want to continue? [y/N]: ")

	var response string
	fmt.Scanln(&response)

	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		fmt.Println("Operation cancelled.")
		return nil
	}

	repo.Towers = slices.Delete(repo.Towers, towerIndex, towerIndex+1)

	if isCurrent {
		repo.Current = ""
	}

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

type ConfigCmd struct {
}

func (c *ConfigCmd) Run(_ *kong.Context) error {
	configPath, err := ConfigPath()
	if err != nil {
		return fmt.Errorf("failed to get config file path: %w", err)
	}

	fmt.Println(configPath)
	return nil
}

type CLI struct {
	Globals

	Ls      LsCmd      `cmd:"" help:"List all towers in current repository"`
	Branch  BranchCmd  `cmd:"branch" help:"Operate on branches in the current tower: add, rm"`
	Init    InitCmd    `cmd:"init" help:"Initialize the current repository in ghenga config"`
	New     NewCmd     `cmd:"new" help:"Create a new tower in current repository and set it as current"`
	Current CurrentCmd `cmd:"current" help:"Set the current tower"`
	Rename  RenameCmd  `cmd:"rename" help:"Rename the current tower"`
	Base    BaseCmd    `cmd:"base" help:"Set the tower's base commit"`
	Rm      RmTowerCmd `cmd:"rm" help:"Remove the specified tower"`
	Rebase  RebaseCmd  `cmd:"rebase" help:"Rebase branches in the current tower (use 'rebase undo' to undo)"`
	Config  ConfigCmd  `cmd:"config" help:"Print the location of the config file"`
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
			Compact:             true,
			NoExpandSubcommands: true,
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
