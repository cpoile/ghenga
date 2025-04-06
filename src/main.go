package main

import (
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/fatih/color"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
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
	Version VersionFlag `name:"version" help:"Print version information and quit"`
}

type ListCmd struct {
	TowerName string `arg:"" optional:"" help:"Name of the tower to list. If not provided, all towers will be listed."`
}

func (a *ListCmd) Run(ctx *kong.Context) error {
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

	// Filter towers based on TowerName
	var towers []*Tower
	if a.TowerName != "" {
		tower := FindTowerByName(repo, a.TowerName)
		if tower == nil {
			return fmt.Errorf("tower '%s' not found", a.TowerName)
		}
		towers = []*Tower{tower}
	} else {
		towers = repo.Towers
	}

	// Iterate through towers
	for _, tower := range towers {
		towerColor.Printf("Tower: %s\n", tower.Name)

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

			// Get commit history
			commitIter, err := r.Log(&git.LogOptions{From: branchRef.Hash()})
			if err != nil {
				continue
			}

			// Collect commits (limit to 10 for now)
			var commits []*object.Commit
			count := 0
			commitIter.ForEach(func(c *object.Commit) error {
				if count < 10 {
					commits = append(commits, c)
					count++
					return nil
				}
				return fmt.Errorf("stop")
			})

			// Display commits in reverse order
			for j := 0; j < len(commits); j++ {
				commit := commits[j]
				message := strings.Split(commit.Message, "\n")[0]
				commitColor.Printf("    %s %s\n", commit.Hash.String()[:7], message)
			}
		}
		fmt.Println()
	}

	return nil
}

type AddCmd struct {
	Name  string `arg:"" help:"Name of the branch to add"`
	Tower string `help:"Name of the tower to add the branch to" default:"default"`
}

func (a *AddCmd) Run(ctx *kong.Context) error {
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

type InitCmd struct {
	DefaultTower string `help:"Name of the default tower to create" default:"default"`
}

func (i *InitCmd) Run(ctx *kong.Context) error {
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

type CLI struct {
	Globals

	List ListCmd `cmd:"" aliases:"ls" help:"List all towers in current repository"`
	Add  AddCmd  `cmd:"add" help:"Add a branch to a tower in current repository"`
	Init InitCmd `cmd:"init" help:"Initialize the current repository in ghenga config"`
}

func main() {
	// Load configuration at startup
	_, err := LoadConfig()
	if err != nil {
		fmt.Printf("Error loading configuration: %v\n", err)
	}

	cli := CLI{
		Globals: Globals{
			Version: VersionFlag(Version),
		},
	}

	ctx := kong.Parse(&cli,
		kong.Name("ghenga"),
		kong.Description("A tool to manage stacked pull requests on Github"),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{
			Compact: true,
		}),
		kong.Vars{
			"version": Version,
		})
	err = ctx.Run(&cli.Globals)
	ctx.FatalIfErrorf(err)
}
