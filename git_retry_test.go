package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRunGitCommandWithRetryGeneric tests the generic git retry function with different commands
func TestRunGitCommandWithRetryGeneric(t *testing.T) {
	repoPath, _, cleanup := setupTestEnv(t)
	defer cleanup()

	// Create fake git directory and script
	fakeGitDir := filepath.Join(repoPath, "fake-git-bin")
	err := os.MkdirAll(fakeGitDir, 0755)
	require.NoError(t, err)

	fakeGitPath := filepath.Join(fakeGitDir, "git")
	logFilePath := filepath.Join(repoPath, "fake-git-calls.log")
	counterFilePath := filepath.Join(repoPath, "git-attempts")

	// Create fake git script that simulates lock file errors for different commands
	fakeGitScript := `#!/bin/bash
LOG_FILE="` + logFilePath + `"
echo "$(date): fake git called: $*" >> "$LOG_FILE"

COUNTER_FILE="` + counterFilePath + `"

# Find real git
ORIGINAL_PATH="$PATH"
NEW_PATH=$(echo "$PATH" | sed 's|` + fakeGitDir + `:||g')
export PATH="$NEW_PATH"
REAL_GIT=$(which git)
export PATH="$ORIGINAL_PATH"

if [ -z "$REAL_GIT" ]; then
    REAL_GIT=/usr/bin/git
fi

# Check if this is a command we want to simulate lock errors for
if [ "$1" = "fetch" ] || [ "$1" = "reset" ] || [ "$1" = "checkout" ]; then
    echo "$(date): simulating lock error for: $1" >> "$LOG_FILE"
    
    # Read attempt count
    if [ -f "$COUNTER_FILE" ]; then
        ATTEMPTS=$(cat "$COUNTER_FILE")
    else
        ATTEMPTS=0
    fi
    
    ATTEMPTS=$((ATTEMPTS + 1))
    echo "$ATTEMPTS" > "$COUNTER_FILE"
    
    # Fail first 2 attempts with lock error
    if [ "$ATTEMPTS" -le 2 ]; then
        echo "error: Unable to create '.git/index.lock': File exists." >&2
        echo "Another git process seems to be running" >&2
        exit 1
    fi
    
    # Succeed on 3rd attempt
    echo "$(date): simulating success for: $1" >> "$LOG_FILE"
    echo "Success: $*"
    exit 0
else
    # For other commands, delegate to real git
    exec "$REAL_GIT" "$@"
fi
`

	err = os.WriteFile(fakeGitPath, []byte(fakeGitScript), 0755)
	require.NoError(t, err)

	// Modify PATH
	originalPath := os.Getenv("PATH")
	newPath := fakeGitDir + ":" + originalPath
	err = os.Setenv("PATH", newPath)
	require.NoError(t, err)
	defer os.Setenv("PATH", originalPath)

	// Test different git commands with retry logic
	testCases := []struct {
		name string
		args []string
	}{
		{"fetch", []string{"fetch", "origin"}},
		{"reset", []string{"reset", "--hard", "HEAD"}},
		{"checkout", []string{"checkout", "main"}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset counter for each test
			os.Remove(counterFilePath)

			// Test the git command with retry
			output, err := runGitCommandWithRetry(repoPath, tc.args...)
			require.NoError(t, err, "runGitCommandWithRetry should succeed for %s", tc.name)
			require.Contains(t, string(output), "Success", "Should get success output")

			// Verify retries were made
			counterData, err := os.ReadFile(counterFilePath)
			require.NoError(t, err)
			attempts := strings.TrimSpace(string(counterData))
			require.Equal(t, "3", attempts, "Should have made 3 attempts (2 failures + 1 success)")

			t.Logf("✅ %s command succeeded after retries", tc.name)
		})
	}

	// Verify log shows all commands were tested
	logData, err := os.ReadFile(logFilePath)
	require.NoError(t, err)
	logContent := string(logData)

	require.Contains(t, logContent, "simulating lock error for: fetch")
	require.Contains(t, logContent, "simulating lock error for: reset")
	require.Contains(t, logContent, "simulating lock error for: checkout")

	t.Log("✅ Generic git retry function works for fetch, reset, and checkout commands")
	t.Log("✅ This covers the git commands used in updateLocalBranchFromRemote and cherryPickWithRetry")
}
