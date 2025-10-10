#!/usr/bin/env python3
"""
Generate Python clients for ManMan APIs.

This script generates OpenAPI specifications from FastAPI applications
and then generates Python client libraries using openapi-generator-cli.

Usage:
    python tools/generate_clients.py --strategy=duplicate
    python tools/generate_clients.py --strategy=shared
    
Strategies:
    - duplicate: Generate clients with duplicated models (fast, works immediately)
    - shared: Generate clients that import from shared manman.src.models (requires setup)
"""
import argparse
import inspect
import json
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Literal

# Add project root to path so we can import manman modules
PROJECT_ROOT = Path(__file__).parent.parent
sys.path.insert(0, str(PROJECT_ROOT))

from manman.src.config import ManManConfig
from libs.python.openapi_gen.openapi_gen import generate_openapi_spec


Strategy = Literal["duplicate", "shared"]


def check_openapi_generator_installed() -> bool:
    """Check if openapi-generator-cli is available."""
    try:
        result = subprocess.run(
            ["openapi-generator-cli", "version"],
            capture_output=True,
            text=True,
        )
        return result.returncode == 0
    except FileNotFoundError:
        return False


def generate_openapi_spec_file(api_name: str) -> Path:
    """
    Generate OpenAPI spec for a ManMan API.
    
    Uses the openapi_gen library to generate specs.
    """
    print(f"📝 Generating OpenAPI spec for {api_name}...")
    
    # Validate API name
    try:
        ManManConfig.validate_api_name(api_name)
    except ValueError as e:
        print(f"❌ Invalid API name: {e}")
        sys.exit(1)
    
    # Build FastAPI app based on API
    if api_name == ManManConfig.EXPERIENCE_API:
        from manman.src.host.api.experience import create_app
        fastapi_app = create_app()
    elif api_name == ManManConfig.STATUS_API:
        from manman.src.host.api.status import create_app
        fastapi_app = create_app()
    elif api_name == ManManConfig.WORKER_DAL_API:
        from manman.src.host.api.worker_dal import create_app
        fastapi_app = create_app()
    else:
        print(f"❌ Unknown API name: {api_name}")
        sys.exit(1)
    
    # Generate spec using the library function
    output_dir = PROJECT_ROOT / "openapi-specs"
    spec_path = generate_openapi_spec(fastapi_app, api_name, output_dir)
    
    print(f"✅ Spec generated: {spec_path}")
    return spec_path


def discover_shared_models() -> dict[str, str]:
    """
    Automatically discover all Pydantic/SQLModel models in manman.src.models.
    
    Returns:
        Dictionary mapping model name to full import path.
        Example: {"Worker": "manman.src.models.Worker"}
    """
    from manman.src import models as manman_models
    
    model_mappings = {}
    
    for name, obj in inspect.getmembers(manman_models):
        # Check if it's a class and has Pydantic's model_validate method
        if (
            inspect.isclass(obj)
            and hasattr(obj, "model_validate")
            and obj.__module__ == "manman.src.models"
        ):
            model_mappings[name] = f"manman.src.models.{name}"
            print(f"   Found model: {name}")
    
    return model_mappings


def generate_config_duplicate_strategy(api_name: str) -> Path | None:
    """
    Generate config for duplicate strategy (no config needed, use defaults).
    
    Returns None since we don't need a config file for this strategy.
    """
    return None


def generate_config_shared_strategy(
    api_name: str, shared_models: dict[str, str]
) -> Path:
    """
    Generate OpenAPI generator config with import mappings for shared models.
    
    This tells the generator:
    - Don't generate these model classes
    - Import them from manman.src.models instead
    """
    package_name = f"manman_{api_name.replace('-', '_')}_client"
    
    config = {
        "packageName": package_name,
        "projectName": f"manman-{api_name}-client",
        "packageVersion": "0.1.0",
        "library": "urllib3",
        # Tell generator to import these models instead of generating them
        "importMappings": shared_models,
        # Map OpenAPI type names to Python type names
        "typeMappings": {name: name for name in shared_models.keys()},
    }
    
    config_path = PROJECT_ROOT / "tmp" / f"openapi-config-{api_name}.json"
    config_path.parent.mkdir(exist_ok=True)
    
    with open(config_path, "w") as f:
        json.dump(config, f, indent=2)
    
    print(f"✅ Config generated: {config_path}")
    return config_path


def copy_models_to_client(client_dir: Path):
    """
    Copy shared models source into client for self-containment.
    
    This allows the client to work outside the monorepo by including
    the model source code in the distributed package.
    """
    print(f"📦 Copying shared models into client...")
    
    # Source files to copy
    src_files = [
        PROJECT_ROOT / "manman" / "src" / "models.py",
        PROJECT_ROOT / "manman" / "src" / "constants.py",  # Models depend on this
    ]
    
    # Destination: manman/src/ inside the client package
    dest_dir = client_dir / "manman" / "src"
    dest_dir.mkdir(parents=True, exist_ok=True)
    
    # Create __init__.py files for proper package structure
    (dest_dir.parent / "__init__.py").touch()
    (dest_dir / "__init__.py").touch()
    
    # Copy files
    for src_file in src_files:
        if src_file.exists():
            shutil.copy(src_file, dest_dir / src_file.name)
            print(f"   Copied: {src_file.name}")
    
    # Also need SQLModel and dependencies in the package's setup.py
    # The generator should handle this, but we'll document it
    print(f"✅ Models copied to {dest_dir}")


