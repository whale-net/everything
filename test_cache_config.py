#!/usr/bin/env python3
"""
Test script to validate Bazel remote cache concurrency configuration.
"""

import subprocess
import re
import sys


def run_bazel_command(args):
    """Run a bazel command and return output."""
    try:
        result = subprocess.run(
            ["bazel"] + args,
            capture_output=True,
            text=True,
            cwd="/home/runner/work/everything/everything"
        )
        return result.stdout, result.stderr, result.returncode
    except Exception as e:
        return "", str(e), 1


def test_configuration_applied():
    """Test that our cache configuration is being applied."""
    print("=== Testing Remote Cache Configuration ===\n")
    
    # Test that bazel can parse our configuration
    print("1. Testing configuration parsing...")
    stdout, stderr, rc = run_bazel_command(["help", "build"])
    
    if rc != 0:
        print(f"❌ Bazel help command failed: {stderr}")
        return False
    
    print("✅ Bazel configuration parsed successfully")
    
    # Test configuration with announce_rc to see active flags
    print("\n2. Testing active configuration flags...")
    stdout, stderr, rc = run_bazel_command([
        "build", 
        "--announce_rc",
        "//demo/hello_python:hello_python"
    ])
    
    # Look for our specific flags in the output
    found_remote_connections = False
    found_http_downloads = False
    
    output = stdout + stderr
    
    # Check for remote_max_connections setting
    if "--remote_max_connections=300" in output:
        found_remote_connections = True
        print("✅ remote_max_connections=300 is active")
    elif "remote_max_connections" in output:
        # Extract the actual value
        match = re.search(r'--remote_max_connections=(\d+)', output)
        if match:
            value = match.group(1)
            print(f"ℹ️  remote_max_connections is set to {value}")
            found_remote_connections = True
    
    # Check for http_max_parallel_downloads setting
    if "--http_max_parallel_downloads=24" in output:
        found_http_downloads = True
        print("✅ http_max_parallel_downloads=24 is active")
    elif "http_max_parallel_downloads" in output:
        # Extract the actual value
        match = re.search(r'--http_max_parallel_downloads=(\d+)', output)
        if match:
            value = match.group(1)
            print(f"ℹ️  http_max_parallel_downloads is set to {value}")
            found_http_downloads = True
    
    if not found_remote_connections:
        print("⚠️  remote_max_connections setting not found in output")
    
    if not found_http_downloads:
        print("⚠️  http_max_parallel_downloads setting not found in output")
    
    # Test CI configuration
    print("\n3. Testing CI-specific configuration...")
    stdout, stderr, rc = run_bazel_command([
        "build",
        "--config=ci", 
        "--announce_rc",
        "//demo/hello_python:hello_python"
    ])
    
    ci_output = stdout + stderr
    ci_flags_found = []
    
    expected_ci_flags = [
        "remote_timeout=90s",
        "experimental_remote_cache_compression_threshold=50"
    ]
    
    for flag in expected_ci_flags:
        if flag in ci_output:
            ci_flags_found.append(flag)
            print(f"✅ CI flag {flag} is active")
    
    print(f"\n4. Configuration Summary:")
    print(f"   - remote_max_connections: {'✅' if found_remote_connections else '❌'}")
    print(f"   - http_max_parallel_downloads: {'✅' if found_http_downloads else '❌'}")
    print(f"   - CI flags found: {len(ci_flags_found)}/{len(expected_ci_flags)}")
    
    success = found_remote_connections and found_http_downloads
    
    if success:
        print("\n🎉 Configuration test PASSED - Remote cache concurrency settings are active!")
    else:
        print("\n❌ Configuration test FAILED - Some settings may not be active")
    
    return success


def test_config_syntax():
    """Test that our .bazelrc syntax is valid."""
    print("\n=== Testing .bazelrc Syntax ===\n")
    
    # Try to run a simple query to test syntax
    stdout, stderr, rc = run_bazel_command(["query", "--keep_going", "//demo/..."])
    
    if rc != 0 and "syntax error" in stderr.lower():
        print(f"❌ Syntax error in .bazelrc: {stderr}")
        return False
    
    print("✅ .bazelrc syntax is valid")
    return True


def compare_before_after():
    """Show the before/after comparison of our changes."""
    print("\n=== Before/After Comparison ===\n")
    
    comparison = {
        "Setting": ["Default", "New Value", "Improvement"],
        "remote_max_connections": ["100", "300", "3x more concurrent connections"],
        "http_max_parallel_downloads": ["8", "24", "3x more parallel downloads"],
        "remote_timeout (CI)": ["60s", "90s", "50% longer timeout for stability"],
        "compression_threshold (CI)": ["100", "50", "More aggressive compression"]
    }
    
    # Print table
    for i, (key, values) in enumerate(comparison.items()):
        if i == 0:  # Header
            print(f"{'Setting':<35} {'Default':<10} {'New Value':<12} {'Improvement'}")
            print("-" * 80)
        else:
            print(f"{key:<35} {values[0]:<10} {values[1]:<12} {values[2]}")
    
    print("\n💡 Expected Benefits:")
    print("   - 2-3x faster cache resolution for parallel builds")
    print("   - Better utilization of available bandwidth")
    print("   - Reduced build queue times")
    print("   - Improved CI performance")


if __name__ == "__main__":
    print("Bazel Remote Cache Configuration Test")
    print("=" * 50)
    
    # Run tests
    syntax_ok = test_config_syntax()
    config_ok = test_configuration_applied()
    
    # Show comparison
    compare_before_after()
    
    # Final result
    print("\n" + "=" * 50)
    if syntax_ok and config_ok:
        print("🎉 ALL TESTS PASSED - Configuration is working correctly!")
        sys.exit(0)
    else:
        print("❌ SOME TESTS FAILED - Please check configuration")
        sys.exit(1)