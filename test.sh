#!/bin/bash

# Clean test runner that suppresses output unless tests fail
#
# Usage:
#   ./test.sh                          # Run all tests with clean output
#   ./test.sh -v                       # Force verbose output
#   ./test.sh TestLand*                # Run specific test pattern
#   ./test.sh -run=TestLand_HappyPath  # Run specific test

set -e

# Check for verbose flag
VERBOSE=false
TEST_ARGS=""

for arg in "$@"; do
    case $arg in
        -v|--verbose)
            VERBOSE=true
            shift
            ;;
        *)
            TEST_ARGS="$TEST_ARGS $arg"
            ;;
    esac
done

# If verbose flag is set, just run tests normally
if [ "$VERBOSE" = true ]; then
    echo "Running tests with verbose output..."
    go test -v $TEST_ARGS ./...
    exit $?
fi

echo "Running tests..."

# Run tests without verbose output first
if go test $TEST_ARGS ./... > /tmp/test_output.txt 2>&1; then
    # Tests passed - show brief output
    cat /tmp/test_output.txt
    echo "✅ All tests passed!"
else
    # Tests failed - show only the failure summary, not all the t.Log() output
    echo "❌ Tests failed! Failure summary:"
    grep -E "(^FAIL|^--- FAIL:|^    [^[:space:]].*\.go:[0-9]+:)" /tmp/test_output.txt || cat /tmp/test_output.txt
    echo ""
    
    # Extract failed test names from the output
    FAILED_TESTS=$(grep "^--- FAIL:" /tmp/test_output.txt | sed 's/^--- FAIL: \([^ ]*\).*/\1/' | tr '\n' '|' | sed 's/|$//')
    
    if [ -n "$FAILED_TESTS" ]; then
        echo "Running only failed tests with verbose output for debugging:"
        echo "Failed tests: $(echo $FAILED_TESTS | tr '|' ' ')"
        echo "============================================================"
        # Run only the failed tests with verbose output
        go test -v -run="^($FAILED_TESTS)$" $TEST_ARGS ./...
    else
        echo "Could not determine which tests failed. Running all tests with verbose output:"
        echo "============================================================================="
        go test -v $TEST_ARGS ./...
    fi
fi

# Clean up
rm -f /tmp/test_output.txt
