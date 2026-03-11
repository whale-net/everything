"""Bazel rules for templ template generation."""

load("@rules_go//go:def.bzl", "go_library")

def templ_library(name, srcs, deps = [], go_srcs = [], visibility = None, **kwargs):
    """Generate Go code from templ files and create a go_library.
    
    Args:
        name: Name of the library
        srcs: List of .templ files
        deps: Go dependencies for the generated code
        go_srcs: Additional .go files to include (non-generated)
        visibility: Visibility of the library
        **kwargs: Additional arguments passed to go_library
    """
    
    # Convert .templ files to expected _templ.go files
    generated_files = [src.replace(".templ", "_templ.go") for src in srcs]
    
    # Combine generated files with additional Go sources
    all_srcs = generated_files + go_srcs
    
    # Create go_library with both .templ and generated .go files
    # Always include templ and templ/runtime as dependencies
    go_library(
        name = name,
        srcs = all_srcs,
        importpath = "github.com/whale-net/everything/" + native.package_name(),
        deps = deps + [
            "@com_github_a_h_templ//:templ",
            "@com_github_a_h_templ//runtime",
        ],
        visibility = visibility,
        **kwargs
    )
