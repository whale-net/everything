"""Pytest integration for tests that automatically invokes pytest."""

load("@aspect_rules_py//py:defs.bzl", _py_test = "py_test")

def py_test(name, srcs, deps = [], main = None, args = [], **kwargs):
    """Wrapper around py_test that automatically uses pytest as the test runner.
    
    This eliminates the need for __main__ blocks in test files. pytest will
    automatically discover and run test functions.
    
    Args:
        name: Test target name
        srcs: Test source files  
        deps: Dependencies (pytest will be added automatically)
        main: Optional main file (if not provided, pytest runner is used)
        args: Additional args to pass (we add pytest ignores automatically)
        **kwargs: Other py_test arguments (size, etc.)
    """
    # Add pytest dependency if not already present
    test_deps = list(deps)
    if "@pypi//:pytest" not in test_deps:
        test_deps.append("@pypi//:pytest")
    
    # Use aspect_rules_py's built-in pytest_main flag if no explicit main
    test_args = list(args)
    if not main:
        kwargs["pytest_main"] = True
        # Get the package path from the current package
        pkg = native.package_name()
        # Ignore the generated pytest_main.py file so pytest doesn't try to collect it
        if pkg:
            test_args.append("--ignore={}/{}.pytest_main.py".format(pkg, name))
        else:
            test_args.append("--ignore={}.pytest_main.py".format(name))
    
    _py_test(
        name = name,
        srcs = srcs,
        main = main,
        deps = test_deps,
        args = test_args,
        **kwargs
    )


