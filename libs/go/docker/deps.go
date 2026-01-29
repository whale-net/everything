// Package docker_deps imports transitive dependencies of github.com/docker/docker
// to ensure they are added to MODULE.bazel's use_repo list.
//
// This file exists solely to force Bazel to include Docker's transitive dependencies
// in the build graph. These packages are required by the Docker client library but
// are not directly imported by our code.
//
// DO NOT DELETE THIS FILE - it ensures Docker library builds correctly in Bazel.
package docker

import (
	// Docker client transitive dependencies
	_ "github.com/containerd/errdefs"
	_ "github.com/distribution/reference"
	_ "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	_ "go.opentelemetry.io/otel/trace"
)
