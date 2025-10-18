"""Temporal workflow engine provider.

Provides decorator for Temporal connection parameters and initialization.
"""

import inspect
import logging
from dataclasses import dataclass
from typing import Annotated, Callable, Optional

import typer

logger = logging.getLogger(__name__)


# Type alias for CLI parameters
TemporalHost = Annotated[str, typer.Option(..., envvar="TEMPORAL_HOST")]


@dataclass
class TemporalContext:
    """Temporal connection context.
    
    Attributes:
        host: Temporal server host
    """
    
    host: str


def create_temporal_context(
    host: TemporalHost,
    temporal_initializer: Optional[callable] = None,
) -> TemporalContext:
    """Create Temporal context with connection configuration.
    
    Args:
        host: Temporal server host
        temporal_initializer: Optional function to initialize Temporal (e.g., init_temporal)
    
    Returns:
        TemporalContext with host configuration
    """
    logger.debug("Creating Temporal context for host: %s", host)
    
    ctx = TemporalContext(host=host)
    
    # Run custom initialization if provided
    if temporal_initializer:
        logger.debug("Running Temporal initializer")
        temporal_initializer(ctx)
    
    logger.debug("Temporal context created successfully")
    
    return ctx


# ==============================================================================
# Decorator for injecting Temporal parameters
# ==============================================================================

def temporal_params(func: Callable) -> Callable:
    """Decorator that injects Temporal parameters into the callback.
    
    Adds Temporal host parameter and stores in ctx.obj['temporal'].
    
    Usage:
        @app.callback()
        @temporal_params
        def callback(ctx: typer.Context, ...):
            temporal = ctx.obj['temporal']
            # temporal = {'host': '...'}
    """
    from libs.python.cli.params_base import _create_param_decorator
    
    param_specs = [
        ('temporal_host', inspect.Parameter(
            'temporal_host', inspect.Parameter.KEYWORD_ONLY,
            annotation=TemporalHost
        )),
    ]
    
    def extractor(kwargs):
        return {
            'host': kwargs.pop('temporal_host'),
        }
    
    return _create_param_decorator(param_specs, 'temporal', extractor)(func)
