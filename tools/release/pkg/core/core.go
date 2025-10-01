// Package core provides core utilities for the release helper.
package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FindWorkspaceRoot finds the workspace root directory.
// It checks BUILD_WORKSPACE_DIRECTORY environment variable first,
// then looks for WORKSPACE or MODULE.bazel files in parent directories.
func FindWorkspaceRoot() (string, error) {
	// When run via bazel run, BUILD_WORKSPACE_DIRECTORY is set to the workspace root
	if workspaceDir := os.Getenv("BUILD_WORKSPACE_DIRECTORY"); workspaceDir != "" {
		return workspaceDir, nil
	}

	// When run directly, look for workspace markers
	currentDir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	dir := currentDir
	for {
		// Check for WORKSPACE or MODULE.bazel
		if fileExists(filepath.Join(dir, "WORKSPACE")) || fileExists(filepath.Join(dir, "MODULE.bazel")) {
			return dir, nil
		}

		// Move to parent directory
		parentDir := filepath.Dir(dir)
		if parentDir == dir {
			// Reached root without finding markers
			break
		}
		dir = parentDir
	}

	// As a last resort, return current directory
	return currentDir, nil
}

// RunBazel runs a bazel command with consistent configuration.
func RunBazel(args []string, captureOutput bool, env map[string]string) (*BazelResult, error) {
	workspaceRoot, err := FindWorkspaceRoot()
	if err != nil {
		return nil, fmt.Errorf("failed to find workspace root: %w", err)
	}

	cmdArgs := append([]string{}, args...)
	cmd := exec.Command("bazel", cmdArgs...)
	cmd.Dir = workspaceRoot

	// Set environment
	if env != nil {
		cmdEnv := os.Environ()
		for k, v := range env {
			cmdEnv = append(cmdEnv, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = cmdEnv
	}

	result := &BazelResult{
		Args: args,
	}

	if captureOutput {
		output, err := cmd.CombinedOutput()
		result.Stdout = string(output)
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exitErr.ExitCode()
			}
			return result, fmt.Errorf("bazel command failed: %w\nstdout/stderr: %s", err, string(output))
		}
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				result.ExitCode = exitErr.ExitCode()
			}
			return result, fmt.Errorf("bazel command failed: %w", err)
		}
	}

	result.ExitCode = 0
	return result, nil
}

// BazelResult represents the result of running a Bazel command.
type BazelResult struct {
	Args     []string
	Stdout   string
	ExitCode int
}

// Lines returns the output lines, trimming empty lines.
func (r *BazelResult) Lines() []string {
	if r.Stdout == "" {
		return []string{}
	}
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
