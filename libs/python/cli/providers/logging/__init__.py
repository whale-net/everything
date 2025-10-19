"""Logging provider with OpenTelemetry support."""

from libs.python.cli.providers.logging.logging import (
    EnableConsoleExporter,
    EnableOTLP,
    LogLevel,
    LoggingContext,
    create_logging_context,
    logging_params,
)


import inspect
import logging
import os
from dataclasses import dataclass
from typing import Annotated, Callable, Literal, Optional

import typer
from opentelemetry._logs import set_logger_provider
from opentelemetry.exporter.otlp.proto.grpc._log_exporter import OTLPLogExporter
from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor, ConsoleLogExporter
from opentelemetry.sdk.resources import Resource

logger = logging.getLogger(__name__)


# Type aliases for CLI parameters
LogLevel = Annotated[
    Literal["DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"],
    typer.Option(help="Logging level"),
]
EnableOTLP = Annotated[bool, typer.Option("--log-otlp", help="Enable OTLP logging")]
EnableConsoleExporter = Annotated[
    bool, typer.Option("--log-console", help="Enable console OTLP exporter")
]


@dataclass
class LoggingContext:
    """Typed logging context.
    
    Attributes:
        service_name: Service name for OTLP resource
        log_level: Configured logging level
        enable_otlp: Whether OTLP export is enabled
        enable_console: Whether console export is enabled
        logger_provider: OpenTelemetry logger provider
    """

    service_name: str
    log_level: str
    enable_otlp: bool
    enable_console: bool
    logger_provider: LoggerProvider


def create_logging_context(
    service_name: str,
    log_level: str = "INFO",
    enable_otlp: bool = False,
    enable_console: bool = False,
    otlp_endpoint: Optional[str] = None,
    instance_id: Optional[str] = None,
) -> LoggingContext:
    """Create logging context with OpenTelemetry support.
    
    Args:
        service_name: Service name for OTLP resource
        log_level: Logging level (DEBUG, INFO, WARNING, ERROR, CRITICAL)
        enable_otlp: Enable OTLP log export
        enable_console: Enable console OTLP export (for debugging)
        otlp_endpoint: OTLP endpoint URL (defaults to env or http://0.0.0.0:4317)
        instance_id: Instance identifier (defaults to hostname)
    
    Returns:
        LoggingContext with configured logging
        
    Example:
        >>> ctx = create_logging_context(
        ...     service_name="my-app",
        ...     log_level="DEBUG",
        ...     enable_otlp=True,
        ... )
    """
    logger.debug("Creating logging context for service: %s", service_name)

    # Set up OTLP logger provider
    logger_provider = LoggerProvider(
        resource=Resource.create(
            {
                "service.name": service_name,
                "service.instance.id": instance_id or os.uname().nodename,
            }
        ),
    )
    set_logger_provider(logger_provider)

    # Add OTLP exporter if enabled
    if enable_otlp:
        endpoint = (
            otlp_endpoint
            or os.getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
            or "http://0.0.0.0:4317"
        )
        logger.debug("Enabling OTLP log export to: %s", endpoint)
        otlp_exporter = OTLPLogExporter(endpoint=endpoint, insecure=True)
        logger_provider.add_log_record_processor(
            BatchLogRecordProcessor(otlp_exporter)
        )

    # Add console exporter if enabled (useful for debugging)
    if enable_console:
        logger.debug("Enabling console OTLP exporter")
        console_exporter = ConsoleLogExporter()
        logger_provider.add_log_record_processor(
            BatchLogRecordProcessor(console_exporter)
        )

    # Configure root logger
    log_level_int = getattr(logging, log_level.upper())
    handler = LoggingHandler(level=logging.NOTSET, logger_provider=logger_provider)
    logging.basicConfig(level=log_level_int)
    logging.getLogger().addHandler(handler)

    logger.debug("Logging context created successfully")

    return LoggingContext(
        service_name=service_name,
        log_level=log_level,
        enable_otlp=enable_otlp,
        enable_console=enable_console,
        logger_provider=logger_provider,
    )


# ==============================================================================
# Decorator for injecting logging parameters
# ==============================================================================

def logging_params(func: Callable) -> Callable:
    """
    Decorator that injects logging parameters into the callback.
    
    Usage:
        @app.callback()
        @logging_params
        def callback(ctx: typer.Context, ...):
            log_config = ctx.obj['logging']
            # log_config = {'enable_otlp': True/False}
    """
    from libs.python.cli.params_base import _create_param_decorator
    
    param_specs = [
        ('log_otlp', inspect.Parameter(
            'log_otlp', inspect.Parameter.KEYWORD_ONLY,
            default=False, annotation=EnableOTLP
        )),
    ]
    
    def extractor(kwargs):
        return {
            'enable_otlp': kwargs.pop('log_otlp', False),
        }
    
    return _create_param_decorator(param_specs, 'logging', extractor)(func)
