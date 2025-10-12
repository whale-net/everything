import logging
import os

import typer
from opentelemetry._logs import set_logger_provider
from opentelemetry.exporter.otlp.proto.grpc._log_exporter import OTLPLogExporter
from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor, ConsoleLogExporter
from opentelemetry.sdk.resources import Resource

logger = logging.getLogger(__name__)
FILENAME = os.path.basename(__file__)


def setup_logging(
    ctx: typer.Context,
    log_debug: bool = False,
    log_otlp: bool = True,
    log_console: bool = False,
):
    """
    Setup logging for the CLI application.
    """
    # OTEL logging setup
    logger_provider = LoggerProvider(
        resource=Resource.create(
            {
                "service.name": "friendly-computing-machine",
                "service.instance.id": os.uname().nodename,
            }
        ),
    )
    set_logger_provider(logger_provider)

    if log_otlp:
        otlp_exporter = OTLPLogExporter(
            endpoint=os.getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")
            or "http://0.0.0.0:4317",
            insecure=True,
        )
        logger_provider.add_log_record_processor(BatchLogRecordProcessor(otlp_exporter))

    if log_console:
        console_exporter = ConsoleLogExporter()
        logger_provider.add_log_record_processor(
            BatchLogRecordProcessor(console_exporter)
        )

    handler = LoggingHandler(level=logging.NOTSET, logger_provider=logger_provider)

    logging.basicConfig(level=logging.DEBUG if log_debug else logging.INFO)
    logging.getLogger().addHandler(handler)
    # keep these just because and for skae of example
    ctx.obj[FILENAME] = {
        "log_otlp": log_otlp,
        "log_console": log_console,
    }
    logger.debug("logging setup complete")
