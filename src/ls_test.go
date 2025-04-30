package main

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLsCmd_NoTowers(t *testing.T) {
	// Setup test environment
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	// Initialize empty config
	config := createTestConfig(t, repoPath, "", []*Tower{}, "")

	// Run the list command
	output := runLsCommandWithConfig(t, config, &LsCmd{})

	// Verify output doesn't contain any towers
	assert.NotContains(t, output, "Tower:")
}

func TestLsCmd_OneTowerNoRepositories(t *testing.T) {
	// Setup test environment
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	// Initialize config with one tower but no branches
	towers := []*Tower{
		{
			Name:     "test-tower",
			Branches: []Branch{},
		},
	}
	config := createTestConfig(t, repoPath, "", towers, "")

	// Run the list command
	output := runLsCommandWithConfig(t, config, &LsCmd{})

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")
	assert.NotContains(t, output, "Initial commit")
}

func TestLsCmd_OneTowerOneBranchNoCommits(t *testing.T) {
	// Setup test environment
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Create a branch but don't add any commits
	branchName := "feature-branch"
	headRef, err := repo.Head()
	require.NoError(t, err)
	branchRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), headRef.Hash())
	err = repo.Storer.SetReference(branchRef)
	require.NoError(t, err)

	// Initialize config with one tower and one branch
	towers := []*Tower{
		{
			Name: "test-tower",
			Branches: []Branch{
				{Name: branchName},
			},
		},
	}
	config := createTestConfig(t, repoPath, "", towers, "")

	// Run the list command
	output := runLsCommandWithConfig(t, config, &LsCmd{})

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")
	assert.Contains(t, output, branchName)
	assert.Contains(t, output, "Initial commit")
}

func TestLsCmd_OneTowerOneBranchWithCommits(t *testing.T) {
	// Setup test environment and get repo object
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Create a branch with commits
	branchName := "feature-branch"
	createTestBranch(t, repo, branchName, 3)

	// Initialize config with one tower and one branch
	towers := []*Tower{
		{
			Name: "test-tower",
			Branches: []Branch{
				{Name: branchName},
			},
		},
	}
	config := createTestConfig(t, repoPath, "", towers, "")

	// Run the list command
	output := runLsCommandWithConfig(t, config, &LsCmd{})

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")
	assert.Contains(t, output, branchName)
	assert.Contains(t, output, "Add file-feature-branch-0.txt")
	assert.Contains(t, output, "Add file-feature-branch-1.txt")
	assert.Contains(t, output, "Add file-feature-branch-2.txt")

	// Verify ordering - the most recent commit (2) should come before older commits (1 and 0)
	pos2 := strings.Index(output, "Add file-feature-branch-2.txt")
	pos1 := strings.Index(output, "Add file-feature-branch-1.txt")
	pos0 := strings.Index(output, "Add file-feature-branch-0.txt")
	assert.True(t, pos2 < pos1, "Most recent commit should be listed first")
	assert.True(t, pos1 < pos0, "Commits should be in reverse chronological order")
}

func TestLsCmd_MultipleTowersMultipleBranches(t *testing.T) {
	// Setup test environment and get repo object
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Create branches with commits
	createTestBranch(t, repo, "feature-1", 2)
	createTestBranch(t, repo, "feature-2", 3)
	createTestBranch(t, repo, "bugfix-1", 1)
	createTestBranch(t, repo, "bugfix-2", 2)

	// Initialize config with multiple towers and branches
	towers := []*Tower{
		{
			Name: "feature-tower",
			Branches: []Branch{
				{Name: "feature-1"},
				{Name: "feature-2"},
			},
		},
		{
			Name: "bugfix-tower",
			Branches: []Branch{
				{Name: "bugfix-1"},
				{Name: "bugfix-2"},
			},
		},
	}
	config := createTestConfig(t, repoPath, "", towers, "")

	// Run the list command
	output := runLsCommandWithConfig(t, config, &LsCmd{})

	// Verify output
	assert.Contains(t, output, "Tower: feature-tower")
	assert.Contains(t, output, "Tower: bugfix-tower")

	// Check branch ordering (reverse order)
	feature2Index := strings.Index(output, "feature-2")
	feature1Index := strings.Index(output, "feature-1")
	assert.True(t, feature2Index < feature1Index, "feature-2 should be listed before feature-1")

	bugfix2Index := strings.Index(output, "bugfix-2")
	bugfix1Index := strings.Index(output, "bugfix-1")
	assert.True(t, bugfix2Index < bugfix1Index, "bugfix-2 should be listed before bugfix-1")

	// Check for commit messages
	assert.Contains(t, output, "Add file-feature-1-0.txt")
	assert.Contains(t, output, "Add file-feature-1-1.txt")
	assert.Contains(t, output, "Add file-feature-2-0.txt")
	assert.Contains(t, output, "Add file-feature-2-1.txt")
	assert.Contains(t, output, "Add file-feature-2-2.txt")
	assert.Contains(t, output, "Add file-bugfix-1-0.txt")
	assert.Contains(t, output, "Add file-bugfix-2-0.txt")
	assert.Contains(t, output, "Add file-bugfix-2-1.txt")

	// Verify ordering of commits within feature-2
	f2Pos2 := strings.Index(output, "Add file-feature-2-2.txt")
	f2Pos1 := strings.Index(output, "Add file-feature-2-1.txt")
	f2Pos0 := strings.Index(output, "Add file-feature-2-0.txt")
	assert.True(t, f2Pos2 < f2Pos1, "Most recent commit should be listed first")
	assert.True(t, f2Pos1 < f2Pos0, "Commits should be in reverse chronological order")
}

