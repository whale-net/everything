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


def run_bazel(args: list[str], capture_output: bool = True, env: dict = None) -> subprocess.CompletedProcess:
    """Run a bazel command with consistent configuration.

    Respects BAZEL_CONFIG env var to inject --config flags into build/run/test commands.
    This ensures inner Bazel calls from the release helper use the same config as the
    outer CI invocation (e.g., --config=ci-images for remote cache and download settings).
    """
    workspace_root = find_workspace_root()

    # Explicitly point Bazel to the workspace .bazelrc to ensure config is found
    # This is needed when the release helper is run as a standalone binary
    bazelrc_path = workspace_root / ".bazelrc"
    startup_opts = []
    if bazelrc_path.exists():
        startup_opts = [f"--bazelrc={bazelrc_path}"]

    # Inject --config flag for build/run/test commands if BAZEL_CONFIG is set
    bazel_config = os.environ.get("BAZEL_CONFIG", "")
    if bazel_config and len(args) > 0 and args[0] in ("build", "run", "test", "query"):
        config_flags = [f"--config={c.strip()}" for c in bazel_config.split(",") if c.strip()]
        cmd = ["bazel"] + startup_opts + [args[0]] + config_flags + args[1:]
    else:
        cmd = ["bazel"] + startup_opts + args
    
    # Use provided environment or current environment
    run_env = env if env is not None else os.environ.copy()
    
    try:
        return subprocess.run(
            cmd,
            capture_output=capture_output,
            text=True,
            check=True,
            cwd=workspace_root,
            env=run_env
        )
    except subprocess.CalledProcessError as e:
        print(f"Bazel command failed: {' '.join(cmd)}", file=sys.stderr)
        print(f"Working directory: {workspace_root}", file=sys.stderr)
        if e.stderr:
            print(f"stderr: {e.stderr}", file=sys.stderr)
        if e.stdout:
            print(f"stdout: {e.stdout}", file=sys.stderr)
        raise