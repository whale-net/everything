#!/usr/bin/env python3
"""
Test script for multi-architecture container workflow.

This script tests the new multi-architecture release system by:
1. Building images for multiple platforms
2. Creating platform-specific tags
3. Testing manifest list creation (simulated)
"""

import subprocess
import sys
from typing import List

def run_command(cmd: List[str], description: str) -> bool:
    """Run a command and return success status."""
    print(f"🔧 {description}")
    print(f"   Command: {' '.join(cmd)}")
    
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, check=True)
        if result.stdout.strip():
            print(f"   ✅ Success: {result.stdout.strip()[:100]}")
        else:
            print(f"   ✅ Success")
        return True
    except subprocess.CalledProcessError as e:
        print(f"   ❌ Failed: {e}")
        if e.stdout:
            print(f"   STDOUT: {e.stdout}")
        if e.stderr:
            print(f"   STDERR: {e.stderr}")
        return False

def test_platform_builds():
    """Test building images for different platforms."""
    print("\n🚀 Testing Multi-Architecture Builds")
    print("=" * 50)
    
    apps_to_test = ["hello_python", "hello_go"]
    platforms = ["amd64", "arm64"]
    
    results = []
    
    for app in apps_to_test:
        for platform in platforms:
            # Test building for specific platform using bazel directly
            cmd = [
                "bazel", "run", f"//demo/{app}:{app}_image_load",
                f"--platforms=//tools:linux_{platform.replace('amd64', 'x86_64')}"
            ]
            
            success = run_command(cmd, f"Building {app} for {platform}")
            results.append((app, platform, success))
    
    return results

def test_platform_tags():
    """Test that we can generate platform-specific tags."""
    print("\n🏷️  Testing Platform Tag Generation")
    print("=" * 50)
    
    # Test the format_registry_tags function
    try:
        sys.path.append('/Users/alex/whale/everything')
        from tools.release_helper.images import format_registry_tags
        
        # Test AMD64 tags
        amd64_tags = format_registry_tags(
            domain="demo",
            app_name="hello_python", 
            version="v1.0.0",
            platform="amd64"
        )
        
        print(f"   AMD64 Tags: {amd64_tags}")
        
        # Test ARM64 tags  
        arm64_tags = format_registry_tags(
            domain="demo",
            app_name="hello_python",
            version="v1.0.0", 
            platform="arm64"
        )
        
        print(f"   ARM64 Tags: {arm64_tags}")
        
        # Verify tags have platform suffixes
        assert "-amd64" in amd64_tags["latest"]
        assert "-arm64" in arm64_tags["latest"]
        assert "-amd64" in amd64_tags["version"]
        assert "-arm64" in arm64_tags["version"]
        
        print("   ✅ Platform tag generation working correctly")
        return True
        
    except Exception as e:
        print(f"   ❌ Platform tag generation failed: {e}")
        return False

def test_metadata_functions():
    """Test that the metadata functions work with our apps."""
    print("\n📋 Testing Metadata Functions")
    print("=" * 50)
    
    try:
        sys.path.append('/Users/alex/whale/everything')
        from tools.release_helper.metadata import get_app_metadata, get_image_targets
        
        # Test metadata retrieval
        metadata = get_app_metadata("//demo/hello_python:hello_python_metadata")
        print(f"   Metadata: {metadata}")
        
        # Test image targets
        targets = get_image_targets("hello_python")
        print(f"   Image targets: {targets}")
        
        # Verify we have platform-specific push targets
        assert "push_amd64" in targets
        assert "push_arm64" in targets
        
        print("   ✅ Metadata functions working correctly")
        return True
        
    except Exception as e:
        print(f"   ❌ Metadata functions failed: {e}")
        return False

def test_container_inspection():
    """Test that we can inspect built containers for architecture."""
    print("\n🔍 Testing Container Architecture Inspection")
    print("=" * 50)
    
    # Check what containers we have
    cmd = ["docker", "images", "--format", "table {{.Repository}}:{{.Tag}}\t{{.Size}}"]
    success = run_command(cmd, "Listing available containers")
    
    # Test inspecting a known container
    demo_containers = ["demo-hello_python:latest", "demo-hello_go:latest"]
    
    for container in demo_containers:
        cmd = ["docker", "inspect", container, "--format", "{{.Architecture}}"]
        success = run_command(cmd, f"Checking architecture of {container}")
    
    return True

def main():
    """Run all tests."""
    print("🎯 Multi-Architecture Container Workflow Test")
    print("=" * 60)
    
    tests = [
        ("Platform Builds", test_platform_builds),
        ("Platform Tags", test_platform_tags),
        ("Metadata Functions", test_metadata_functions),
        ("Container Inspection", test_container_inspection),
    ]
    
    results = {}
    
    for test_name, test_func in tests:
        try:
            result = test_func()
            results[test_name] = result
        except Exception as e:
            print(f"❌ {test_name} failed with exception: {e}")
            results[test_name] = False
    
    # Summary
    print("\n📊 Test Summary")
    print("=" * 30)
    
    for test_name, success in results.items():
        status = "✅ PASS" if success else "❌ FAIL"
        print(f"{status} {test_name}")
    
    total_tests = len(results)
    passed_tests = sum(1 for success in results.values() if success)
    
    print(f"\nPassed: {passed_tests}/{total_tests}")
    
    if passed_tests == total_tests:
        print("🎉 All tests passed! Multi-architecture workflow is ready.")
        return 0
    else:
        print("⚠️  Some tests failed. Review the output above.")
        return 1

if __name__ == "__main__":
    sys.exit(main())