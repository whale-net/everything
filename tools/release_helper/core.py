"""
Core utilities for the release helper.
"""

import os
import subprocess
import sys
from pathlib import Path


def find_workspace_root() -> Path:
    """Find the workspace root directory."""
    # When run via bazel run, BUILD_WORKSPACE_DIRECTORY is set to the workspace root
    if "BUILD_WORKSPACE_DIRECTORY" in os.environ:
        return Path(os.environ["BUILD_WORKSPACE_DIRECTORY"])

    # When run directly, look for workspace markers
    current = Path.cwd()
    for path in [current] + list(current.parents):
        if (path / "WORKSPACE").exists() or (path / "MODULE.bazel").exists():
            return path

    # As a last resort, assume current directory
    return current


def run_bazel(args: list[str], capture_output: bool = True, env: dict = None, timeout: int = None) -> subprocess.CompletedProcess:
    """Run a bazel command with consistent configuration.
    
    Args:
        args: Bazel command arguments (e.g., ["build", "//target"])
        capture_output: Whether to capture stdout/stderr
        env: Optional environment variables
        timeout: Optional timeout in seconds (default: 600 for long builds)
    
    Returns:
        CompletedProcess from subprocess.run
        
    Note:
        Uses --noblock_for_lock to prevent deadlocks when release_helper
        is invoked from within a Bazel build (e.g., via `bazel run`).
        This prevents waiting indefinitely for Bazel server locks.
    """
    workspace_root = find_workspace_root()
    
    # Add --noblock_for_lock before the command to prevent lock waiting
    # This is critical when release_helper is invoked from Bazel itself
    cmd = ["bazel", "--noblock_for_lock"] + args
    
    # Use provided environment or current environment
    run_env = env if env is not None else os.environ.copy()
    
    # Default timeout of 10 minutes for long image builds
    if timeout is None:
        timeout = 600
    
    try:
        return subprocess.run(
            cmd,
            capture_output=capture_output,
            text=True,
            check=True,
            cwd=workspace_root,
            env=run_env,
            timeout=timeout
        )
    except subprocess.TimeoutExpired as e:
        print(f"Bazel command timed out after {timeout}s: {' '.join(cmd)}", file=sys.stderr)
        print(f"Working directory: {workspace_root}", file=sys.stderr)
        raise
    except subprocess.CalledProcessError as e:
        print(f"Bazel command failed: {' '.join(cmd)}", file=sys.stderr)
        print(f"Working directory: {workspace_root}", file=sys.stderr)
        if e.stderr:
            print(f"stderr: {e.stderr}", file=sys.stderr)
        if e.stdout:
            print(f"stdout: {e.stdout}", file=sys.stderr)
        raise