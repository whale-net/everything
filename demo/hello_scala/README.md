# Scala Support

This directory contains the Scala hello world demo application.

## Structure

- `Main.scala` - Main application entry point
- `MainTest.scala` - ScalaTest unit tests
- `BUILD.bazel` - Bazel build configuration

## Building and Running

### Prerequisites

Scala support requires:
- `rules_scala` (configured in MODULE.bazel)
- Local JDK installation
- Network access to Maven Central and Bazel mirrors (for dependency downloads)

### Build

```bash
bazel build //demo/hello_scala:hello-scala
```

### Run

```bash
bazel run //demo/hello_scala:hello-scala
```

### Test

```bash
bazel test //demo/hello_scala:main_test
```

## Network Restrictions

**Note**: Building Scala applications requires downloading dependencies from:
- `cdn.azul.com` (for JDK)
- `mirror.bazel.build` (for Bazel Java tools)
- `repo.maven.apache.org` (for Scala libraries)

If these domains are blocked in your environment, you may need to:
1. Configure a proxy
2. Use pre-downloaded dependencies
3. Configure Bazel to use local mirrors

For CI environments with network restrictions, consider using a container with pre-downloaded dependencies or configuring Bazel's repository cache.

## Release Support

The `release_app` macro in the BUILD.bazel file configures this app for:
- Container image generation
- Multi-platform builds (AMD64/ARM64)
- Release automation

See the root README.md for more information on the release system.
