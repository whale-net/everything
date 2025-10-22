"""
Example: Integrating Unified Logging with release_app Metadata

This example shows how an application can read its metadata from environment
variables (which would be auto-injected from release_app in the future) and
use it to configure logging.
"""

import logging
import os
import sys

# Simulate environment variables that would be set by Kubernetes/Helm
# In production, these come from release_app metadata
os.environ["APP_NAME"] = "demo-service"
os.environ["APP_TYPE"] = "external-api"
os.environ["APP_DOMAIN"] = "demo"
os.environ["APP_ENV"] = "dev"

from libs.python.log_setup import setup_logging


def get_app_metadata():
    """
    Read app metadata from environment variables.
    
    In the future, these could be automatically injected during container build
    from the release_app macro metadata.
    """
    return {
        "app_name": os.getenv("APP_NAME"),
        "app_type": os.getenv("APP_TYPE"),
        "domain": os.getenv("APP_DOMAIN"),
        "app_env": os.getenv("APP_ENV"),
    }


def main():
    """Main application entry point."""
    # Get metadata from environment
    metadata = get_app_metadata()
    
    print("=== Application Metadata ===")
    print(f"Domain: {metadata['domain']}")
    print(f"App Name: {metadata['app_name']}")
    print(f"App Type: {metadata['app_type']}")
    print(f"Environment: {metadata['app_env']}")
    print()
    
    # Configure logging with metadata
    setup_logging(
        level=logging.INFO,
        app_name=metadata["app_name"],
        app_type=metadata["app_type"],
        domain=metadata["domain"],
        app_env=metadata["app_env"],
        enable_otel=False,  # Set to True in production
        enable_console=True,
    )
    
    # Get logger
    logger = logging.getLogger(__name__)
    
    print("=== Log Output ===")
    logger.info("Application initialized with metadata")
    logger.info(f"Service: {metadata['domain']}-{metadata['app_name']}")
    logger.warning("This is a warning with full context")
    
    # Simulate some application work
    logger.info("Processing request...")
    
    # Child loggers inherit the configuration
    api_logger = logging.getLogger("api.handlers")
    api_logger.info("Handler processing request")
    
    db_logger = logging.getLogger("db.queries")
    db_logger.info("Executing database query")
    
    print()
    print("=== Benefits ===")
    print("1. All logs include full context: [domain/app/type/env]")
    print("2. Easy to filter logs by environment in production")
    print("3. OTEL collectors can aggregate by service.name = domain-app")
    print("4. Consistent logging across all monorepo applications")


if __name__ == "__main__":
    main()
