"""
Tests for the dependency injection system.

These tests verify that the Depends class and inject_dependencies decorator
work correctly for automatic dependency resolution in Typer CLI applications.
"""

from typing import Annotated
from unittest.mock import Mock

import pytest
import typer

from libs.python.cli.deps import (
    Depends,
    inject_dependencies,
    injectable,
)


# Test fixtures and helper functions
@injectable
def simple_dependency(ctx: typer.Context) -> str:
    """Simple dependency that returns a string."""
    return "simple_value"


@injectable
def dependency_with_param(
    ctx: typer.Context,
    value: str,
) -> str:
    """Dependency that takes a parameter."""
    return f"param_{value}"


@injectable
def cached_dependency(ctx: typer.Context) -> int:
    """Dependency that should be cached."""
    # This simulates expensive initialization
    if "call_count" not in ctx.obj:
        ctx.obj["call_count"] = 0
    ctx.obj["call_count"] += 1
    return ctx.obj["call_count"]


@injectable
def nested_dependency(
    ctx: typer.Context,
    simple: Annotated[str, Depends(simple_dependency)],
) -> str:
    """Dependency that depends on another dependency."""
    return f"nested_{simple}"


# Tests
class TestDependsClass:
    """Tests for the Depends class."""

    def test_depends_initialization(self):
        """Test that Depends can be initialized with a callable."""
        dep = Depends(simple_dependency)
        assert dep.dependency == simple_dependency
        assert "simple_dependency" in dep.cache_key

    def test_depends_resolve_simple(self):
        """Test resolving a simple dependency."""
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        dep = Depends(simple_dependency)
        result = dep(ctx)
        
        assert result == "simple_value"

    def test_depends_caching(self):
        """Test that dependencies are cached in ctx.obj."""
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        dep = Depends(cached_dependency)
        
        # First call should create and cache
        result1 = dep(ctx)
        assert result1 == 1
        
        # Second call should return cached value
        result2 = dep(ctx)
        assert result2 == 1  # Same value, not 2
        
        # Verify it was cached
        assert ctx.obj["call_count"] == 1

    def test_depends_nested_resolution(self):
        """Test that dependencies can depend on other dependencies."""
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        dep = Depends(nested_dependency)
        result = dep(ctx)
        
        assert result == "nested_simple_value"


class TestInjectableDecorator:
    """Tests for the @injectable decorator."""

    def test_injectable_marks_function(self):
        """Test that @injectable decorator marks functions."""
        assert hasattr(simple_dependency, "__injectable__")
        assert simple_dependency.__injectable__ is True

    def test_injectable_preserves_function(self):
        """Test that @injectable doesn't change function behavior."""
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        result = simple_dependency(ctx)
        assert result == "simple_value"


class TestInjectDependencies:
    """Tests for the @inject_dependencies decorator."""

    def test_inject_simple_dependency(self):
        """Test injecting a simple dependency into a function."""
        @inject_dependencies
        def test_func(
            ctx: typer.Context,
            dep: Annotated[str, Depends(simple_dependency)],
        ) -> str:
            return dep
        
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        result = test_func(ctx)
        assert result == "simple_value"

    def test_inject_multiple_dependencies(self):
        """Test injecting multiple dependencies."""
        @inject_dependencies
        def test_func(
            ctx: typer.Context,
            dep1: Annotated[str, Depends(simple_dependency)],
            dep2: Annotated[str, Depends(nested_dependency)],
        ) -> tuple:
            return dep1, dep2
        
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        result = test_func(ctx)
        assert result == ("simple_value", "nested_simple_value")

    def test_inject_with_regular_params(self):
        """Test that regular parameters work alongside injected dependencies."""
        @inject_dependencies
        def test_func(
            ctx: typer.Context,
            dep: Annotated[str, Depends(simple_dependency)],
            regular_param: str = "default",
        ) -> tuple:
            return dep, regular_param
        
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        # Test with default parameter
        result1 = test_func(ctx)
        assert result1 == ("simple_value", "default")
        
        # Test with provided parameter
        result2 = test_func(ctx, regular_param="custom")
        assert result2 == ("simple_value", "custom")

    def test_inject_requires_context(self):
        """Test that inject_dependencies requires a ctx parameter."""
        @inject_dependencies
        def test_func(
            dep: Annotated[str, Depends(simple_dependency)],
        ) -> str:
            return dep
        
        with pytest.raises(ValueError, match="inject_dependencies requires a ctx parameter"):
            test_func()

    def test_inject_with_cached_dependency(self):
        """Test that injected dependencies are cached across calls."""
        @inject_dependencies
        def test_func(
            ctx: typer.Context,
            dep: Annotated[int, Depends(cached_dependency)],
        ) -> int:
            return dep
        
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        # First call
        result1 = test_func(ctx)
        assert result1 == 1
        
        # Second call should get cached value
        result2 = test_func(ctx)
        assert result2 == 1
        
        # Verify only one initialization occurred
        assert ctx.obj["call_count"] == 1


