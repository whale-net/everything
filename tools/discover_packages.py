#!/usr/bin/env python3
"""Discover pip packages from py_binary runfiles for per-package OCI layers.

This script analyzes a py_binary's runfiles structure and extracts information
about pip packages to enable efficient per-package layering in OCI images.
"""

import json
import sys
from pathlib import Path
from typing import Dict, List, Set


def discover_pip_packages(runfiles_dir: Path) -> Dict[str, List[str]]:
    """Discover pip packages and their files from runfiles directory.
    
    Args:
        runfiles_dir: Path to the .runfiles directory
        
    Returns:
        Dict mapping package name to list of relative file paths
    """
    packages = {}
    
    # Pip packages are in: rules_pycross++lock_repos+pypi/_lock/
    pip_dir = runfiles_dir / "rules_pycross++lock_repos+pypi" / "_lock"
    
    if not pip_dir.exists():
        return packages
    
    # Each subdirectory is a package (e.g., "fastapi@0.118.0")
    for package_dir in pip_dir.iterdir():
        if not package_dir.is_dir():
            continue
            
        package_name = package_dir.name
        package_files = []
        
        # Collect all files in this package (follow symlinks)
        for file_path in package_dir.rglob("*"):
            if file_path.is_file():
                # Get relative path from runfiles root
                rel_path = file_path.relative_to(runfiles_dir)
                package_files.append(str(rel_path))
        
        if package_files:
            packages[package_name] = sorted(package_files)
    
    return packages


def discover_interpreter_files(runfiles_dir: Path) -> List[str]:
    """Discover Python interpreter files from hermetic toolchain.
    
    Args:
        runfiles_dir: Path to the .runfiles directory
        
    Returns:
        List of relative file paths for the Python interpreter
    """
    interpreter_files = []
    
    # Hermetic Python is in: rules_python++python+python_3_11_*
    for python_dir in runfiles_dir.glob("rules_python++python+python_3_11_*"):
        if not python_dir.is_dir():
            continue
            
        for file_path in python_dir.rglob("*"):
            if file_path.is_file():
                rel_path = file_path.relative_to(runfiles_dir)
                interpreter_files.append(str(rel_path))
    
    return sorted(interpreter_files)


def discover_app_files(runfiles_dir: Path, app_path: str) -> List[str]:
    """Discover application files (not from pip or interpreter).
    
    Args:
        runfiles_dir: Path to the .runfiles directory
        app_path: Path pattern for app files (e.g., "_main/demo/hello_fastapi")
        
    Returns:
        List of relative file paths for application code
    """
    app_files = []
    
    app_dir = runfiles_dir / "_main" / app_path
    if not app_dir.exists():
        return app_files
    
    for file_path in app_dir.rglob("*"):
        if file_path.is_file():
            rel_path = file_path.relative_to(runfiles_dir)
            app_files.append(str(rel_path))
    
    return sorted(app_files)


def main():
    """Main entry point for package discovery."""
    if len(sys.argv) != 2:
        print("Usage: discover_packages.py <runfiles_dir>")
        sys.exit(1)
    
    runfiles_dir = Path(sys.argv[1])
    
    if not runfiles_dir.exists():
        print(f"Error: Runfiles directory not found: {runfiles_dir}")
        sys.exit(1)
    
    # Discover all package categories
    result = {
        "interpreter": discover_interpreter_files(runfiles_dir),
        "pip_packages": discover_pip_packages(runfiles_dir),
        # App files would need the app path - for now just structure
    }
    
    # Output as JSON for Bazel to consume
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
