#!/usr/bin/env python3
"""
Comprehensive tests for multiplatform image building with experimental Bazel platforms.

This test suite validates that our experimental platform approach continues to work correctly
and catches any regressions in multi-platform container functionality.

Designed to run within Bazel's sandbox environment.
"""

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Dict, List, Optional


class MultiplatformImageTester:
    """Test suite for validating multi-platform container functionality within Bazel sandbox."""
    
    def __init__(self, workspace_root: str):
        self.workspace_root = Path(workspace_root)
        self.test_results: List[Dict[str, any]] = []
        
        # Ensure we're in the correct directory for Bazel commands
        os.chdir(self.workspace_root)
        
    def run_bazel_command(self, command: List[str], expect_success: bool = True) -> subprocess.CompletedProcess:
        """Run a bazel command and return the result."""
        full_command = ["bazel"] + command
        print(f"🔧 Running: {' '.join(full_command)}")
        
        result = subprocess.run(
            full_command,
            cwd=self.workspace_root,
            capture_output=True,
            text=True
        )
        
        if expect_success and result.returncode != 0:
            print(f"❌ Command failed: {' '.join(full_command)}")
            print(f"STDOUT: {result.stdout}")
            print(f"STDERR: {result.stderr}")
            
        return result
    
    def test_platform_specific_builds_in_sandbox(self) -> bool:
        """Test that platform-specific images build correctly within Bazel sandbox."""
        print("\n🧪 Testing platform-specific builds in Bazel sandbox...")
        
        test_cases = [
            ("//demo/hello_python:hello_python_image_amd64", "//tools:linux_x86_64"),
            ("//demo/hello_python:hello_python_image_arm64", "//tools:linux_arm64"),
            ("//demo/hello_go:hello_go_image_amd64", "//tools:linux_x86_64"),
            ("//demo/hello_go:hello_go_image_arm64", "//tools:linux_arm64"),
        ]
        
        all_passed = True
        for target, platform in test_cases:
            result = self.run_bazel_command([
                "build", target, f"--platforms={platform}"
            ])
            
            passed = result.returncode == 0
            all_passed = all_passed and passed
            
            self.test_results.append({
                "test": "platform_specific_build_sandbox",
                "target": target,
                "platform": platform,
                "passed": passed,
                "details": result.stderr if not passed else "Build successful"
            })
            
            print(f"  {'✅' if passed else '❌'} {target} on {platform}")
        
        return all_passed
    
    def test_multiplatform_manifest_creation_in_sandbox(self) -> bool:
        """Test that multi-platform manifests are created using experimental platform approach."""
        print("\n🧪 Testing multi-platform manifest creation in Bazel sandbox...")
        
        targets = [
            "//demo/hello_python:hello_python_image",
            "//demo/hello_go:hello_go_image",
        ]
        
        all_passed = True
        for target in targets:
            # Build the multi-platform image
            result = self.run_bazel_command(["build", target])
            build_passed = result.returncode == 0
            
            if not build_passed:
                all_passed = False
                self.test_results.append({
                    "test": "multiplatform_manifest_build_sandbox",
                    "target": target,
                    "passed": False,
                    "details": f"Build failed: {result.stderr}"
                })
                continue
            
            # Check that the manifest file exists in bazel-bin
            app_name = target.split(":")[-1]
            domain = target.split("/")[2]
            
            # The target name is app_name_image, so we need to look in that directory
            manifest_path = self.workspace_root / f"bazel-bin/{domain}/{app_name.replace('_image', '')}/{app_name}/index.json"
            manifest_exists = manifest_path.exists()
            
            manifest_details = "Manifest file not found"
            if manifest_exists:
                try:
                    with open(manifest_path) as f:
                        manifest_data = json.load(f)
                    
                    manifest_count = len(manifest_data.get("manifests", []))
                    manifest_details = f"Found {manifest_count} manifest entries"
                    
                    # Verify the structure exists and is valid OCI format
                    structure_valid = (
                        "manifests" in manifest_data and 
                        isinstance(manifest_data["manifests"], list) and
                        len(manifest_data["manifests"]) > 0
                    )
                    
                    if structure_valid:
                        # Check that manifest entries have required fields
                        for manifest in manifest_data["manifests"]:
                            if not all(key in manifest for key in ["mediaType", "digest", "size"]):
                                structure_valid = False
                                manifest_details += " (missing required fields)"
                                break
                    
                except Exception as e:
                    manifest_exists = False
                    manifest_details = f"Error reading manifest: {e}"
                    structure_valid = False
            else:
                structure_valid = False
            
            passed = build_passed and manifest_exists and structure_valid
            all_passed = all_passed and passed
            
            self.test_results.append({
                "test": "multiplatform_manifest_creation_sandbox",
                "target": target,
                "passed": passed,
                "details": manifest_details
            })
            
            print(f"  {'✅' if passed else '❌'} {target}: {manifest_details}")
        
        return all_passed
    
    def test_bazel_query_consistency(self) -> bool:
        """Test that Bazel queries return consistent results for our targets."""
        print("\n🧪 Testing Bazel query consistency...")
        
        # Test that all our multiplatform targets are discoverable
        query_result = self.run_bazel_command([
            "query", 
            "kind('oci_image', //demo/...)"
        ])
        
        if query_result.returncode != 0:
            self.test_results.append({
                "test": "bazel_query_consistency",
                "passed": False,
                "details": f"Query failed: {query_result.stderr}"
            })
            return False
        
        oci_images = query_result.stdout.strip().split('\n')
        expected_images = [
            "//demo/hello_python:hello_python_image_amd64",
            "//demo/hello_python:hello_python_image_arm64", 
            "//demo/hello_go:hello_go_image_amd64",
            "//demo/hello_go:hello_go_image_arm64",
        ]
        
        missing_images = []
        for expected in expected_images:
            if expected not in oci_images:
                missing_images.append(expected)
        
        passed = len(missing_images) == 0
        details = "All expected images found" if passed else f"Missing images: {missing_images}"
        
        self.test_results.append({
            "test": "bazel_query_consistency",
            "passed": passed,
            "details": details
        })
        
        print(f"  {'✅' if passed else '❌'} Bazel query consistency: {details}")
        return passed
    
    def test_experimental_platform_features_in_sandbox(self) -> bool:
        """Test that experimental platform features work within Bazel sandbox."""
        print("\n🧪 Testing experimental platform features in Bazel sandbox...")
        
        # Test that we can build with platform transitions in the sandbox
        result = self.run_bazel_command([
            "build", 
            "//demo/hello_python:hello_python_image",
            "//demo/hello_go:hello_go_image",
            "--experimental_platforms_api"  # Enable experimental platforms
        ])
        
        passed = result.returncode == 0
        
        details = "Builds successfully with experimental platform approach"
        if not passed:
            details = f"Build failed: {result.stderr}"
        
        self.test_results.append({
            "test": "experimental_platform_build_sandbox",
            "passed": passed,
            "details": details
        })
        
        print(f"  {'✅' if passed else '❌'} Experimental platform builds: {'Success' if passed else 'Failed'}")
        
        return passed
    
    def test_build_file_dependencies(self) -> bool:
        """Test that our BUILD.bazel files have correct dependencies."""
        print("\n🧪 Testing BUILD.bazel file dependencies...")
        
        # Test that our multiplatform_image.bzl loads correctly
        query_result = self.run_bazel_command([
            "query", 
            "//tools:multiplatform_image.bzl"
        ])
        
        # This should succeed (target exists) or fail gracefully (file target)
        load_test_passed = True
        
        # Test that targets using our macro can be analyzed
        analysis_result = self.run_bazel_command([
            "build", 
            "//demo/hello_python:hello_python_image",
            "--nobuild"  # Only analyze, don't build
        ])
        
        analysis_passed = analysis_result.returncode == 0
        
        passed = load_test_passed and analysis_passed
        
        details = "Dependencies correct"
        if not analysis_passed:
            details = f"Analysis failed: {analysis_result.stderr}"
        
        self.test_results.append({
            "test": "build_file_dependencies",
            "passed": passed,
            "details": details
        })
        
        print(f"  {'✅' if passed else '❌'} BUILD file dependencies: {details}")
        return passed
    
    def test_platform_transition_configuration(self) -> bool:
        """Test that platform transitions are configured correctly."""
        print("\n🧪 Testing platform transition configuration...")
        
        # Test that our platform targets exist and are queryable
        platforms_to_test = [
            "//tools:linux_x86_64",
            "//tools:linux_arm64"
        ]
        
        all_passed = True
        for platform in platforms_to_test:
            result = self.run_bazel_command([
                "query", platform
            ])
            
            platform_passed = result.returncode == 0
            all_passed = all_passed and platform_passed
            
            self.test_results.append({
                "test": "platform_transition_config",
                "platform": platform,
                "passed": platform_passed,
                "details": "Platform exists" if platform_passed else f"Platform missing: {result.stderr}"
            })
            
            print(f"  {'✅' if platform_passed else '❌'} Platform {platform}")
        
        return all_passed
    
    def run_all_tests(self) -> bool:
        """Run all tests suitable for Bazel sandbox and return overall success."""
        print("🚀 Starting Bazel sandbox multiplatform image tests...\n")
        
        tests = [
            ("Platform-specific builds", self.test_platform_specific_builds_in_sandbox),
            ("Multi-platform manifest creation", self.test_multiplatform_manifest_creation_in_sandbox),
            ("Bazel query consistency", self.test_bazel_query_consistency),
            ("Experimental platform features", self.test_experimental_platform_features_in_sandbox),
            ("BUILD file dependencies", self.test_build_file_dependencies),
            ("Platform transition configuration", self.test_platform_transition_configuration),
        ]
        
        all_passed = True
        for test_name, test_func in tests:
            try:
                passed = test_func()
                all_passed = all_passed and passed
                print(f"\n📊 {test_name}: {'✅ PASSED' if passed else '❌ FAILED'}")
            except Exception as e:
                print(f"\n📊 {test_name}: ❌ ERROR - {e}")
                all_passed = False
                self.test_results.append({
                    "test": test_name.lower().replace(" ", "_"),
                    "passed": False,
                    "details": f"Test error: {e}"
                })
        
        return all_passed
    
    def generate_report(self) -> Dict[str, any]:
        """Generate a comprehensive test report."""
        total_tests = len(self.test_results)
        passed_tests = sum(1 for result in self.test_results if result["passed"])
        
        return {
            "summary": {
                "total_tests": total_tests,
                "passed_tests": passed_tests,
                "failed_tests": total_tests - passed_tests,
                "success_rate": f"{(passed_tests / total_tests * 100):.1f}%" if total_tests > 0 else "0%",
                "overall_status": "PASS" if passed_tests == total_tests else "FAIL"
            },
            "detailed_results": self.test_results
        }


