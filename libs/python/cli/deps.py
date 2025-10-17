"""
Dependency injection system for Typer CLI applications.

This module provides a lightweight dependency injection system that allows
"define-once" dependencies to be reused across multiple CLI commands.

For complete documentation including benefits and usage patterns, see:
    - docs/DEPENDENCY_INJECTION.md - Complete guide
    - docs/DI_QUICK_REFERENCE.md - Quick reference

Example Usage:
    ```python
    from libs.python.cli.deps import Depends, injectable
    
    # Define an injectable dependency
    @injectable
    def get_database(
        ctx: typer.Context,
        database_url: Annotated[str, typer.Option(..., envvar="DATABASE_URL")],
    ) -> Engine:
        if "db_engine" not in ctx.obj:
            ctx.obj["db_engine"] = create_engine(database_url)
        return ctx.obj["db_engine"]
    
    # Use it in a command
    @app.command()
    def my_command(
        ctx: typer.Context,
        db: Annotated[Engine, Depends(get_database)],
    ):
        # db is automatically resolved and passed to the function
        print(f"Using database: {db}")
    ```
"""

import functools
import inspect
import logging
from typing import Annotated, Any, Callable, TypeVar, get_args, get_origin

import typer

logger = logging.getLogger(__name__)

T = TypeVar("T")


class Depends:
    """
    Marks a parameter as a dependency that should be resolved automatically.
    
    Args:
        dependency: A callable that will be invoked to resolve the dependency.
                   The callable should accept a typer.Context as its first parameter
                   and may accept additional parameters (which themselves can be dependencies).
    
    Example:
        ```python
        def get_db_engine(
            ctx: typer.Context,
            database_url: Annotated[str, typer.Option(..., envvar="DATABASE_URL")],
        ) -> Engine:
            return create_engine(database_url)
        
        @app.command()
        def my_command(
            ctx: typer.Context,
            engine: Annotated[Engine, Depends(get_db_engine)],
        ):
            # engine is automatically resolved
            pass
        ```
    """

    def __init__(self, dependency: Callable[..., T]):
        self.dependency = dependency
        self.cache_key = f"__dep__{dependency.__module__}.{dependency.__name__}"

    def __call__(self, ctx: typer.Context) -> T:
        """
        Resolve the dependency.
        
        Args:
            ctx: The Typer context object for storing resolved dependencies
            
        Returns:
            The resolved dependency value
        """
        # Check cache first
        if self.cache_key in ctx.obj:
            logger.debug(f"Returning cached dependency: {self.cache_key}")
            return ctx.obj[self.cache_key]

        # Resolve the dependency
        logger.debug(f"Resolving dependency: {self.cache_key}")
        
        # Get the signature of the dependency function
        sig = inspect.signature(self.dependency)
        kwargs = {}
        
        # Resolve each parameter of the dependency function
        for param_name, param in sig.parameters.items():
            # Skip 'ctx' parameter - we'll pass it explicitly
            if param_name == "ctx":
                continue
            
            # Check if this parameter has a Depends annotation
            if param.annotation != inspect.Parameter.empty:
                # Handle Annotated types
                origin = get_origin(param.annotation)
                if origin is Annotated:
                    args = get_args(param.annotation)
                    # Check if any of the metadata is a Depends instance
                    for metadata in args[1:]:
                        if isinstance(metadata, Depends):
                            # Recursively resolve this dependency
                            kwargs[param_name] = metadata(ctx)
                            break
                    else:
                        # No Depends found, this must be a Typer option/argument
                        # These will be handled by Typer itself
                        pass
        
        # Call the dependency function with resolved dependencies
        result = self.dependency(ctx, **kwargs)
        
        # Cache the result
        ctx.obj[self.cache_key] = result
        logger.debug(f"Cached dependency: {self.cache_key}")
        
        return result


def injectable(func: Callable[..., T]) -> Callable[..., T]:
    """
    Decorator that marks a function as an injectable dependency.
    
    This is optional but helps with documentation and IDE support.
    Functions used with Depends don't need this decorator, but it can
    make the code more self-documenting.
    
    Args:
        func: The function to mark as injectable
        
    Returns:
        The same function, unchanged
        
    Example:
        ```python
        @injectable
        def get_database(
            ctx: typer.Context,
            database_url: Annotated[str, typer.Option(..., envvar="DATABASE_URL")],
        ) -> Engine:
            return create_engine(database_url)
        ```
    """
    func.__injectable__ = True
    return func


def inject_dependencies(func: Callable) -> Callable:
    """
    Decorator that automatically injects dependencies into a Typer command function.
    
    This decorator inspects the function signature for Depends annotations and
    automatically resolves those dependencies before calling the function.
    
    Args:
        func: The Typer command function to wrap
        
    Returns:
        A wrapped function that handles dependency injection
        
    Example:
        ```python
        @app.command()
        @inject_dependencies
        def my_command(
            ctx: typer.Context,
            db: Annotated[Engine, Depends(get_database)],
            name: str = "default",
        ):
            # db is automatically injected
            print(f"Using {name} with {db}")
        ```
    """

    @functools.wraps(func)
    def wrapper(*args, **kwargs):
        # Get the function signature
        sig = inspect.signature(func)
        
        # Find the ctx parameter
        ctx = None
        for arg_name, arg_value in zip(sig.parameters.keys(), args):
            if arg_name == "ctx" and isinstance(arg_value, typer.Context):
                ctx = arg_value
                break
        
        if ctx is None and "ctx" in kwargs:
            ctx = kwargs["ctx"]
        
        if ctx is None:
            raise ValueError("inject_dependencies requires a ctx parameter")
        
        # Resolve dependencies
        resolved_kwargs = dict(kwargs)
        for param_name, param in sig.parameters.items():
            # Skip parameters already provided
            if param_name in kwargs:
                continue
            
            # Check for Depends annotation
            if param.annotation != inspect.Parameter.empty:
                origin = get_origin(param.annotation)
                if origin is Annotated:
                    args_tuple = get_args(param.annotation)
                    # Check if any of the metadata is a Depends instance
                    for metadata in args_tuple[1:]:
                        if isinstance(metadata, Depends):
                            # Resolve the dependency
                            resolved_kwargs[param_name] = metadata(ctx)
                            break
        
        # Call the original function with resolved dependencies
        return func(*args, **resolved_kwargs)
    
    return wrapper


# Legacy support: create type aliases that work like the old pattern
def create_dependency(
    setup_func: Callable,
    cache_key: str = None,
) -> Callable[[typer.Context], Any]:
    """
    Create a dependency factory from a legacy setup function.
    
    This helper function makes it easy to convert existing setup_* functions
    into injectable dependencies.
    
    Args:
        setup_func: The setup function to convert (e.g., setup_db, setup_slack)
        cache_key: Optional cache key (defaults to function name)
        
    Returns:
        A dependency factory function that can be used with Depends
        
    Example:
        ```python
        # Old pattern:
        def setup_db(ctx: typer.Context, database_url: str):
            ctx.obj["db"] = create_engine(database_url)
        
        # New pattern:
        get_db = create_dependency(setup_db, "db")
        
        # Use it:
        @app.command()
        def my_command(
            ctx: typer.Context,
            db: Annotated[Any, Depends(get_db)],
        ):
            pass
        ```
    """
    key = cache_key or setup_func.__name__
    
    @injectable
    def dependency_factory(ctx: typer.Context, *args, **kwargs):
        if key not in ctx.obj:
            setup_func(ctx, *args, **kwargs)
        return ctx.obj.get(key)
    
    return dependency_factory
