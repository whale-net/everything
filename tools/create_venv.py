#!/usr/bin/env python3
"""
Tool to create a Python virtual environment using requirements.lock.txt.

This tool creates a Python virtual environment for local development that includes
all packages specified in requirements.lock.txt. The venv is intended for local
development use and provides a superset of all project dependencies.
"""

import argparse
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


def create_venv(venv_path: Path, requirements_file: Path, python_executable: str = "python3") -> None:
    """Create a virtual environment and install requirements."""
    print(f"Creating virtual environment at: {venv_path}")
    
    # Create the virtual environment
    subprocess.run([
        python_executable, "-m", "venv", 
        str(venv_path),
        "--clear"  # Clear existing venv if it exists
    ], check=True)
    
    # Determine pip executable path
    if os.name == 'nt':  # Windows
        pip_executable = venv_path / "Scripts" / "pip"
        python_venv_executable = venv_path / "Scripts" / "python"
    else:  # Unix-like
        pip_executable = venv_path / "bin" / "pip"
        python_venv_executable = venv_path / "bin" / "python"
    
    print(f"Installing requirements from: {requirements_file}")
    
    # Upgrade pip first
    subprocess.run([
        str(python_venv_executable), "-m", "pip", "install", "--upgrade", "pip"
    ], check=True)
    
    # Install requirements
    subprocess.run([
        str(pip_executable), "install", "-r", str(requirements_file)
    ], check=True)
    
    print(f"\n✅ Virtual environment created successfully!")
    print(f"📁 Location: {venv_path}")
    print(f"\n🚀 To activate the environment:")
    if os.name == 'nt':  # Windows
        print(f"   {venv_path}\\Scripts\\activate")
    else:  # Unix-like
        print(f"   source {venv_path}/bin/activate")
    print(f"\n📦 Installed packages from: {requirements_file}")


def main():
    parser = argparse.ArgumentParser(
        description="Create a Python virtual environment using requirements.lock.txt",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Create venv in default location (.venv)
  bazel run //tools:create_venv

  # Create venv in custom location
  bazel run //tools:create_venv -- --venv-path ./my_venv

  # Use specific Python version
  bazel run //tools:create_venv -- --python python3.11
        """
    )
    
    parser.add_argument(
        "--venv-path",
        type=Path,
        default=Path(".venv"),
        help="Path where the virtual environment should be created (default: .venv)"
    )
    
    parser.add_argument(
        "--python",
        type=str,
        default="python3",
        help="Python executable to use (default: python3)"
    )
    
    parser.add_argument(
        "--requirements",
        type=Path,
        help="Path to requirements file (default: requirements.lock.txt in workspace root)"
    )
    
    args = parser.parse_args()
    
    # Find workspace root
    workspace_root = find_workspace_root()
    print(f"Workspace root: {workspace_root}")
    
    # Determine requirements file
    if args.requirements:
        requirements_file = args.requirements
        if not requirements_file.is_absolute():
            requirements_file = workspace_root / requirements_file
    else:
        requirements_file = workspace_root / "requirements.lock.txt"
    
    # Verify requirements file exists
    if not requirements_file.exists():
        print(f"❌ Error: Requirements file not found: {requirements_file}")
        sys.exit(1)
    
    # Determine venv path (relative to workspace root if not absolute)
    venv_path = args.venv_path
    if not venv_path.is_absolute():
        venv_path = workspace_root / venv_path
    
    try:
        create_venv(venv_path, requirements_file, args.python)
    except subprocess.CalledProcessError as e:
        print(f"❌ Error creating virtual environment: {e}")
        sys.exit(1)
    except Exception as e:
        print(f"❌ Unexpected error: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()