"""Google Gemini AI provider.

Provides decorator for Gemini API key parameter and initialization.
"""

import inspect
import logging
from dataclasses import dataclass
from typing import Annotated, Callable, Optional

import typer

logger = logging.getLogger(__name__)


# Type alias for CLI parameters
GoogleApiKey = Annotated[str, typer.Option(..., envvar="GOOGLE_API_KEY")]


@dataclass
class GeminiContext:
    """Gemini AI context.
    
    Attributes:
        api_key: Google API key for Gemini
    """
    
    api_key: str


def create_gemini_context(
    api_key: GoogleApiKey,
    gemini_initializer: Optional[callable] = None,
) -> GeminiContext:
    """Create Gemini context with API configuration.
    
    Args:
        api_key: Google API key
        gemini_initializer: Optional function to initialize Gemini (e.g., genai.configure)
    
    Returns:
        GeminiContext with API key
    """
    logger.debug("Creating Gemini context")
    
    ctx = GeminiContext(api_key=api_key)
    
    # Run custom initialization if provided
    if gemini_initializer:
        logger.debug("Running Gemini initializer")
        gemini_initializer(ctx)
    
    logger.debug("Gemini context created successfully")
    
    return ctx


# ==============================================================================
# Decorator for injecting Gemini parameters
# ==============================================================================

def gemini_params(func: Callable) -> Callable:
    """Decorator that injects Gemini API parameters into the callback.
    
    Adds Google API key parameter and stores in ctx.obj['gemini'].
    
    Usage:
        @app.callback()
        @gemini_params
        def callback(ctx: typer.Context, ...):
            gemini = ctx.obj['gemini']
            # gemini = {'api_key': '...'}
    """
    from libs.python.cli.params_base import _create_param_decorator
    
    param_specs = [
        ('google_api_key', inspect.Parameter(
            'google_api_key', inspect.Parameter.KEYWORD_ONLY,
            annotation=GoogleApiKey
        )),
    ]
    
    def extractor(kwargs):
        return {
            'api_key': kwargs.pop('google_api_key'),
        }
    
    return _create_param_decorator(param_specs, 'gemini', extractor)(func)
