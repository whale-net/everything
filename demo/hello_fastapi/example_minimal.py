"""
Minimal example of using api_deployment configuration.

This demonstrates the simplest way to add production deployment
configuration to a FastAPI application.
"""

from fastapi import FastAPI
from libs.python.api_deployment import run_with_gunicorn

app = FastAPI()


@app.get("/")
def read_root():
    return {"message": "hello from minimal example"}


if __name__ == "__main__":
    # Run with production-ready gunicorn configuration
    run_with_gunicorn(
        "demo.hello_fastapi.example_minimal:app",
        app_name="minimal-example",
        port=8000,
    )
