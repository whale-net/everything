from contextlib import asynccontextmanager

from .api import router

__all__ = ["router", "create_app"]


@asynccontextmanager
async def lifespan(app):
    """Lifespan context manager for FastAPI application."""
    # Startup
    yield
    # Shutdown - cleanup RabbitMQ connections
    from manman.util import cleanup_rabbitmq_connections

    cleanup_rabbitmq_connections()


def create_app():
    """Factory function to create the Status API FastAPI application."""
    from fastapi import FastAPI

    from manman.host.api.shared import add_health_check

    app = FastAPI(title="ManMan Status API", root_path="/status", lifespan=lifespan)
    app.include_router(router)
    add_health_check(app)
    return app