func TestLsCmd_FilterByTowerName(t *testing.T) {
	// Setup test environment and get repo object
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Create branches with commits
	createTestBranch(t, repo, "feature-1", 2)
	createTestBranch(t, repo, "bugfix-1", 1)

	// Initialize config with multiple towers and branches
	towers := []*Tower{
		{
			Name: "feature-tower",
			Branches: []Branch{
				{Name: "feature-1"},
			},
		},
		{
			Name: "bugfix-tower",
			Branches: []Branch{
				{Name: "bugfix-1"},
			},
		},
	}
	config := createTestConfig(t, repoPath, "", towers, "")

	// Run the list command with tower filter
	cmd := &LsCmd{TowerName: "feature-tower"}
	output := runLsCommandWithConfig(t, config, cmd)

	// Verify output
	assert.Contains(t, output, "Tower: feature-tower")
	assert.NotContains(t, output, "Tower: bugfix-tower")
	assert.Contains(t, output, "feature-1")
	assert.NotContains(t, output, "bugfix-1")
	assert.Contains(t, output, "Add file-feature-1-0.txt")
	assert.Contains(t, output, "Add file-feature-1-1.txt")
}

func TestLsCmd_StaggeredCommitView(t *testing.T) {
	// Setup test environment and get repo object
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Create a stack of branches with specific commits
	// main -> feature-base -> feature-middle -> feature-top

	// Get the initial main branch reference (created by setupTestRepo)
	headRef, err := repo.Head()
	require.NoError(t, err)
	mainHash := headRef.Hash()

	// Create feature-base branch with 2 commits
	createTestBranch(t, repo, "feature-base", 2)

	// Get the feature-base reference
	_, err = repo.Reference(plumbing.NewBranchReferenceName("feature-base"), true)
	require.NoError(t, err)

	// Create feature-middle branch with 3 commits
	createTestBranch(t, repo, "feature-middle", 3)

	// Get the feature-middle reference
	_, err = repo.Reference(plumbing.NewBranchReferenceName("feature-middle"), true)
	require.NoError(t, err)

	// Create feature-top branch with 2 commits
	createTestBranch(t, repo, "feature-top", 2)

	// Initialize config with one tower and a stack of branches in order
	towerName := "stacked-tower"
	towers := []*Tower{
		{
			Name: towerName,
			// Base is set via createTestConfig
			Branches: []Branch{
				{Name: "feature-base"},
				{Name: "feature-middle"},
				{Name: "feature-top"},
			},
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, mainHash.String())

	// Run the list command
	cmd := &LsCmd{}
	output := runLsCommandWithConfig(t, config, cmd)

	// Verify output
	// 1. Should contain all three branches in the correct order
	assert.Contains(t, output, "Tower: stacked-tower")

	// Find positions of each branch in the output to verify order
	featureTopPos := strings.Index(output, "feature-top (current)\n")
	assert.True(t, featureTopPos != -1, "feature-top should be in the output")
	featureMiddlePos := strings.Index(output, "feature-middle\n")
	assert.True(t, featureMiddlePos != -1, "feature-middle should be in the output")
	featureBasePos := strings.Index(output, "feature-base\n")
	assert.True(t, featureBasePos != -1, "feature-base should be in the output")

	// Verify branches are in the correct order (top-to-bottom)
	assert.True(t, featureTopPos < featureMiddlePos, "feature-top should be listed before feature-middle")
	assert.True(t, featureMiddlePos < featureBasePos, "feature-middle should be listed before feature-base")

	// 2. The base branch (feature-base) should show commits up to the base commit
	// It has 2 commits of its own + the base commit marker
	assert.Contains(t, output, "file-feature-base-0.txt")
	assert.Contains(t, output, "file-feature-base-1.txt")
	assert.Contains(t, output, "(base)")

	// 3. The middle branch should only show its unique commits (not feature-base commits)
	assert.Contains(t, output, "file-feature-middle-0.txt")
	assert.Contains(t, output, "file-feature-middle-1.txt")
	assert.Contains(t, output, "file-feature-middle-2.txt")

	// 4. The top branch should only show its unique commits (not feature-middle or feature-base commits)
	assert.Contains(t, output, "file-feature-top-0.txt")
	assert.Contains(t, output, "file-feature-top-1.txt")

	// 5. Verify separation - feature-base section should not contain feature-middle or feature-top commits
	baseSection := output[featureBasePos:]
	assert.NotContains(t, baseSection, "file-feature-middle", "Base branch section should not contain middle branch commits")
	assert.NotContains(t, baseSection, "file-feature-top", "Base branch section should not contain top branch commits")

	// 6. Verify separation - feature-middle section should not contain feature-top or feature-base commits
	middleSection := output[featureMiddlePos:featureBasePos]
	assert.NotContains(t, middleSection, "file-feature-top", "Middle branch section should not contain top branch commits")
	assert.NotContains(t, middleSection, "file-feature-base", "Middle branch section should not contain base branch commits")

	// 7. Verify separation - feature-top section should not contain feature-middle or feature-base commits
	topSection := output[featureTopPos:featureMiddlePos]
	assert.NotContains(t, topSection, "file-feature-middle", "Top branch section should not contain middle branch commits")
	assert.NotContains(t, topSection, "file-feature-base", "Top branch section should not contain base branch commits")

	// Run the list command with specific tower name
	cmdWithName := &LsCmd{TowerName: "stacked-tower"}
	outputWithName := runLsCommandWithConfig(t, config, cmdWithName)

	// Verify filtering by name produces the same output
	assert.Equal(t, output, outputWithName, "Filtering by tower name should produce the same output")

	// Test case for when the tower has no base commit set
	config.Repos[0].Towers[0].Base = ""
	// Rerun with modified config
	outputNoBase := runLsCommandWithConfig(t, config, cmd)

	// Verify output without base
	assert.Contains(t, outputNoBase, "Tower: stacked-tower")
	assert.NotContains(t, outputNoBase, "(base)", "Output should not contain base marker when no base is set")
}

func TestLsCmd_MiddleBranchDivergence(t *testing.T) {
	// Setup test environment and get repo object
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Get the initial main branch reference
	headRef, err := repo.Head()
	require.NoError(t, err)
	mainHash := headRef.Hash()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Step 1: Create the base branch with 3 commits (branches off main/HEAD)
	createTestBranch(t, repo, "base-branch", 3)

	// Step 2: Create branch-2 (middle-branch) off base branch's HEAD, with 2 commits
	// Checkout base-branch first
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("base-branch"),
	})
	require.NoError(t, err)
	createTestBranch(t, repo, "middle-branch", 2)

	// Get middle branch reference (this is the ref *before* the divergent commit)
	middleRef, err := repo.Reference(plumbing.NewBranchReferenceName("middle-branch"), true)
	require.NoError(t, err)

	// Step 3: Create branch-3 (top-branch) off the original branch-2's HEAD, with 3 commits
	// Checkout the specific commit where middle-branch ended before divergence
	err = wt.Checkout(&git.CheckoutOptions{
		Hash:   middleRef.Hash(), // Branch off the *original* middle-branch HEAD
		Create: false,            // Don't create a new branch, just checkout the hash
	})
	require.NoError(t, err)
	// Now create top-branch from this point
	createTestBranch(t, repo, "top-branch", 3)

	// Step 4: Checkout branch-2 again (latest HEAD) to add the divergent commit
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("middle-branch"),
	})
	require.NoError(t, err)

	// Add divergent commit to middle branch
	addSingleCommit(t, repoPath, wt, "divergent-middle.txt", "divergent content", "Divergent commit on middle branch")

	// Initialize config with one tower and all branches in order
	towerName := "test-tower"
	towers := []*Tower{
		{
			Name: towerName,
			// Base is set via createTestConfig
			Branches: []Branch{
				{Name: "base-branch"},
				{Name: "middle-branch"},
				{Name: "top-branch"},
			},
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, mainHash.String())

	// Run the list command
	cmd := &LsCmd{}
	output := runLsCommandWithConfig(t, config, cmd)

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")

	// Check for divergence warning
	assert.Contains(t, output, "⚠️ This branch has diverged", "Output should contain divergence warning")

	// Verify the top branch shows divergence from middle branch
	topPos := strings.Index(output, "top-branch\n")
	assert.True(t, topPos != -1, "Top branch should be in the output")
	middlePos := strings.Index(output, "middle-branch (current)\n")
	assert.True(t, middlePos != -1, "Middle branch should be in the output")
	divergencePos := strings.Index(output, "⚠️ This branch has diverged")
	assert.True(t, divergencePos != -1, "Divergence warning should be in the output")

	assert.True(t, topPos < divergencePos, "Divergence warning should appear in top branch section")
	assert.True(t, divergencePos < middlePos, "Divergence warning should appear before middle branch section")

	// Verify the divergent commit message is in the output
	assert.Contains(t, output, "Divergent commit on middle branch", "Divergent commit should be shown")
}

