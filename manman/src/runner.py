"""Test runner for manman core tests."""
import pytest
import sys

if __name__ == "__main__":
    # Run pytest with the current directory and exit with the same code
    sys.exit(pytest.main([__file__.replace("/runner.py", "")]))