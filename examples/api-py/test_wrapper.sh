#!/bin/bash

# Set PYTHONPATH to include the virtual environment's site-packages
export PYTHONPATH=$PYTHONPATH:$(rlocation _main/.venv/lib/python3.11/site-packages)

# Execute pytest
# The pytest executable is usually in .venv/bin/pytest
# The test file is in examples/api-py/tests/test_api.py
# We need to make sure the python interpreter is used to run pytest

# Find the python executable in the virtual environment
PYTHON_BIN=$(rlocation _main/.venv/bin/python)

# Find the pytest executable in the virtual environment
PYTEST_BIN=$(rlocation _main/.venv/bin/pytest)

# Execute pytest with the correct python interpreter
exec "$PYTHON_BIN" "$PYTEST_BIN" "$(rlocation _main/examples/api-py/tests/test_api.py)"