func TestLsCmd_TopBranchDivergence(t *testing.T) {
	// Setup test environment and get repo object
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Get the initial main branch reference
	headRef, err := repo.Head()
	require.NoError(t, err)
	mainHash := headRef.Hash()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Step 1: Create the base branch with 3 commits
	createTestBranch(t, repo, "base-branch", 3)

	// Step 2: Create branch-2 (middle-branch) off base branch's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base-branch")})
	require.NoError(t, err)
	createTestBranch(t, repo, "middle-branch", 2)
	middleRef, err := repo.Reference(plumbing.NewBranchReferenceName("middle-branch"), true)
	require.NoError(t, err)

	// Step 3: Create branch-3 (top-branch) off branch-2's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{Hash: middleRef.Hash()}) // Checkout original middle branch HEAD
	require.NoError(t, err)
	createTestBranch(t, repo, "top-branch", 2)

	// Step 4: Go back to top branch and add another commit (creates divergence)
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("top-branch"),
	})
	require.NoError(t, err)
	addSingleCommit(t, repoPath, wt, "divergent-top.txt", "divergent content", "Divergent commit on top branch")

	// Step 5: Go back to middle branch and add another commit
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("middle-branch"),
	})
	require.NoError(t, err)
	addSingleCommit(t, repoPath, wt, "another-middle.txt", "another middle content", "Another commit on middle branch")

	// Initialize config with one tower and all branches in order
	towerName := "test-tower"
	towers := []*Tower{
		{
			Name: towerName,
			// Base is set via createTestConfig
			Branches: []Branch{
				{Name: "base-branch"},
				{Name: "middle-branch"},
				{Name: "top-branch"},
			},
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, mainHash.String())

	// Run the list command
	cmd := &LsCmd{}
	output := runLsCommandWithConfig(t, config, cmd)

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")

	// Check for divergence warning
	assert.Contains(t, output, "⚠️ This branch has diverged", "Output should contain divergence warning")

	// Verify the top branch shows divergence
	topPos := strings.Index(output, "top-branch\n")
	assert.True(t, topPos != -1, "Top branch should be in the output")
	middlePos := strings.Index(output, "middle-branch (current)\n")
	assert.True(t, middlePos != -1, "Middle branch should be in the output")
	divergencePos := strings.Index(output, "⚠️ This branch has diverged")
	assert.True(t, divergencePos != -1, "Divergence warning should be in the output")

	assert.True(t, topPos < divergencePos, "Divergence warning should appear in top branch section")
	assert.True(t, divergencePos < middlePos, "Divergence warning should appear before middle branch section")

	// Verify the divergent commit messages are in the output
	assert.Contains(t, output, "Divergent commit on top branch", "Top branch divergent commit should be shown")
	assert.Contains(t, output, "Another commit on middle branch", "Middle branch additional commit should be shown")
}

