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
    
    # Generate Go files from templ files using templ binary
    generated_files = []
    for src in srcs:
        out = src.replace(".templ", "_templ.go")
        generated_files.append(out)
        
        native.genrule(
            name = name + "_gen_" + src.replace("/", "_").replace(".templ", ""),
            srcs = [src],
            outs = [out],
            cmd = "$(location @com_github_a_h_templ//cmd/templ) generate -f $(SRCS) -stdout > $(OUTS)",
            tools = ["@com_github_a_h_templ//cmd/templ"],
        )
    
    # Combine generated files with additional Go sources
    all_srcs = generated_files + go_srcs
    
    # Create go_library with generated .go files
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
