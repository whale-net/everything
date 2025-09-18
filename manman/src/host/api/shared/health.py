"""Shared health check functionality for all API services."""

from fastapi import APIRouter, FastAPI


def add_health_check(app: FastAPI, prefix: str = "") -> None:
    """
    Add a health check endpoint to a FastAPI app.

    Args:
        app: The FastAPI application instance
        prefix: Prefix for the health endpoint (e.g., "/experience", "/status")
    """
    health_router = APIRouter(prefix=prefix)

    @health_router.get("/health")
    async def health() -> str:
        return "OK"

    app.include_router(health_router)