func TestLsCmd_MultipleDivergences(t *testing.T) {
	// Setup test environment and get repo object
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Get the initial main branch reference
	headRef, err := repo.Head()
	require.NoError(t, err)
	mainHash := headRef.Hash()

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Step 1: Create the base branch with 3 commits
	createTestBranch(t, repo, "base-branch", 3)

	// Step 2: Create branch-2 (middle-branch) off base branch's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("base-branch")})
	require.NoError(t, err)
	createTestBranch(t, repo, "middle-branch", 2)
	middleRef, err := repo.Reference(plumbing.NewBranchReferenceName("middle-branch"), true)
	require.NoError(t, err)

	// Step 3: Create branch-3 (third-branch) off branch-2's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{Hash: middleRef.Hash()}) // Checkout original middle branch HEAD
	require.NoError(t, err)
	createTestBranch(t, repo, "third-branch", 2)
	thirdRef, err := repo.Reference(plumbing.NewBranchReferenceName("third-branch"), true)
	require.NoError(t, err)

	// Step 4: Create branch-4 (top-branch) off branch-3's HEAD, with 2 commits
	err = wt.Checkout(&git.CheckoutOptions{Hash: thirdRef.Hash()}) // Checkout original third branch HEAD
	require.NoError(t, err)
	createTestBranch(t, repo, "top-branch", 2)

	// Step 5: Go back to middle-branch and add one more commit (creates first divergence)
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("middle-branch"),
	})
	require.NoError(t, err)
	addSingleCommit(t, repoPath, wt, "divergent-middle.txt", "divergent middle content", "Divergent commit on middle branch")

	// Step 6: Go back to top-branch and add another commit (creates second divergence)
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("top-branch"),
	})
	require.NoError(t, err)
	addSingleCommit(t, repoPath, wt, "divergent-top.txt", "divergent top content", "Divergent commit on top branch")

	// Step 7: Go back to third-branch and add another commit (completes second divergence)
	err = wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("third-branch"),
	})
	require.NoError(t, err)
	addSingleCommit(t, repoPath, wt, "another-third.txt", "another third content", "Another commit on third branch")

	// Initialize config with one tower and all branches in order
	towerName := "test-tower"
	towers := []*Tower{
		{
			Name: towerName,
			// Base is set via createTestConfig
			Branches: []Branch{
				{Name: "base-branch"},
				{Name: "middle-branch"},
				{Name: "third-branch"},
				{Name: "top-branch"},
			},
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, mainHash.String())

	// Run the list command
	cmd := &LsCmd{}
	output := runLsCommandWithConfig(t, config, cmd)

	// Verify output
	assert.Contains(t, output, "Tower: test-tower")

	// There should be two divergence warnings
	divergencesCount := strings.Count(output, "⚠️ This branch has diverged")
	assert.Equal(t, 2, divergencesCount, "Output should contain two divergence warnings")

	// Verify both divergent commits are highlighted
	assert.Contains(t, output, "Divergent commit on middle branch", "Middle branch divergent commit should be shown")
	assert.Contains(t, output, "Divergent commit on top branch", "Top branch divergent commit should be shown")

	// Verify branch order and placement of warnings
	topPos := strings.Index(output, "top-branch\n")
	assert.True(t, topPos != -1, "Top branch should be in the output")
	thirdPos := strings.Index(output, "third-branch (current)\n")
	assert.True(t, thirdPos != -1, "Third branch should be in the output")
	middlePos := strings.Index(output, "middle-branch\n")
	assert.True(t, middlePos != -1, "Middle branch should be in the output")
	basePos := strings.Index(output, "base-branch\n")
	assert.True(t, basePos != -1, "Base branch should be in the output")

	// Find positions of divergence warnings
	firstWarningPos := strings.Index(output, "⚠️ This branch has diverged")
	secondWarningPos := strings.Index(output[firstWarningPos+1:], "⚠️ This branch has diverged") + firstWarningPos + 1

	// Verify warnings appear in the right sections
	assert.True(t, topPos < firstWarningPos && firstWarningPos < thirdPos,
		"First divergence warning should be in top branch section")
	assert.True(t, thirdPos < secondWarningPos && secondWarningPos < middlePos,
		"Second divergence warning should be in middle branch section")
}

func TestLsCmd_BaseBranchMissingWarning(t *testing.T) {
	// Setup test environment and get repo object
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Create branches
	baseBranchName := "base-branch"
	featureBranchName := "feature-branch"
	createTestBranch(t, repo, baseBranchName, 1)
	createTestBranch(t, repo, featureBranchName, 1)

	// Initialize config with the tower
	towerName := "test-tower"
	towers := []*Tower{
		{
			Name: towerName,
			Branches: []Branch{
				{Name: baseBranchName},
				{Name: featureBranchName},
			},
		},
	}
	config := createTestConfig(t, repoPath, towerName, towers, "")

	// Delete the base branch from the git repository
	err := repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(baseBranchName))
	require.NoError(t, err)

	// Run the list command
	output := runLsCommandWithConfig(t, config, &LsCmd{})

	// Verify output contains the warning
	assert.Contains(t, output, "⚠️ Warning: The base branch is no longer valid")
}