def generate_client(
    api_name: str, spec_path: Path, strategy: Strategy
) -> Path:
    """
    Generate Python client from OpenAPI spec using openapi-generator-cli.
    
    Args:
        api_name: Name of the API (e.g., "experience-api")
        spec_path: Path to the OpenAPI spec JSON file
        strategy: "duplicate" or "shared" models strategy
    
    Returns:
        Path to generated client directory
    """
    output_dir = PROJECT_ROOT / "clients" / f"{api_name}-client"
    package_name = f"manman_{api_name.replace('-', '_')}_client"
    
    print(f"🔧 Generating client for {api_name} (strategy: {strategy})...")
    
    # Prepare base command
    cmd = [
        "openapi-generator-cli",
        "generate",
        "-i",
        str(spec_path),
        "-g",
        "python",
        "-o",
        str(output_dir),
        "--package-name",
        package_name,
        "--additional-properties",
        f"projectName=manman-{api_name}-client,packageVersion=0.1.0",
    ]
    
    # Add config file if using shared strategy
    if strategy == "shared":
        print("🔍 Discovering shared models...")
        shared_models = discover_shared_models()
        print(f"   Found {len(shared_models)} shared models")
        
        config_path = generate_config_shared_strategy(api_name, shared_models)
        cmd.extend(["-c", str(config_path)])
    
    # Run the generator
    print(f"   Running: {' '.join(cmd)}")
    result = subprocess.run(cmd, capture_output=True, text=True)
    
    if result.returncode != 0:
        print(f"❌ Failed to generate client: {result.stderr}")
        sys.exit(1)
    
    # For shared strategy, copy models into client for self-containment
    if strategy == "shared":
        copy_models_to_client(output_dir)
    
    print(f"✅ Client generated: {output_dir}")
    return output_dir


def build_client_wheel(client_dir: Path):
    """
    Build a distributable wheel for the client.
    
    Requires the 'build' package: pip install build
    """
    print(f"🔨 Building wheel for {client_dir.name}...")
    
    result = subprocess.run(
        [sys.executable, "-m", "build"],
        cwd=client_dir,
        capture_output=True,
        text=True,
    )
    
    if result.returncode != 0:
        print(f"⚠️  Failed to build wheel: {result.stderr}")
        print("   (You may need to install 'build': pip install build)")
        return
    
    # Find the generated wheel
    dist_dir = client_dir / "dist"
    wheels = list(dist_dir.glob("*.whl"))
    if wheels:
        print(f"✅ Wheel built: {wheels[0]}")
    else:
        print(f"⚠️  No wheel found in {dist_dir}")


def main():
    parser = argparse.ArgumentParser(
        description="Generate Python clients for ManMan APIs"
    )
    parser.add_argument(
        "--strategy",
        choices=["duplicate", "shared"],
        default="duplicate",
        help="Generation strategy: 'duplicate' (default) or 'shared' models",
    )
    parser.add_argument(
        "--api",
        choices=list(ManManConfig.KNOWN_API_NAMES) + ["all"],
        default="all",
        help="Which API to generate (default: all)",
    )
    parser.add_argument(
        "--build-wheel",
        action="store_true",
        help="Build distributable wheel after generation",
    )
    
    args = parser.parse_args()
    
    print("=" * 70)
    print("ManMan API Client Generator")
    print("=" * 70)
    print(f"Strategy: {args.strategy}")
    print(f"API: {args.api}")
    print()
    
    # Check prerequisites
    if not check_openapi_generator_installed():
        print("❌ openapi-generator-cli not found!")
        print("   Install via npm: npm install @openapitools/openapi-generator-cli -g")
        print("   Or use Docker: see README for instructions")
        sys.exit(1)
    
    # Determine which APIs to generate
    if args.api == "all":
        apis = list(ManManConfig.KNOWN_API_NAMES)
    else:
        apis = [args.api]
    
    # Generate clients for each API
    for api_name in apis:
        print()
        print(f"{'=' * 70}")
        print(f"Processing: {api_name}")
        print(f"{'=' * 70}")
        
        # Step 1: Generate OpenAPI spec
        spec_path = generate_openapi_spec_file(api_name)
        
        # Step 2: Generate client
        client_dir = generate_client(api_name, spec_path, args.strategy)
        
        # Step 3: Build wheel (optional)
        if args.build_wheel:
            build_client_wheel(client_dir)
        
        print()
    
    print("=" * 70)
    print("✅ All clients generated successfully!")
    print("=" * 70)
    print()
    print("Next steps:")
    print(f"  1. Review generated clients in: {PROJECT_ROOT / 'clients'}")
    if not args.build_wheel:
        print("  2. Build wheels: python -m build (in each client directory)")
    print("  3. Install and test: pip install clients/*/dist/*.whl")
    print()


if __name__ == "__main__":
    main()
