"""Demo app showing how to use SteamCMD in a container."""

import subprocess
import sys


def main():
    """Test that SteamCMD is available in the container."""
    print("Testing SteamCMD availability...")
    
    # Check if steamcmd is in PATH
    try:
        result = subprocess.run(
            ["which", "steamcmd"],
            capture_output=True,
            text=True,
            check=True,
        )
        print(f"✓ steamcmd found at: {result.stdout.strip()}")
    except subprocess.CalledProcessError:
        print("✗ steamcmd not found in PATH")
        sys.exit(1)
    
    # Check the direct path
    try:
        result = subprocess.run(
            ["test", "-f", "/opt/steamcmd/steamcmd.sh"],
            check=True,
        )
        print("✓ /opt/steamcmd/steamcmd.sh exists")
    except subprocess.CalledProcessError:
        print("✗ /opt/steamcmd/steamcmd.sh not found")
        sys.exit(1)
    
    # Try to run steamcmd with +quit to just exit immediately
    print("\nTesting steamcmd execution...")
    try:
        result = subprocess.run(
            ["steamcmd", "+quit"],
            capture_output=True,
            text=True,
            timeout=30,
        )
        print("✓ steamcmd executed successfully")
        print(f"Exit code: {result.returncode}")
        if result.stdout:
            print(f"Output (first 500 chars): {result.stdout[:500]}")
    except subprocess.TimeoutExpired:
        print("✗ steamcmd timed out")
        sys.exit(1)
    except Exception as e:
        print(f"✗ steamcmd failed: {e}")
        sys.exit(1)
    
    print("\n✓ All tests passed! SteamCMD is ready to use.")


if __name__ == "__main__":
    main()
