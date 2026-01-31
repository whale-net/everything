# Bazel Test Runner Skill

A lightweight Haiku-powered sub-agent that runs Bazel tests and reports results without polluting the parent context.

## Usage

Invoke this skill in any of these ways:

```bash
/test-bazel
```

Or Claude may automatically invoke it when you ask to run tests:
- "Run the tests"
- "Check if tests pass"
- "Run bazel tests"

## How It Works

1. **Forked Context**: Runs in an isolated sub-agent to keep parent context clean
2. **Haiku Model**: Uses Claude Haiku for speed and efficiency
3. **Bash Agent**: Specialized for running commands
4. **Automatic Reporting**: Returns only the summary to parent

## Benefits

- **Clean Context**: Test output stays in sub-agent, parent sees only summary
- **Fast**: Haiku model processes test results quickly
- **Cost-Effective**: Lower token usage for repetitive test runs
- **Reusable**: Invoke anytime with `/test-bazel`

## Example Output

When successful:
```
✓ OK - All tests passed
Total: 42 test targets
```

When failures occur:
```
✗ FAILED - 3 test target(s) failed

Failed targets:
- //manman/core:core_test - NullPointerException in processRequest
- //manman/api:api_test - Timeout after 60s
- //common/utils:utils_test - AssertionError: expected 5, got 3

Action required:
1. Fix NPE in manman/core/request_handler.go:123
2. Increase timeout or optimize api test
3. Update assertion in utils_test.go:45
```
