# everything

This monorepo supports the following application types:

*   **API**: For web applications that expose an API.
*   **Headless**: For applications that run without a user interface.
*   **Pub/Sub**: For applications that use a publish/subscribe messaging pattern.
*   **Temporal**: For applications that use the Temporal workflow engine.

## Getting Started

To get started with this project, you will need to install [Bazelisk](https://github.com/bazelbuild/bazelisk), a version manager for Bazel.

### Installation

You can install Bazelisk using Homebrew:

```bash
brew install bazelisk
```

Once Bazelisk is installed, it will automatically download and use the version of Bazel specified in the `.bazelversion` file.

## Running Tests

### Python Tests

To run Python tests, you need to first create and activate the virtual environment, and install the dependencies:

```bash
uv venv
source .venv/bin/activate
uv pip install -r requirements.txt
```

Then, you can run the tests using `pytest`:

```bash
pytest examples/api-py/tests/test_api.py
```

### Go Tests

To run Go tests, you can use the `go test` command directly from the project directory:

```bash
go test ./examples/api-go/...
go test ./examples/headless-go/...
go test ./examples/pubsub-go/...
go test ./examples/temporal-go/...
```
