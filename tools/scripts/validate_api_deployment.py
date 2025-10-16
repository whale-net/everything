#!/usr/bin/env python3
"""
Validation script for api_deployment module.

This script verifies that the api_deployment module is properly installed
and can be used to configure API deployments.
"""

import sys
import os
from pathlib import Path

# Add the repository root to Python path
# This script is in tools/scripts/, so go up two levels
repo_root = Path(__file__).parent.parent.parent.resolve()
sys.path.insert(0, str(repo_root))

def test_config_module():
    """Test the config module can be imported and used."""
    print("Testing config module...")
    from libs.python.api_deployment.config import get_default_gunicorn_config
    
    # Test default configuration
    config = get_default_gunicorn_config()
    assert "bind" in config
    assert "workers" in config
    assert "worker_class" in config
    assert config["worker_class"] == "uvicorn.workers.UvicornWorker"
    print("✓ Config module works correctly")
    
    # Test custom configuration
    config = get_default_gunicorn_config(
        app_name="test-api",
        port=8080,
        workers=4,
    )
    assert config["bind"] == "0.0.0.0:8080"
    assert config["workers"] == 4
    print("✓ Custom configuration works correctly")

def test_cli_module():
    """Test the CLI module can be imported and used."""
    print("\nTesting CLI module...")
    from libs.python.api_deployment.cli import create_deployment_cli
    
    # Test parser creation
    parser = create_deployment_cli("main:app", "test-api")
    
    # Test default parsing
    args = parser.parse_args([])
    assert args.host == "0.0.0.0"
    assert args.port == 8000
    assert args.production is False
    print("✓ CLI module works correctly")
    
    # Test with arguments
    args = parser.parse_args([
        "--host", "127.0.0.1",
        "--port", "9000",
        "--production",
        "--workers", "8",
    ])
    assert args.host == "127.0.0.1"
    assert args.port == 9000
    assert args.production is True
    assert args.workers == 8
    print("✓ CLI argument parsing works correctly")

def test_main_module():
    """Test the main module can be imported."""
    print("\nTesting main module...")
    from libs.python.api_deployment import (
        get_default_gunicorn_config,
        run_with_gunicorn,
        create_deployment_cli,
    )
    
    # Verify all functions are available
    assert callable(get_default_gunicorn_config)
    assert callable(run_with_gunicorn)
    assert callable(create_deployment_cli)
    print("✓ Main module exports all functions correctly")

def main():
    """Run all validation tests."""
    print("=" * 60)
    print("API Deployment Module Validation")
    print("=" * 60)
    
    try:
        test_config_module()
        test_cli_module()
        test_main_module()
        
        print("\n" + "=" * 60)
        print("✅ All validation tests passed!")
        print("=" * 60)
        print("\nThe api_deployment module is ready to use.")
        print("\nQuick start:")
        print("  from libs.python.api_deployment.cli import run_from_cli")
        print("  run_from_cli('main:app', app_name='my-api')")
        return 0
        
    except Exception as e:
        print("\n" + "=" * 60)
        print(f"❌ Validation failed: {e}")
        print("=" * 60)
        import traceback
        traceback.print_exc()
        return 1

if __name__ == "__main__":
    sys.exit(main())
