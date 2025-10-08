#!/usr/bin/env python3
"""
Tool to install a Python virtual environment for local development.

This tool creates a virtual environment and installs dependencies from pyproject.toml.
It's useful for IDE support, debugging, and running Python tools outside of Bazel.

Usage:
    python3 tools/install_venv.py [--venv-dir VENV_DIR]
    
    Or via Bazel:
    bazel run //tools:install_venv
    bazel run //tools:install_venv -- --venv-dir ./my_venv
"""

import argparse
import os
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


def install_venv(venv_dir: Path, workspace_root: Path):
    """Create and install Python virtual environment."""
    
    print(f"Creating Python virtual environment in: {venv_dir}")
    
    # Create the venv
    try:
        subprocess.run(
            [sys.executable, "-m", "venv", str(venv_dir)],
            check=True,
            cwd=workspace_root,
        )
        print(f"✓ Virtual environment created at {venv_dir}")
    except subprocess.CalledProcessError as e:
        print(f"✗ Failed to create virtual environment: {e}", file=sys.stderr)
        return False
    
    # Get the path to pip in the venv
    if sys.platform == "win32":
        pip_path = venv_dir / "Scripts" / "pip"
        python_path = venv_dir / "Scripts" / "python"
    else:
        pip_path = venv_dir / "bin" / "pip"
        python_path = venv_dir / "bin" / "python"
    
    # Upgrade pip
    print("Upgrading pip...")
    try:
        subprocess.run(
            [str(python_path), "-m", "pip", "install", "--upgrade", "pip"],
            check=True,
            cwd=workspace_root,
            timeout=60,  # 60 second timeout
        )
        print("✓ pip upgraded")
    except subprocess.TimeoutExpired:
        print("⚠ pip upgrade timed out, continuing with existing pip version")
    except subprocess.CalledProcessError as e:
        print(f"⚠ Failed to upgrade pip: {e}, continuing with existing pip version")
    
    # Install dependencies from pyproject.toml
    pyproject_path = workspace_root / "pyproject.toml"
    if pyproject_path.exists():
        print(f"Installing dependencies from {pyproject_path}...")
        
        # Read dependencies from pyproject.toml
        try:
            import tomllib
        except ImportError:
            # Python < 3.11
            try:
                import tomli as tomllib
            except ImportError:
                print("Warning: tomli/tomllib not available, attempting to install it first...")
                subprocess.run(
                    [str(pip_path), "install", "tomli"],
                    check=True,
                    cwd=workspace_root,
                )
                import tomli as tomllib
        
        with open(pyproject_path, "rb") as f:
            pyproject = tomllib.load(f)
        
        dependencies = pyproject.get("project", {}).get("dependencies", [])
        
        if dependencies:
            print(f"Found {len(dependencies)} dependencies to install...")
            try:
                subprocess.run(
                    [str(pip_path), "install"] + dependencies,
                    check=True,
                    cwd=workspace_root,
                    timeout=300,  # 5 minute timeout
                )
                print("✓ Dependencies installed")
            except subprocess.TimeoutExpired:
                print("⚠ Dependency installation timed out", file=sys.stderr)
                print("  You may need to install dependencies manually or retry with better network connection")
                return False
            except subprocess.CalledProcessError as e:
                print(f"✗ Failed to install dependencies: {e}", file=sys.stderr)
                return False
        else:
            print("No dependencies found in pyproject.toml")
    else:
        print(f"Warning: pyproject.toml not found at {pyproject_path}", file=sys.stderr)
    
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
        import shutil
        shutil.rmtree(venv_dir)
    
    # Install venv
    success = install_venv(venv_dir, workspace_root)
    
    return 0 if success else 1


if __name__ == "__main__":
    sys.exit(main())
