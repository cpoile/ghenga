package main

import (
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeStrategyTower sets up a test env with a single current tower whose base is
// "main", optionally pre-setting its strategy, and returns the repo path.
func makeStrategyTower(t *testing.T, strategy string) (string, func()) {
	t.Helper()
	repoPath, _, cleanup := setupTestEnv(t)

	towerName := "strat-tower"
	tower := &Tower{Name: towerName, Base: "main", Strategy: strategy}
	config := createTestConfig(t, repoPath, towerName, []*Tower{tower}, "main")
	require.NoError(t, SaveConfig(config))

	return repoPath, cleanup
}

func TestStrategy_DefaultIsRebase(t *testing.T) {
	// A tower with an empty Strategy must behave as a rebase tower.
	tower := &Tower{Name: "t", Base: "main"}
	assert.Equal(t, StrategyRebase, tower.strategy())

	tower.Strategy = StrategyMerge
	assert.Equal(t, StrategyMerge, tower.strategy())
}

func TestStrategy_GuardsPointToMatchingCommand(t *testing.T) {
	// Rebase tower: merge commands are rejected, rebase commands allowed.
	repoPath, cleanup := makeStrategyTower(t, "") // empty == rebase
	defer cleanup()
	_ = repoPath

	require.NoError(t, ensureTowerStrategy(StrategyRebase))
	err := ensureTowerStrategy(StrategyMerge)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uses the 'rebase' strategy")
	assert.Contains(t, err.Error(), "Use 'ghenga rebase'")

	// A merge command run against a rebase tower surfaces the same guard.
	mergeErr := (&MergeDoCmd{}).Run(&kong.Context{})
	require.Error(t, mergeErr)
	assert.Contains(t, mergeErr.Error(), "Use 'ghenga rebase'")
}

func TestStrategy_GuardsRejectRebaseOnMergeTower(t *testing.T) {
	repoPath, cleanup := makeStrategyTower(t, StrategyMerge)
	defer cleanup()
	_ = repoPath

	require.NoError(t, ensureTowerStrategy(StrategyMerge))
	err := ensureTowerStrategy(StrategyRebase)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uses the 'merge' strategy")
	assert.Contains(t, err.Error(), "Use 'ghenga merge'")

	// A rebase command run against a merge tower surfaces the guard before any
	// git work happens.
	rebaseErr := (&RebaseDoCmd{}).Run(&kong.Context{})
	require.Error(t, rebaseErr)
	assert.Contains(t, rebaseErr.Error(), "Use 'ghenga merge'")
}

func TestStrategy_BaseSetsStrategyWithConfirm(t *testing.T) {
	repoPath, cleanup := makeStrategyTower(t, "") // starts as rebase
	defer cleanup()

	// Switching rebase -> merge prompts; answer yes.
	restore := mockInput("y")
	cmd := &BaseCmd{BaseBranch: "main", Strategy: StrategyMerge}
	out, err := CaptureOutput(func() error { return cmd.Run(&kong.Context{}) })
	restore()
	require.NoError(t, err)
	assert.Contains(t, out, "WARNING")

	repoConfig, _ := runTowerCommandAndGetRepo(t, &BaseCmd{BaseBranch: "main"}, &kong.Context{}, repoPath)
	// runTowerCommandAndGetRepo with no Strategy keeps strategy unchanged.
	tower := findTowerByName(repoConfig, "strat-tower")
	require.NotNil(t, tower)
	assert.Equal(t, StrategyMerge, tower.strategy(), "strategy should now be merge")
}

func TestStrategy_CommandSetsStrategyWithoutChangingBase(t *testing.T) {
	_, cleanup := makeStrategyTower(t, "") // starts as rebase
	defer cleanup()

	restore := mockInput("y")
	cmd := &StrategyCmd{Strategy: StrategyMerge}
	out, err := CaptureOutput(func() error { return cmd.Run(&kong.Context{}) })
	restore()
	require.NoError(t, err)
	assert.Contains(t, out, "Set strategy")

	config, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(config.Repos[0], "strat-tower")
	require.NotNil(t, tower)
	assert.Equal(t, "main", tower.Base, "strategy command must not change the base")
	assert.Equal(t, StrategyMerge, tower.strategy())
}

func TestStrategy_CommandIsRegistered(t *testing.T) {
	parser := kong.Must(&CLI{})

	for _, strategy := range []string{StrategyMerge, StrategyRebase} {
		_, err := parser.Parse([]string{"strategy", strategy})
		require.NoError(t, err)
	}
}

func TestStrategy_BaseChangeDeclinedLeavesUnchanged(t *testing.T) {
	repoPath, cleanup := makeStrategyTower(t, StrategyMerge)
	defer cleanup()
	_ = repoPath

	// Switching merge -> rebase prompts; answer no.
	restore := mockInput("n")
	cmd := &BaseCmd{BaseBranch: "main", Strategy: StrategyRebase}
	out, err := CaptureOutput(func() error { return cmd.Run(&kong.Context{}) })
	restore()
	require.NoError(t, err)
	assert.Contains(t, out, "cancelled")

	config, err := LoadConfig()
	require.NoError(t, err)
	tower := findTowerByName(config.Repos[0], "strat-tower")
	require.NotNil(t, tower)
	assert.Equal(t, StrategyMerge, tower.strategy(), "declined change must leave strategy as merge")
}

func TestStrategy_SameStrategyNoPrompt(t *testing.T) {
	repoPath, cleanup := makeStrategyTower(t, StrategyMerge)
	defer cleanup()
	_ = repoPath

	// Re-asserting the same strategy must not prompt and must not error even
	// with no stdin available.
	cmd := &BaseCmd{BaseBranch: "main", Strategy: StrategyMerge}
	out, err := CaptureOutput(func() error { return cmd.Run(&kong.Context{}) })
	require.NoError(t, err)
	assert.NotContains(t, out, "WARNING")
}

func TestStrategy_ChangeBlockedWhenOperationInProgress(t *testing.T) {
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	towerName := "busy-tower"
	tower := &Tower{
		Name:       towerName,
		Base:       "main",
		Strategy:   StrategyRebase,
		MergeState: &MergeState{IsInProgress: true, TargetBranch: "x"},
	}
	config := createTestConfig(t, repoPath, towerName, []*Tower{tower}, "main")
	require.NoError(t, SaveConfig(config))

	cmd := &BaseCmd{BaseBranch: "main", Strategy: StrategyMerge}
	err := cmd.Run(&kong.Context{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in progress")
}
