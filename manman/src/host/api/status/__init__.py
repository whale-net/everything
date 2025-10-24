from contextlib import asynccontextmanager

from .api import router

__all__ = ["router", "create_app"]


@asynccontextmanager
async def lifespan(app):
    """Lifespan context manager for FastAPI application."""
    # Startup
    yield
    # Shutdown - cleanup RabbitMQ connections
    from libs.python.rmq import cleanup_rabbitmq_connections

    cleanup_rabbitmq_connections()


def create_app():
    """Factory function to create the Status API FastAPI application."""
    from fastapi import FastAPI
    from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
    from libs.python.logging import configure_metrics
    import os

    from manman.src.host.api.shared import add_health_check

    app = FastAPI(
        title="ManMan Status API",
    )
    app.include_router(router)
    add_health_check(app)
    
    # Setup metrics (if OTLP enabled)
    if os.getenv('LOG_OTLP', '').lower() in ('true', '1', 'yes'):
        configure_metrics()
    
    # Automatically instrument FastAPI with OpenTelemetry
    # This creates spans for all endpoints and captures request/response details
    # Also automatically creates metrics when meter provider is configured
    FastAPIInstrumentor.instrument_app(app)
    
    return app
