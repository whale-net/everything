package cmd

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

type realBazelRunner struct {
	workspaceRoot string
}

func (r *realBazelRunner) Run(args ...string) (string, error) {
	cmd := exec.Command("bazel", args...)
	cmd.Dir = r.workspaceRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		// Surface any stdout Bazel produced before failing (e.g. partial
		// query/cquery output) alongside the wrapped error, so callers and
		// error messages have as much context as possible.
		return out, fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

type realGitRunner struct {
	workspaceRoot string
}

func (r *realGitRunner) Run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.workspaceRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

func init() {
	// Lazily initialise real runners. They will call findWorkspaceRoot() on
	// first use; here we just set up sentinel structs so the package-level vars
	// are non-nil by the time any command runs.
	defaultBazel = &lazyBazelRunner{}
	defaultGit = &lazyGitRunner{}
}

// lazyBazelRunner resolves the workspace root on first call.
type lazyBazelRunner struct{ inner *realBazelRunner }

func (l *lazyBazelRunner) Run(args ...string) (string, error) {
	if l.inner == nil {
		root, err := findWorkspaceRoot()
		if err != nil {
			return "", err
		}
		l.inner = &realBazelRunner{workspaceRoot: root}
	}
	return l.inner.Run(args...)
}

// lazyGitRunner resolves the workspace root on first call.
type lazyGitRunner struct{ inner *realGitRunner }

func (l *lazyGitRunner) Run(args ...string) (string, error) {
	if l.inner == nil {
		root, err := findWorkspaceRoot()
		if err != nil {
			return "", err
		}
		l.inner = &realGitRunner{workspaceRoot: root}
	}
	return l.inner.Run(args...)
}
