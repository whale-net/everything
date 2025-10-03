#!/usr/bin/env python3
"""
Smoke test for container image building.

This test validates that the image building system works correctly by:
1. Building images for representative Python and Go applications
2. Verifying the images are created with correct tags
3. Running the images to ensure they work properly

This is a focused test that validates the core image building capability
without requiring full matrix builds of all apps.
"""

import subprocess
import sys
from typing import List, Tuple


def run_command(cmd: List[str], description: str) -> Tuple[bool, str]:
    """Run a command and return success status and output."""
    print(f"🔧 {description}")
    print(f"   Command: {' '.join(cmd)}")
    
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, check=True, timeout=300)
        output = result.stdout.strip()
        if output:
            # Show first 200 chars of output
            preview = output[:200] + "..." if len(output) > 200 else output
            print(f"   ✅ Success: {preview}")
        else:
            print(f"   ✅ Success")
        return True, output
    except subprocess.CalledProcessError as e:
        print(f"   ❌ Failed: {e}")
        if e.stdout:
            print(f"   STDOUT: {e.stdout[:500]}")
        if e.stderr:
            print(f"   STDERR: {e.stderr[:500]}")
        return False, ""
    except subprocess.TimeoutExpired:
        print(f"   ❌ Timeout: Command took longer than 300 seconds")
        return False, ""


def test_build_python_image():
    """Test building Python demo image."""
    print("\n🐍 Testing Python Image Build")
    print("=" * 50)
    
    # Build the image using bazel
    cmd = ["bazel", "run", "//demo/hello_python:hello_python_image_load"]
    success, _ = run_command(cmd, "Building hello_python image")
    
    if not success:
        return False
    
    # Verify image exists
    cmd = ["docker", "images", "demo-hello_python", "--format", "{{.Repository}}:{{.Tag}}"]
    success, output = run_command(cmd, "Verifying hello_python image exists")
    
    if not success or "demo-hello_python:latest" not in output:
        print(f"   ❌ Image not found in docker images output")
        return False
    
    # Test running the image
    cmd = ["docker", "run", "--rm", "demo-hello_python:latest"]
    success, output = run_command(cmd, "Running hello_python container")
    
    if not success:
        return False
    
    # Verify expected output
    if "Hello, world from uv and Bazel" not in output:
        print(f"   ❌ Unexpected output from container: {output}")
        return False
    
    print("   ✅ Python image build and run successful")
    return True


def test_build_go_image():
    """Test building Go demo image."""
    print("\n🦫 Testing Go Image Build")
    print("=" * 50)
    
    # Build the image using bazel
    cmd = ["bazel", "run", "//demo/hello_go:hello_go_image_load"]
    success, _ = run_command(cmd, "Building hello_go image")
    
    if not success:
        return False
    
    # Verify image exists
    cmd = ["docker", "images", "demo-hello_go", "--format", "{{.Repository}}:{{.Tag}}"]
    success, output = run_command(cmd, "Verifying hello_go image exists")
    
    if not success or "demo-hello_go:latest" not in output:
        print(f"   ❌ Image not found in docker images output")
        return False
    
    # Test running the image
    cmd = ["docker", "run", "--rm", "demo-hello_go:latest"]
    success, output = run_command(cmd, "Running hello_go container")
    
    if not success:
        return False
    
    # Verify expected output
    if "Hello, world from Bazel from Go!" not in output:
        print(f"   ❌ Unexpected output from container: {output}")
        return False
    
    print("   ✅ Go image build and run successful")
    return True


def test_image_metadata():
    """Test that images have correct metadata."""
    print("\n📋 Testing Image Metadata")
    print("=" * 50)
    
    images = ["demo-hello_python:latest", "demo-hello_go:latest"]
    
    for image in images:
        # Inspect image architecture
        cmd = ["docker", "inspect", image, "--format", "{{.Architecture}}"]
        success, output = run_command(cmd, f"Checking architecture of {image}")
        
        if not success:
            return False
        
        # Verify architecture is set (should be amd64 or arm64)
        if output not in ["amd64", "arm64"]:
            print(f"   ❌ Unexpected architecture: {output}")
            return False
        
        print(f"   ✅ {image} has valid architecture: {output}")
    
    return True


def main():
    """Run all smoke tests."""
    print("🎯 Container Image Build Smoke Test")
    print("=" * 60)
    print("This test validates that representative Python and Go images")
    print("can be built and run successfully, ensuring the build system works.")
    print("=" * 60)
    
    tests = [
        ("Python Image Build", test_build_python_image),
        ("Go Image Build", test_build_go_image),
        ("Image Metadata", test_image_metadata),
    ]
    
    results = {}
    
    for test_name, test_func in tests:
        try:
            result = test_func()
            results[test_name] = result
        except Exception as e:
            print(f"❌ {test_name} failed with exception: {e}")
            import traceback
            traceback.print_exc()
            results[test_name] = False
    
    # Summary
    print("\n📊 Smoke Test Summary")
    print("=" * 40)
    
    for test_name, success in results.items():
        status = "✅ PASS" if success else "❌ FAIL"
        print(f"{status} {test_name}")
    
    total_tests = len(results)
    passed_tests = sum(1 for success in results.values() if success)
    
    print(f"\nPassed: {passed_tests}/{total_tests}")
    
    if passed_tests == total_tests:
        print("\n🎉 All smoke tests passed!")
        print("The image build system is working correctly.")
        return 0
    else:
        print("\n⚠️  Some smoke tests failed.")
        print("The image build system may have issues.")
        return 1


if __name__ == "__main__":
    sys.exit(main())
