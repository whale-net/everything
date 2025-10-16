#!/usr/bin/env python3
"""
Standalone test of the dependency injection system.

This test verifies the core dependency injection functionality without
requiring external dependencies or environment setup.
"""

import sys
from typing import Annotated

# Mock typer.Context for testing
class MockContext:
    def __init__(self):
        self.obj = {}


class MockTyper:
    pass


class MockOption:
    def __init__(self, *args, **kwargs):
        pass


# Mock typer module
class MockTyperModule:
    Context = MockContext
    Typer = MockTyper
    Option = MockOption


# Inject mock
sys.modules['typer'] = MockTyperModule()

# Now import our modules
from friendly_computing_machine.src.friendly_computing_machine.cli.deps import (
    Depends,
    inject_dependencies,
    injectable,
)


# Define test dependencies
@injectable
def simple_dep(ctx) -> str:
    """Simple dependency."""
    return "simple_value"


@injectable
def cached_dep(ctx) -> int:
    """Cached dependency."""
    if "call_count" not in ctx.obj:
        ctx.obj["call_count"] = 0
    ctx.obj["call_count"] += 1
    return ctx.obj["call_count"]


@injectable
def chained_dep(ctx, simple: Annotated[str, Depends(simple_dep)]) -> str:
    """Dependency that depends on another."""
    return f"chained_{simple}"


# Test functions
def test_simple_dependency():
    """Test simple dependency resolution."""
    print("Test 1: Simple dependency resolution")
    ctx = MockContext()
    dep = Depends(simple_dep)
    result = dep(ctx)
    assert result == "simple_value", f"Expected 'simple_value', got {result}"
    print("  ✓ Simple dependency works")


def test_caching():
    """Test dependency caching."""
    print("\nTest 2: Dependency caching")
    ctx = MockContext()
    dep = Depends(cached_dep)
    
    result1 = dep(ctx)
    print(f"  First call: {result1}")
    assert result1 == 1, f"Expected 1, got {result1}"
    
    result2 = dep(ctx)
    print(f"  Second call: {result2}")
    assert result2 == 1, f"Expected 1 (cached), got {result2}"
    
    assert ctx.obj["call_count"] == 1, "Dependency was called more than once"
    print("  ✓ Caching works correctly")


def test_chained_dependencies():
    """Test chained dependency resolution."""
    print("\nTest 3: Chained dependencies")
    ctx = MockContext()
    dep = Depends(chained_dep)
    result = dep(ctx)
    assert result == "chained_simple_value", f"Expected 'chained_simple_value', got {result}"
    print("  ✓ Chained dependencies work")


def test_inject_decorator():
    """Test @inject_dependencies decorator."""
    print("\nTest 4: @inject_dependencies decorator")
    
    @inject_dependencies
    def test_func(
        ctx,
        dep: Annotated[str, Depends(simple_dep)],
        regular_param: str = "default",
    ):
        return dep, regular_param
    
    ctx = MockContext()
    result = test_func(ctx)
    assert result == ("simple_value", "default"), f"Expected ('simple_value', 'default'), got {result}"
    print("  ✓ Decorator injection works")
    
    result2 = test_func(ctx, regular_param="custom")
    assert result2 == ("simple_value", "custom"), f"Expected ('simple_value', 'custom'), got {result2}"
    print("  ✓ Mixed parameters work")


def test_multiple_dependencies():
    """Test multiple dependencies in one function."""
    print("\nTest 5: Multiple dependencies")
    
    @inject_dependencies
    def multi_func(
        ctx,
        dep1: Annotated[str, Depends(simple_dep)],
        dep2: Annotated[str, Depends(chained_dep)],
        param: int = 42,
    ):
        return dep1, dep2, param
    
    ctx = MockContext()
    result = multi_func(ctx)
    assert result == ("simple_value", "chained_simple_value", 42)
    print("  ✓ Multiple dependencies work")


def test_injectable_marker():
    """Test @injectable decorator."""
    print("\nTest 6: @injectable decorator")
    assert hasattr(simple_dep, "__injectable__")
    assert simple_dep.__injectable__ is True
    print("  ✓ @injectable marker works")


def main():
    """Run all tests."""
    print("=" * 60)
    print("Dependency Injection System - Standalone Tests")
    print("=" * 60)
    
    try:
        test_simple_dependency()
        test_caching()
        test_chained_dependencies()
        test_inject_decorator()
        test_multiple_dependencies()
        test_injectable_marker()
        
        print("\n" + "=" * 60)
        print("✨ All tests passed!")
        print("=" * 60)
        return 0
    except AssertionError as e:
        print(f"\n❌ Test failed: {e}")
        return 1
    except Exception as e:
        print(f"\n❌ Unexpected error: {e}")
        import traceback
        traceback.print_exc()
        return 1


if __name__ == "__main__":
    sys.exit(main())
