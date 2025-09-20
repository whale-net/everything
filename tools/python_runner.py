#!/usr/bin/env python3
"""
Python wrapper that properly configures runfiles for containerized Python apps.
This handles the complexity of Bazel runfiles in containers automatically and provides
cross-platform compatibility for compiled extensions.
"""
import os
import sys

def setup_python_path():
    """Automatically discover and add all pip dependency site-packages to PYTHONPATH."""
    app_dir = "/app"
    
    # Base paths
    python_paths = [app_dir, f"{app_dir}/libs"]
    
    # Find runfiles directory by looking for .runfiles directories
    runfiles_dir = None
    for item in os.listdir(app_dir):
        if item.endswith(".runfiles"):
            runfiles_dir = os.path.join(app_dir, item)
            break
    
    if runfiles_dir and os.path.exists(runfiles_dir):
        # Add all pip dependency directories to Python path
        runfiles_contents = os.listdir(runfiles_dir)
        
        for item in runfiles_contents:
            if item.startswith("rules_python++pip+"):
                dep_path = os.path.join(runfiles_dir, item, "site-packages")
                if os.path.exists(dep_path):
                    python_paths.append(dep_path)
    
    # Set PYTHONPATH
    new_path = ":".join(python_paths)
    os.environ["PYTHONPATH"] = new_path
    
    # Set runfiles environment
    if runfiles_dir:
        os.environ["RUNFILES_DIR"] = runfiles_dir

if __name__ == "__main__":
    setup_python_path()
    
    # Import and run the main module
    main_script = sys.argv[1] if len(sys.argv) > 1 else "/app/main.py"
    
    # Change to app directory
    os.chdir("/app")
    
    # Execute the main script
    with open(main_script, 'r') as f:
        exec(f.read())