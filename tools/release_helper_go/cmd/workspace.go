package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrWorkspaceRootNotFound is wrapped into findWorkspaceRoot's returned error
// so callers that know *why* they need a workspace root (e.g.
// autoIncrementVersion's git-tag fallback) can distinguish "no monorepo
// checkout available at all" from any other git/bazel failure and give a
// more actionable error than this package's own generic message below.
var ErrWorkspaceRootNotFound = errors.New("workspace root not found")

func findWorkspaceRoot() (string, error) {
	if dir := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); dir != "" {
		return dir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "MODULE.bazel")); err == nil {
			return dir, nil
		}
		if _, err := os.Stat(filepath.Join(dir, "WORKSPACE")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("%w from %s", ErrWorkspaceRootNotFound, cwd)
}