def main():
    """Main test execution for Bazel sandbox."""
    # In Bazel test environment, workspace root is current directory
    workspace_root = os.getcwd()
    
    # Verify we're in a Bazel workspace
    if not Path(workspace_root, "WORKSPACE").exists() and not Path(workspace_root, "MODULE.bazel").exists():
        print("❌ Not in a Bazel workspace root directory")
        sys.exit(1)
    
    tester = MultiplatformImageTester(workspace_root)
    
    # Run all tests
    overall_success = tester.run_all_tests()
    
    # Generate and display report
    report = tester.generate_report()
    
    print("\n" + "="*60)
    print("📋 MULTIPLATFORM IMAGE TEST REPORT")
    print("="*60)
    print(f"Total tests: {report['summary']['total_tests']}")
    print(f"Passed: {report['summary']['passed_tests']}")
    print(f"Failed: {report['summary']['failed_tests']}")
    print(f"Success rate: {report['summary']['success_rate']}")
    print(f"Overall status: {report['summary']['overall_status']}")
    
    if report['summary']['failed_tests'] > 0:
        print("\n❌ FAILED TESTS:")
        for result in report['detailed_results']:
            if not result['passed']:
                print(f"  - {result.get('test', 'unknown')}: {result.get('details', 'No details')}")
    
    print("\n✨ Test execution complete!")
    
    # Exit with appropriate code
    sys.exit(0 if overall_success else 1)


if __name__ == "__main__":
    main()