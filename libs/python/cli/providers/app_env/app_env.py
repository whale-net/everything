"""App environment provider.

Provides application environment parameter via decorator pattern.
"""

import inspect
from typing import Callable

from libs.python.cli.types import AppEnv


def app_env_params(func: Callable) -> Callable:
    """
    Decorator that injects app_env parameter into the callback.
    
    Usage:
        @app.callback()
        @app_env_params
        def callback(ctx: typer.Context):
            app_env = ctx.obj.get('app_env')
            # app_env = 'dev' | 'staging' | 'prod' | None
    """
    from libs.python.cli.params_base import _create_param_decorator
    
    param_specs = [
        ('app_env', inspect.Parameter(
            'app_env', inspect.Parameter.KEYWORD_ONLY,
            default=None, annotation=AppEnv
        )),
    ]
    
    def extractor(kwargs):
        return kwargs.pop('app_env', None)
    
    return _create_param_decorator(param_specs, 'app_env', extractor)(func)
