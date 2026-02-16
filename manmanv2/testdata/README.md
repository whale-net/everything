# Wrapper Integration Tests

This directory contains the test infrastructure for wrapper integration tests.

## Test Game Server

The test game server (`test_game_server.sh`) is a simple shell script that simulates a game server for testing purposes.

### Features

- Prints predictable output to stdout and stderr
- Accepts stdin commands:
  - `stop` - Graceful shutdown (exit 0)
  - `crash` - Simulate crash (exit 42)
  - `ping` - Responds with "pong"
  - `echo <message>` - Echoes the message back
- Handles SIGTERM for graceful shutdown
- Prints heartbeat messages every 2 seconds

### Building the Test Container

Build the test container image before running integration tests:

```bash
cd manman/wrapper/testdata
docker build -t manman-test-game-server:latest .
```

## Running Integration Tests

### Using Bazel

```bash
# Build the test image first
cd manman/wrapper/testdata
docker build -t manman-test-game-server:latest .

# Run integration tests
cd ../../..  # Back to repo root
bazel test //manman/wrapper:wrapper_integration_test --test_tag_filters=integration
```

### Using Go directly

```bash
# Build the test image first
cd manman/wrapper/testdata
docker build -t manman-test-game-server:latest .

# Run tests
cd ..
go test -tags=integration -v .
```

## Test Coverage

The integration tests cover:

1. **Start** - Starting containers with valid and invalid images
2. **Stop** - Graceful and force stop
3. **Status** - Querying container status (running and stopped)
4. **Logs** - Streaming stdout and stderr separately and together
5. **Stdin** - Sending input to containers (edge cases)

## Requirements

- Docker must be running locally
- The test image must be built before running tests
- Tests run in a Bazel sandbox with temporary directories

## CI Integration

These tests are currently manual (`tags = ["manual"]`) because CI doesn't have Docker support yet. When Docker is added to CI, remove the "manual" tag from the BUILD.bazel file.
