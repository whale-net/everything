#!/usr/bin/env python3
"""
Tool to install a Python virtual environment for local development.

This tool creates a virtual environment and installs dependencies using UV from uv.lock.
It's useful for IDE support, debugging, and running Python tools outside of Bazel.

Usage:
    python3 tools/install_venv.py [--venv-dir VENV_DIR]
    
    Or via Bazel:
    bazel run //tools:install_venv
    bazel run //tools:install_venv -- --venv-dir ./my_venv
"""

import argparse
import os
import shutil
import subprocess
import sys
from pathlib import Path


def find_workspace_root():
    """Find the workspace root by looking for pyproject.toml."""
    current = Path.cwd()
    while current != current.parent:
        if (current / "pyproject.toml").exists():
            return current
        current = current.parent
    raise RuntimeError("Could not find workspace root (no pyproject.toml found)")


def check_uv_available():
    """Check if UV is available in the system."""
    return shutil.which("uv") is not None


def install_uv():
    """Install UV if not available."""
    print("UV not found. Installing UV...")
    try:
        # Install UV using the official installation script
        subprocess.run(
            ["curl", "-LsSf", "https://astral.sh/uv/install.sh", "|", "sh"],
            shell=True,
            check=True,
        )
        print("✓ UV installed")
        return True
    except subprocess.CalledProcessError as e:
        print(f"⚠ Failed to install UV automatically: {e}")
        print("\nPlease install UV manually:")
        print("  curl -LsSf https://astral.sh/uv/install.sh | sh")
        print("  or visit: https://github.com/astral-sh/uv")
        return False


def install_venv(venv_dir: Path, workspace_root: Path):
    """Create and install Python virtual environment using UV."""
    
    # Check if UV is available
    if not check_uv_available():
        if not install_uv():
            return False
        # Check again after installation
        if not check_uv_available():
            print("✗ UV is still not available after installation", file=sys.stderr)
            return False
    
    print(f"Creating Python virtual environment in: {venv_dir}")
    
    # Check if uv.lock exists
    lock_file = workspace_root / "uv.lock"
    if not lock_file.exists():
        print(f"✗ uv.lock not found at {lock_file}", file=sys.stderr)
        print("Please run 'uv lock' to generate the lock file first.", file=sys.stderr)
        return False
    
    # Use UV to create venv and sync dependencies from lock file
    print(f"Using UV to sync dependencies from uv.lock...")
    try:
        # UV sync creates the venv and installs all dependencies from the lock file
        env = os.environ.copy()
        env["VIRTUAL_ENV"] = str(venv_dir)
        
        subprocess.run(
            ["uv", "sync", "--frozen"],
            check=True,
            cwd=workspace_root,
            timeout=300,  # 5 minute timeout
        )
        print(f"✓ Virtual environment created and dependencies installed at {venv_dir}")
    except subprocess.TimeoutExpired:
        print("⚠ UV sync timed out", file=sys.stderr)
        print("  You may need to retry with better network connection")
        return False
    except subprocess.CalledProcessError as e:
        print(f"✗ Failed to sync dependencies with UV: {e}", file=sys.stderr)
        print("\nTrying to create venv manually and install dependencies...")
        
        # Fallback: create venv manually and use uv pip sync
        try:
            subprocess.run(
                [sys.executable, "-m", "venv", str(venv_dir)],
                check=True,
                cwd=workspace_root,
            )
            print(f"✓ Virtual environment created at {venv_dir}")
            
            # Get the path to python in the venv
            if sys.platform == "win32":
                python_path = venv_dir / "Scripts" / "python"
            else:
                python_path = venv_dir / "bin" / "python"
            
            # Use uv pip to install from lock file
            subprocess.run(
                ["uv", "pip", "sync", "uv.lock", "--python", str(python_path)],
                check=True,
                cwd=workspace_root,
                timeout=300,
            )
            print("✓ Dependencies installed from uv.lock")
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as fallback_error:
            print(f"✗ Fallback installation also failed: {fallback_error}", file=sys.stderr)
            return False
    
    print("\n" + "=" * 60)
    print("Virtual environment setup complete!")
    print("=" * 60)
    print("\nTo activate the virtual environment:")
    if sys.platform == "win32":
        print(f"  {venv_dir}\\Scripts\\activate")
    else:
        print(f"  source {venv_dir}/bin/activate")
    print("\nTo deactivate:")
    print("  deactivate")
    print()
    
    return True


def main():
    parser = argparse.ArgumentParser(
        description="Install Python virtual environment for local development"
    )
    parser.add_argument(
        "--venv-dir",
        type=str,
        default=".venv",
        help="Directory for the virtual environment (default: .venv)",
    )
    
    args = parser.parse_args()
    
    # Find workspace root
    try:
        workspace_root = find_workspace_root()
        print(f"Workspace root: {workspace_root}")
    except RuntimeError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1
    
    # Create venv directory path
    venv_dir = workspace_root / args.venv_dir
    
    # Check if venv already exists
    if venv_dir.exists():
        response = input(f"\nVirtual environment already exists at {venv_dir}. Recreate? [y/N] ")
        if response.lower() != 'y':
            print("Aborted.")
            return 0
        print(f"Removing existing virtual environment at {venv_dir}...")
        shutil.rmtree(venv_dir)
    
    # Install venv
    success = install_venv(venv_dir, workspace_root)
    
    return 0 if success else 1


if __name__ == "__main__":
    sys.exit(main())
