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

    from manman.src.host.api.shared import add_health_check

    app = FastAPI(title="ManMan Status API", lifespan=lifespan)
    app.include_router(router)
    add_health_check(app)
    return app
