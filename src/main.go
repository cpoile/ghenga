package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"
	kongcompletion "github.com/jotaen/kong-completion"
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

type InitCmd struct {
	DefaultTower string `help:"Name of the default tower to create" default:"default"`
}

func (i *InitCmd) Run(_ *kong.Context) error {
	repoPath, err := getCurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to get current repository: %w", err)
	}

	config, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	existingRepo := findRepoByPath(config, repoPath)
	if existingRepo != nil {
		fmt.Printf("Repository at '%s' already initialized in ghenga\n", repoPath)
		return nil
	}

	repo := findOrCreateRepo(config, repoPath)

	tower := findOrCreateTower(repo, i.DefaultTower)

	repo.Current = tower.Name

	if err := SaveConfig(config); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	fmt.Printf("Initialized repository at '%s' with tower '%s' and set it as current\n", repoPath, tower.Name)
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
