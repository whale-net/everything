"""
ManMan Host Main Entry Point

This module provides the main entry points for the ManMan host services including:
- Experience API (user-facing game server management)
- Status API (monitoring and status endpoints) 
- Worker DAL API (data access layer for workers)
- Status Processor (background pub/sub message processing)
"""

import logging
import typer
from typing import Optional

app = typer.Typer()
logger = logging.getLogger(__name__)


@app.command()
def start_experience_api(
    port: int = 8000,
    workers: int = 1,
):
    """Start the experience API (host layer) that provides game server management."""
    logger.info("Starting Experience API on port %d with %d workers", port, workers)
    # Implementation would go here
    pass


@app.command()  
def start_status_api(
    port: int = 8000,
    workers: int = 1,
):
    """Start the status API that provides monitoring endpoints."""
    logger.info("Starting Status API on port %d with %d workers", port, workers)
    # Implementation would go here
    pass


@app.command()
def start_worker_dal_api(
    port: int = 8000,
    workers: int = 1,
):
    """Start the worker DAL API that provides data access for workers."""
    logger.info("Starting Worker DAL API on port %d with %d workers", port, workers)
    # Implementation would go here  
    pass


@app.command()
def start_status_processor():
    """Start the status event processor for background message handling."""
    logger.info("Starting Status Processor")
    # Implementation would go here
    pass


if __name__ == "__main__":
    app()