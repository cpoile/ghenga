package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

// TestCherryPickWithRetryLockFile tests the git lock file retry logic directly
func TestCherryPickWithRetryLockFile(t *testing.T) {
	repoPath, repo, cleanup := setupTestEnv(t)
	defer cleanup()

	// Create a test branch with a commit to cherry-pick
	createTestBranch(t, repo, "test-branch", 1)
	testBranchRef, err := repo.Reference(plumbing.NewBranchReferenceName("test-branch"), true)
	require.NoError(t, err)
	testCommitHash := testBranchRef.Hash().String()

	// Create fake git directory and script
	fakeGitDir := filepath.Join(repoPath, "fake-git-bin")
	err = os.MkdirAll(fakeGitDir, 0755)
	require.NoError(t, err)

	fakeGitPath := filepath.Join(fakeGitDir, "git")
	logFilePath := filepath.Join(repoPath, "fake-git-calls.log")
	counterFilePath := filepath.Join(repoPath, "cherry-pick-attempts")

	// Create fake git script that simulates lock file errors for the first few attempts
	fakeGitScript := `#!/bin/bash
# Fake git script that simulates lock file errors for cherry-pick commands

# Log file to track all git calls
LOG_FILE="` + logFilePath + `"
echo "$(date): fake git called with args: $* (pwd: $(pwd))" >> "$LOG_FILE"

# Counter file to track cherry-pick attempts
COUNTER_FILE="` + counterFilePath + `"

# Find real git by temporarily removing our directory from PATH
ORIGINAL_PATH="$PATH"
NEW_PATH=$(echo "$PATH" | sed 's|` + fakeGitDir + `:||g' | sed 's|:` + fakeGitDir + `||g' | sed 's|` + fakeGitDir + `||g')
export PATH="$NEW_PATH"
REAL_GIT=$(which git)
export PATH="$ORIGINAL_PATH"

# Fallback to common locations if which failed
if [ -z "$REAL_GIT" ] || [ ! -x "$REAL_GIT" ]; then
    for candidate in /usr/bin/git /usr/local/bin/git /opt/homebrew/bin/git; do
        if [ -x "$candidate" ]; then
            REAL_GIT="$candidate"
            break
        fi
    done
fi

echo "$(date): using real git at: $REAL_GIT" >> "$LOG_FILE"

if [ "$1" = "cherry-pick" ]; then
    echo "$(date): cherry-pick command detected with commit: $2" >> "$LOG_FILE"
    
    # Read current attempt count, default to 0
    if [ -f "$COUNTER_FILE" ]; then
        ATTEMPTS=$(cat "$COUNTER_FILE")
    else
        ATTEMPTS=0
    fi
    
    # Increment attempt count
    ATTEMPTS=$((ATTEMPTS + 1))
    echo "$ATTEMPTS" > "$COUNTER_FILE"
    echo "$(date): this is attempt #$ATTEMPTS" >> "$LOG_FILE"
    
    # Return lock file error for first 3 attempts
    if [ "$ATTEMPTS" -le 3 ]; then
        echo "$(date): simulating lock file error for attempt $ATTEMPTS" >> "$LOG_FILE"
        echo "error: Unable to create '/path/to/repo/.git/index.lock': File exists." >&2
        echo "Another git process seems to be running in this repository, e.g." >&2
        echo "an editor opened by 'git commit'. Please make sure all processes" >&2
        echo "are terminated then try again. If it still fails, a git process" >&2
        echo "may have crashed in this repository earlier:" >&2
        echo "remove the file manually to continue." >&2
        echo "fatal: cherry-pick failed" >&2
        exit 1
    fi
    
    # On 4th attempt and beyond, simulate success
    echo "$(date): attempt $ATTEMPTS - simulating success" >> "$LOG_FILE"
    echo "Successfully cherry-picked commit $2" >&1
    exit 0
else
    # For all other commands, delegate to real git
    echo "$(date): delegating '$1' command to real git" >> "$LOG_FILE"
    exec "$REAL_GIT" "$@"
fi
`

	err = os.WriteFile(fakeGitPath, []byte(fakeGitScript), 0755)
	require.NoError(t, err)

	// Temporarily modify PATH to use fake git
	originalPath := os.Getenv("PATH")
	newPath := fakeGitDir + ":" + originalPath
	err = os.Setenv("PATH", newPath)
	require.NoError(t, err)
	defer func() {
		os.Setenv("PATH", originalPath) // Restore original PATH
	}()

	// Verify fake git is found first
	whichCmd := exec.Command("which", "git")
	whichOutput, err := whichCmd.Output()
	require.NoError(t, err)
	foundGit := strings.TrimSpace(string(whichOutput))
	require.Equal(t, fakeGitPath, foundGit, "Fake git should be found first in PATH")

	// Test that fake git responds to cherry-pick with lock error initially
	testCmd := exec.Command("git", "cherry-pick", "test-commit")
	testCmd.Dir = repoPath
	testOutput, testErr := testCmd.CombinedOutput()
	require.Error(t, testErr, "First cherry-pick should fail with lock error")
	require.Contains(t, string(testOutput), "index.lock", "Should contain lock file error message")

	// Verify fake git was called and logged
	logData, err := os.ReadFile(logFilePath)
	require.NoError(t, err, "Log file should exist after test call")
	logContent := string(logData)
	require.Contains(t, logContent, "cherry-pick command detected", "Log should show cherry-pick was detected")
	t.Logf("Log content after initial test:\n%s", logContent)

	// Reset the counter for the actual test
	err = os.Remove(counterFilePath)
	require.NoError(t, err)

	// Now test the actual runGitCommandWithRetry function (which cherryPickWithRetry uses)
	t.Log("Testing runGitCommandWithRetry function...")

	// Try to cherry-pick the test commit using our retry logic
	// This should succeed after a few retries as our fake git will return lock errors for first 3 attempts
	output, err := runGitCommandWithRetry(repoPath, "cherry-pick", testCommitHash)

	// The function should eventually succeed
	require.NoError(t, err, "runGitCommandWithRetry should succeed after retries")
	require.NotEmpty(t, output, "Should have some output from successful cherry-pick")

	// Verify the retry attempts were made
	if counterData, counterErr := os.ReadFile(counterFilePath); counterErr == nil {
		attempts := strings.TrimSpace(string(counterData))
		t.Logf("Total cherry-pick attempts made: %s", attempts)
		// We expect 4 attempts: 3 failures + 1 success
		require.Equal(t, "4", attempts, "Should have made exactly 4 attempts (3 failures + 1 success)")
	} else {
		t.Fatalf("Counter file not found or couldn't be read: %v", counterErr)
	}

	// Verify the log shows all the retry attempts
	finalLogData, err := os.ReadFile(logFilePath)
	require.NoError(t, err)
	finalLogContent := string(finalLogData)
	t.Logf("Final log content:\n%s", finalLogContent)

	// Count the number of cherry-pick attempts in the log
	cherryPickCount := strings.Count(finalLogContent, "cherry-pick command detected")
	require.GreaterOrEqual(t, cherryPickCount, 4, "Should have at least 4 cherry-pick attempts logged (1 initial test + 4 retry test)")

	// Verify we see the retry attempts
	require.Contains(t, finalLogContent, "attempt #1", "Should show first attempt")
	require.Contains(t, finalLogContent, "attempt #2", "Should show second attempt")
	require.Contains(t, finalLogContent, "attempt #3", "Should show third attempt")
	require.Contains(t, finalLogContent, "attempt #4", "Should show fourth attempt")
	require.Contains(t, finalLogContent, "simulating success", "Should eventually simulate success")

	t.Log("✅ Git lock file retry logic is working correctly!")
	t.Log("✅ runGitCommandWithRetry successfully handled lock file errors with exponential backoff")
	t.Log("✅ This generic function can now be used by any git command that might hit lock file errors")
}
