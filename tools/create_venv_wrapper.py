#!/usr/bin/env python3
"""
Python wrapper for create_venv.sh shell script.

This provides a reliable way to execute the shell script through Bazel
while maintaining the simplicity of the shell script approach.
"""

import os
import subprocess
import sys
from pathlib import Path


def main():
    """Execute the create_venv.sh shell script with the provided arguments."""
    # Find the shell script relative to this Python file
    script_dir = Path(__file__).parent
    shell_script = script_dir / "create_venv.sh"
    
    if not shell_script.exists():
        print(f"❌ Error: Shell script not found: {shell_script}")
        sys.exit(1)
    
    # Make sure the shell script is executable
    shell_script.chmod(0o755)
    
    # Execute the shell script with all the arguments passed to this Python script
    try:
        result = subprocess.run(
            [str(shell_script)] + sys.argv[1:],
            cwd=os.getcwd(),
            env=os.environ
        )
        sys.exit(result.returncode)
    except Exception as e:
        print(f"❌ Error executing shell script: {e}")
        sys.exit(1)


if __name__ == "__main__":
    main()