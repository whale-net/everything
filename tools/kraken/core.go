package kraken

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FindWorkspaceRoot locates the Bazel workspace root directory.
// It checks BUILD_WORKSPACE_DIRECTORY env var first, then walks up
// looking for WORKSPACE or MODULE.bazel markers.
func FindWorkspaceRoot() (string, error) {
	if dir := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); dir != "" {
		return dir, nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working directory: %w", err)
	}

	dir := cwd
	for {
		for _, marker := range []string{"WORKSPACE", "MODULE.bazel"} {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Fallback to current directory
	return cwd, nil
}

// BazelResult holds the output of a bazel command.
type BazelResult struct {
	Stdout string
	Stderr string
}

// RunBazel executes a bazel command with the given arguments.
// It runs from the workspace root directory.
func RunBazel(args []string, captureOutput bool, env []string) (*BazelResult, error) {
	workspaceRoot, err := FindWorkspaceRoot()
	if err != nil {
		return nil, fmt.Errorf("finding workspace root: %w", err)
	}

	cmdArgs := append([]string{}, args...)
	cmd := exec.Command("bazel", cmdArgs...)
	cmd.Dir = workspaceRoot

	if env != nil {
		cmd.Env = env
	} else {
		cmd.Env = os.Environ()
	}

	if captureOutput {
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("bazel command failed: %s\nWorking directory: %s\nstderr: %s\nstdout: %s\n%w",
				strings.Join(append([]string{"bazel"}, args...), " "),
				workspaceRoot,
				stderr.String(),
				stdout.String(),
				err,
			)
		}

		return &BazelResult{
			Stdout: stdout.String(),
			Stderr: stderr.String(),
		}, nil
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("bazel command failed: %s\nWorking directory: %s\n%w",
			strings.Join(append([]string{"bazel"}, args...), " "),
			workspaceRoot,
			err,
		)
	}

	return &BazelResult{}, nil
}
