#!/usr/bin/env python3
"""
Test helper script for the Everything monorepo.
Provides utilities for smart test discovery and execution.
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path
from typing import Dict, List, Tuple


def find_workspace_root() -> Path:
    """Find the workspace root directory."""
    if "BUILD_WORKSPACE_DIRECTORY" in os.environ:
        return Path(os.environ["BUILD_WORKSPACE_DIRECTORY"])
    
    current = Path.cwd()
    for path in [current] + list(current.parents):
        if (path / "WORKSPACE").exists() or (path / "MODULE.bazel").exists():
            return path
    
    return current


def run_bazel(args: List[str], capture_output: bool = True, check: bool = True) -> subprocess.CompletedProcess:
    """Run a bazel command with consistent configuration."""
    workspace_root = find_workspace_root()
    cmd = ["bazel"] + args
    try:
        return subprocess.run(
            cmd,
            capture_output=capture_output,
            text=True,
            check=check,
            cwd=workspace_root
        )
    except subprocess.CalledProcessError as e:
        print(f"Bazel command failed: {' '.join(cmd)}")
        if e.stderr:
            print(f"stderr: {e.stderr}")
        raise


def discover_test_targets() -> Dict[str, List[str]]:
    """Discover all test targets in the workspace."""
    result = run_bazel(["query", "kind('.*_test', //...)", "--output=label"])
    all_test_targets = [line.strip() for line in result.stdout.split('\n') if line.strip()]
    
    return {
        "unit_tests": [t for t in all_test_targets if "integration" not in t.lower()],
        "integration_tests": [t for t in all_test_targets if "integration" in t.lower()],
        "all_tests": all_test_targets
    }


def discover_build_targets() -> List[str]:
    """Discover all buildable targets in the workspace."""
    result = run_bazel(["query", "kind('.*_(binary|library)', //...)", "--output=label"])
    return [line.strip() for line in result.stdout.split('\n') if line.strip()]


def check_targets_exist(patterns: List[str]) -> Tuple[List[str], List[str]]:
    """Check which target patterns have actual targets."""
    valid_patterns = []
    empty_patterns = []
    
    for pattern in patterns:
        try:
            result = run_bazel(["query", pattern, "--output=label"], check=False)
            if result.returncode == 0 and result.stdout.strip():
                valid_patterns.append(pattern)
            else:
                empty_patterns.append(pattern)
        except Exception:
            empty_patterns.append(pattern)
    
    return valid_patterns, empty_patterns


def run_tests_for_patterns(patterns: List[str], config: str = "ci") -> bool:
    """Run tests for the given patterns."""
    # Check which patterns actually have targets
    valid_patterns, empty_patterns = check_targets_exist(patterns)
    
    if empty_patterns:
        print(f"⚠️  Skipping empty patterns: {', '.join(empty_patterns)}")
    
    if not valid_patterns:
        print("⚠️  No valid test patterns found")
        return True
    
    # Query for actual test targets in valid patterns
    test_query_patterns = [f"kind('.*_test', {pattern})" for pattern in valid_patterns]
    query = " union ".join(test_query_patterns)
    
    try:
        result = run_bazel(["query", f"({query})", "--output=label"])
        test_targets = [line.strip() for line in result.stdout.split('\n') if line.strip()]
        
        if not test_targets:
            print("⚠️  No test targets found")
            return True
        
        print(f"📝 Found {len(test_targets)} test targets")
        
        # Run the tests
        args = ["test", f"--config={config}"] + test_targets
        run_bazel(args, capture_output=False)
        print("✅ Tests passed")
        return True
        
    except subprocess.CalledProcessError:
        print("❌ Tests failed")
        return False


def main():
    parser = argparse.ArgumentParser(description="Test helper for Everything monorepo")
    subparsers = parser.add_subparsers(dest="command", help="Available commands")
    
    # Discover command
    discover_parser = subparsers.add_parser("discover", help="Discover available tests")
    discover_parser.add_argument("--format", choices=["json", "text"], default="text")
    
    # Smart tests command
    smart_parser = subparsers.add_parser("smart", help="Run smart test discovery")
    smart_parser.add_argument("--patterns", help="Comma-separated list of target patterns")
    smart_parser.add_argument("--config", default="ci", help="Bazel config to use")
    
    # Unit tests command
    unit_parser = subparsers.add_parser("unit", help="Run unit tests")
    unit_parser.add_argument("--config", default="ci", help="Bazel config to use")
    
    # All tests command
    all_parser = subparsers.add_parser("all", help="Run all tests")
    all_parser.add_argument("--config", default="ci", help="Bazel config to use")
    
    args = parser.parse_args()
    
    if not args.command:
        parser.print_help()
        return
    
    try:
        if args.command == "discover":
            test_targets = discover_test_targets()
            build_targets = discover_build_targets()
            
            if args.format == "json":
                result = {
                    "test_targets": test_targets,
                    "build_targets": build_targets
                }
                print(json.dumps(result, indent=2))
            else:
                print("🔍 Test Discovery Results")
                print("=" * 50)
                print(f"Unit tests: {len(test_targets.get('unit_tests', []))}")
                print(f"Integration tests: {len(test_targets.get('integration_tests', []))}")
                print(f"Total tests: {len(test_targets.get('all_tests', []))}")
                print(f"Build targets: {len(build_targets)}")
        
        elif args.command == "smart":
            patterns = ["//..."]
            if args.patterns:
                patterns = [p.strip() for p in args.patterns.split(',')]
            
            print(f"🤖 Running smart tests for patterns: {', '.join(patterns)}")
            success = run_tests_for_patterns(patterns, args.config)
            sys.exit(0 if success else 1)
        
        elif args.command == "unit":
            print("🧪 Running unit tests...")
            test_targets = discover_test_targets()
            unit_tests = test_targets.get("unit_tests", [])
            
            if not unit_tests:
                print("⚠️  No unit tests found")
                sys.exit(0)
            
            try:
                args_list = ["test", f"--config={args.config}"] + unit_tests
                run_bazel(args_list, capture_output=False)
                print("✅ Unit tests passed")
            except subprocess.CalledProcessError:
                print("❌ Unit tests failed")
                sys.exit(1)
        
        elif args.command == "all":
            print("🚀 Running all tests...")
            success = run_tests_for_patterns(["//..."], args.config)
            sys.exit(0 if success else 1)
    
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
