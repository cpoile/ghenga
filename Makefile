# Makefile for ghenga project

.PHONY: test test-verbose test-clean help

# Default target
help:
	@echo "Available targets:"
	@echo "  test         - Run tests with clean output (only shows failures with verbose details)"
	@echo "  test-verbose - Run tests with full verbose output"
	@echo "  test-clean   - Run tests with minimal output (pass/fail only)"
	@echo ""
	@echo "Examples:"
	@echo "  make test                    # Clean test run (recommended)"
	@echo "  make test-verbose           # Full verbose output"
	@echo "  ./test.sh -run=TestLand*    # Run specific test pattern"
	@echo "  ./test.sh -v                # Force verbose for all tests"

# Clean test run (shows only failed tests with verbose output)
test:
	@./test.sh

# Force verbose output for all tests
test-verbose:
	@./test.sh -v

# Minimal output (just pass/fail)
test-clean:
	@go test ./... > /dev/null && echo "✅ All tests passed!" || (echo "❌ Tests failed!" && exit 1)
