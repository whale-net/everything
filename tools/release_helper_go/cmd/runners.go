package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

type realBazelRunner struct {
	workspaceRoot string
}

// bazelTransientErrorSubstrings mark a bazel failure as a remote-cache/
// network flake worth retrying rather than a real build error -- e.g.
// "lost inputs with digests" (observed in GH Actions run 32689827812/job/
// 97321529487: the remote cache evicted an already-uploaded blob mid-build,
// which a bare network-level retry inside bazel's own gRPC client can't fix
// since the blob itself is gone; only re-running the whole invocation, which
// makes bazel recompute the missing input, recovers).
var bazelTransientErrorSubstrings = []string{
	"lost inputs",
	"connection reset by peer",
	"i/o timeout",
	"unexpected eof",
	"tls handshake timeout",
	"connection refused",
	"no route to host",
	"broken pipe",
	"temporary failure in name resolution",
}

func isTransientBazelError(errText string) bool {
	lower := strings.ToLower(errText)
	for _, substr := range bazelTransientErrorSubstrings {
		if strings.Contains(lower, substr) {
			return true
		}
	}
	return false
}

const (
	bazelRetryAttempts = 3
	bazelRetryDelay    = 5 * time.Second
)

func (r *realBazelRunner) Run(args ...string) (string, error) {
	start := time.Now()
	var out string
	var err error
	for attempt := 1; attempt <= bazelRetryAttempts; attempt++ {
		out, err = r.runOnce(args...)
		if err == nil {
			logBazelDuration(args, time.Since(start))
			return out, nil
		}
		if attempt == bazelRetryAttempts || !isTransientBazelError(err.Error()) {
			logBazelDuration(args, time.Since(start))
			return out, err
		}
		fmt.Fprintf(os.Stderr, "bazel %s: transient failure (attempt %d/%d, %s elapsed), retrying in %s: %v\n",
			strings.Join(args, " "), attempt, bazelRetryAttempts, time.Since(start).Round(time.Second), bazelRetryDelay, err)
		time.Sleep(bazelRetryDelay)
	}
	return out, err
}

// shouldStreamBazelOutput reports whether a bazel invocation is worth
// streaming live and timing: "build" and "run" are the subcommands that
// actually compile/cross-compile/push artifacts and can run for minutes
// (see the app-image and chart/openapi/CLI-binary build paths); "query",
// "cquery", and "info" return in milliseconds and would just add noise.
func shouldStreamBazelOutput(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "run", "build":
		return true
	default:
		return false
	}
}

// logBazelDuration reports how long a slow (build/run) bazel invocation
// took, so a CI log shows where time actually went instead of one opaque
// "Building and pushing..." line followed by silence for minutes.
func logBazelDuration(args []string, d time.Duration) {
	if !shouldStreamBazelOutput(args) {
		return
	}
	fmt.Fprintf(os.Stderr, "bazel %s finished in %s\n", strings.Join(args, " "), d.Round(time.Second))
}

func (r *realBazelRunner) runOnce(args ...string) (string, error) {
	cmd := exec.Command("bazel", args...)
	cmd.Dir = r.workspaceRoot
	var stdout, stderr bytes.Buffer
	if shouldStreamBazelOutput(args) {
		// bazel's own progress output (action counts, cache hits/misses,
		// remote-execution/network stalls) previously went straight into
		// these buffers and was discarded on success -- a slow or hung
		// build/push was silent in CI until it finished or errored. Tee it
		// live to the process's own stdout/stderr as well, so it shows up
		// in the CI step log in real time.
		cmd.Stdout = io.MultiWriter(&stdout, os.Stdout)
		cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)
	} else {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}
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

type realDockerRunner struct{}

func (r *realDockerRunner) Run(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		return out, fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func init() {
	// Lazily initialise real runners. They will call findWorkspaceRoot() on
	// first use; here we just set up sentinel structs so the package-level vars
	// are non-nil by the time any command runs.
	defaultBazel = &lazyBazelRunner{}
	defaultGit = &lazyGitRunner{}
	defaultDocker = &realDockerRunner{}
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
