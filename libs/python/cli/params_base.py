"""
Base decorator factory for CLI parameter injection.

This module provides the core _create_param_decorator function that all
provider modules use to create their decorators. It has no dependencies
on other providers, making it safe to import from any provider module.
"""

from functools import wraps
from typing import Callable
import inspect


def _create_param_decorator(
    param_specs: list[tuple[str, inspect.Parameter]],
    context_key: str,
    param_extractor: Callable,
) -> Callable:
    """
    Factory for creating parameter injection decorators.
    
    Used by provider modules to create their decorators.
    
    Args:
        param_specs: List of (param_name, Parameter) tuples to inject
        context_key: Key to store extracted params in ctx.obj
        param_extractor: Function to extract params from kwargs into dict
    
    Returns:
        Decorator function that injects parameters
    """
    def decorator(func: Callable) -> Callable:
        # Get the original signature
        sig = inspect.signature(func)
        params = list(sig.parameters.values())
        
        # Add new parameters to signature
        params.extend([param for _, param in param_specs])
        
        # Create new signature
        new_sig = sig.replace(parameters=params)
        
        @wraps(func)
        def wrapper(*args, **kwargs):
            # Extract ctx from args (first positional arg in Typer callbacks)
            ctx = args[0] if args else kwargs.get('ctx')
            
            if ctx:
                ctx.ensure_object(dict)
                # Extract params using provided function
                ctx.obj[context_key] = param_extractor(kwargs)
            
            # Call original function
            return func(*args, **kwargs)
        
        # Update wrapper's signature so Typer can inspect it
        wrapper.__signature__ = new_sig
        return wrapper
    
    return decorator
