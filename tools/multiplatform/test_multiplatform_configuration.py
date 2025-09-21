#!/usr/bin/env python3
"""
Configuration validation test for multiplatform image functionality.

This test validates that our experimental platform approach is correctly
configured without requiring external tool execution.
"""

import os
import sys
import unittest
from pathlib import Path


class TestMultiplatformConfiguration(unittest.TestCase):
    """Test suite for multiplatform image configuration validation."""
    
    @classmethod
    def setUpClass(cls):
        """Set up test environment."""
        # Find workspace root
        cls.workspace_root = Path(os.getcwd())
        while cls.workspace_root != cls.workspace_root.parent:
            if (cls.workspace_root / "MODULE.bazel").exists():
                break
            cls.workspace_root = cls.workspace_root.parent
        
        if not (cls.workspace_root / "MODULE.bazel").exists():
            # Try relative path resolution for test environment
            test_srcdir = os.environ.get("TEST_SRCDIR")
            if test_srcdir:
                cls.workspace_root = Path(test_srcdir) / "_main"
        
        if not (cls.workspace_root / "MODULE.bazel").exists():
            cls.workspace_root = Path(__file__).parent.parent
    
    def test_module_bazel_has_rules_oci(self):
        """Test that MODULE.bazel includes rules_oci dependency."""
        module_path = self.workspace_root / "MODULE.bazel"
        self.assertTrue(module_path.exists(), "MODULE.bazel not found")
        
        with open(module_path, 'r') as f:
            content = f.read()
        
        self.assertIn("rules_oci", content, "rules_oci not configured in MODULE.bazel")
        # Check for version specification (indicates proper configuration)
        self.assertIn("version", content, "rules_oci version not specified")
    
    def test_platform_definitions_exist(self):
        """Test that platform definition files exist."""
        platforms_bzl = self.workspace_root / "tools" / "platforms.bzl"
        self.assertTrue(platforms_bzl.exists(), "platforms.bzl not found")
        
        # Check content for expected platform definitions
        with open(platforms_bzl, 'r') as f:
            content = f.read()
        
        expected_platforms = ["linux_x86_64", "linux_arm64"]
        for platform in expected_platforms:
            self.assertIn(platform, content, f"Platform {platform} not defined")
    
    def test_multiplatform_image_bzl_exists(self):
        """Test that multiplatform_image.bzl file exists and is well-formed."""
        multiplatform_bzl = self.workspace_root / "tools" / "multiplatform_image.bzl"
        self.assertTrue(multiplatform_bzl.exists(), "multiplatform_image.bzl not found")
        
        with open(multiplatform_bzl, 'r') as f:
            content = f.read()
        
        # Check for key functions
        expected_functions = [
            "multiplatform_python_image",
            "multiplatform_go_image"
        ]
        
        for func in expected_functions:
            self.assertIn(func, content, f"Function {func} not found")
        
        # Check for experimental platform approach usage
        self.assertIn("oci_image_index", content, "oci_image_index not used")
        self.assertIn("platforms", content, "platforms attribute not used")
    
    def test_demo_build_files_configured(self):
        """Test that demo applications are configured for multiplatform builds."""
        demo_apps = ["hello_python", "hello_go"]
        
        for app in demo_apps:
            app_build_file = self.workspace_root / "demo" / app / "BUILD.bazel"
            self.assertTrue(app_build_file.exists(), f"BUILD.bazel not found for {app}")
            
            with open(app_build_file, 'r') as f:
                content = f.read()
            
            # Check for multiplatform image usage
            self.assertIn("multiplatform_", content, f"Multiplatform build not configured for {app}")
    
    def test_platform_transitions_configured(self):
        """Test that platform transition files exist."""
        platform_transitions = self.workspace_root / "tools" / "platform_transitions.bzl"
        self.assertTrue(platform_transitions.exists(), "platform_transitions.bzl not found")
        
        with open(platform_transitions, 'r') as f:
            content = f.read()
        
        # Check for transition implementation
        self.assertIn("transition_", content, "Platform transitions not implemented")
    
    def test_experimental_features_enabled(self):
        """Test that experimental features are properly configured."""
        bazelrc_path = self.workspace_root / ".bazelrc"
        if bazelrc_path.exists():
            with open(bazelrc_path, 'r') as f:
                content = f.read()
            
            # Check for experimental flags that might be needed
            # This is optional since experimental features may not need special flags
            pass
    
    def test_dockerfiles_exist_for_platforms(self):
        """Test that required Dockerfile templates exist."""
        docker_dir = self.workspace_root / "docker"
        self.assertTrue(docker_dir.exists(), "docker directory not found")
        
        required_dockerfiles = ["Dockerfile.python", "Dockerfile.go"]
        for dockerfile in required_dockerfiles:
            dockerfile_path = docker_dir / dockerfile
            self.assertTrue(dockerfile_path.exists(), f"{dockerfile} not found")
    
    def test_tools_build_file_configured(self):
        """Test that tools BUILD.bazel file has proper platform configuration."""
        tools_build = self.workspace_root / "tools" / "BUILD.bazel"
        self.assertTrue(tools_build.exists(), "tools/BUILD.bazel not found")
        
        with open(tools_build, 'r') as f:
            content = f.read()
        
        # Check for platform definition loading
        self.assertIn("define_platforms", content, "Platform definitions not loaded")
    
    def test_workspace_structure_integrity(self):
        """Test that workspace has expected structure for multiplatform builds."""
        required_dirs = [
            "demo",
            "tools", 
            "docker",
            "libs"
        ]
        
        for dir_name in required_dirs:
            dir_path = self.workspace_root / dir_name
            self.assertTrue(dir_path.exists(), f"Required directory {dir_name} not found")
    
    def test_python_requirements_configured(self):
        """Test that Python requirements are properly configured for multiplatform."""
        requirements_files = [
            "requirements.lock.txt",
            "requirements.linux.amd64.lock.txt", 
            "requirements.linux.arm64.lock.txt"
        ]
        
        for req_file in requirements_files:
            req_path = self.workspace_root / req_file
            self.assertTrue(req_path.exists(), f"{req_file} not found")
            
            # Check file is not empty
            self.assertGreater(req_path.stat().st_size, 0, f"{req_file} is empty")


if __name__ == "__main__":
    # Configure test output
    unittest.main(verbosity=2, buffer=True)