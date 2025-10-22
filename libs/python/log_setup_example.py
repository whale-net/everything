"""Example demonstrating unified logging usage."""

import logging
import os

# Set environment variable (normally set by deployment)
os.environ["APP_ENV"] = "dev"

# Import the unified logging
from libs.python.log_setup import setup_logging

# Setup logging with app metadata
# These would typically come from environment variables or config
setup_logging(
    level=logging.INFO,
    app_name="example-api",
    app_type="external-api",
    domain="demo",
    enable_otel=False,  # Set to True in production with OTEL endpoint configured
    enable_console=True,
)

# Get a logger
logger = logging.getLogger(__name__)

# Log some messages
logger.info("Application started successfully")
logger.debug("This debug message won't show with INFO level")
logger.warning("This is a warning")
logger.error("This is an error")

# Child logger inherits configuration
child_logger = logging.getLogger("demo.api.handlers")
child_logger.info("Processing request in handler")

print("\n--- Log format includes: [domain/app-name/app-type/env] ---")