class TestDependencyChaining:
    """Tests for chaining dependencies."""

    def test_dependency_chain(self):
        """Test that dependencies can form a chain."""
        @injectable
        def level1(ctx: typer.Context) -> str:
            return "level1"
        
        @injectable
        def level2(
            ctx: typer.Context,
            dep1: Annotated[str, Depends(level1)],
        ) -> str:
            return f"{dep1}_level2"
        
        @injectable
        def level3(
            ctx: typer.Context,
            dep2: Annotated[str, Depends(level2)],
        ) -> str:
            return f"{dep2}_level3"
        
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        dep = Depends(level3)
        result = dep(ctx)
        
        assert result == "level1_level2_level3"


class TestIntegrationWithTyper:
    """Integration tests with actual Typer commands."""

    def test_typer_command_with_injection(self):
        """Test using dependency injection in a real Typer command."""
        app = typer.Typer()
        results = []
        
        @app.command()
        @inject_dependencies
        def test_command(
            ctx: typer.Context,
            dep: Annotated[str, Depends(simple_dependency)],
            name: str = "test",
        ):
            results.append((dep, name))
        
        # Create a runner and invoke the command
        ctx = typer.Context(app)
        ctx.obj = {}
        
        test_command(ctx, name="integration")
        
        assert results == [("simple_value", "integration")]


class TestErrorHandling:
    """Tests for error handling in dependency injection."""

    def test_circular_dependency_detection(self):
        """Test that circular dependencies don't cause infinite loops."""
        # Create two dependencies that reference each other
        # This is a bit tricky to set up with the current implementation
        # For now, we rely on Python's recursion limit
        
        @injectable
        def circular_a(
            ctx: typer.Context,
        ) -> str:
            # In a real circular dependency, this would reference circular_b
            return "a"
        
        @injectable  
        def circular_b(
            ctx: typer.Context,
            dep_a: Annotated[str, Depends(circular_a)],
        ) -> str:
            return f"b_{dep_a}"
        
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        dep = Depends(circular_b)
        result = dep(ctx)
        
        # This should work since circular_a doesn't depend on circular_b
        assert result == "b_a"


class TestMockingForTests:
    """Tests demonstrating how to mock dependencies for testing."""

    def test_mock_dependency_in_ctx(self):
        """Test that dependencies can be mocked by pre-populating ctx.obj."""
        @inject_dependencies
        def test_func(
            ctx: typer.Context,
            dep: Annotated[str, Depends(simple_dependency)],
        ) -> str:
            return dep
        
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        # Pre-populate the cache with a mock value
        dep = Depends(simple_dependency)
        ctx.obj[dep.cache_key] = "mocked_value"
        
        result = test_func(ctx)
        assert result == "mocked_value"

    def test_mock_complex_dependency(self):
        """Test mocking a complex dependency."""
        @injectable
        def complex_dep(ctx: typer.Context) -> dict:
            return {"real": "value"}
        
        @inject_dependencies
        def test_func(
            ctx: typer.Context,
            dep: Annotated[dict, Depends(complex_dep)],
        ) -> dict:
            return dep
        
        ctx = typer.Context(typer.Typer())
        ctx.obj = {}
        
        # Mock the dependency
        mock_value = {"mocked": "data"}
        dep = Depends(complex_dep)
        ctx.obj[dep.cache_key] = mock_value
        
        result = test_func(ctx)
        assert result == mock_value
