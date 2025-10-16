"""
Example FastAPI application demonstrating production deployment configuration.

This example shows how to use the api_deployment module to run a FastAPI
application with production-ready gunicorn configuration.
"""

from fastapi import FastAPI

app = FastAPI(
    title="Hello FastAPI",
    description="Simple FastAPI application with production deployment configuration",
    version="1.0.0",
)


@app.get("/")
def read_root():
    """Returns a simple hello world message"""
    return {"message": "hello world"}


@app.get("/health")
def health_check():
    """Health check endpoint for container orchestration"""
    return {"status": "healthy"}


if __name__ == "__main__":
    # Use the deployment CLI helper which supports both development and production modes
    # 
    # Development mode (default):
    #   python demo/hello_fastapi/main_with_deployment.py
    #   - Uses uvicorn for hot-reloading
    #   - Single worker process
    #   - Good for development
    #
    # Production mode:
    #   python demo/hello_fastapi/main_with_deployment.py --production
    #   - Uses gunicorn with uvicorn workers
    #   - Auto-scales workers based on CPU cores
    #   - Production-ready configuration
    #
    # Custom configuration:
    #   python demo/hello_fastapi/main_with_deployment.py --production --workers 4 --port 8080
    
    from libs.python.api_deployment.cli import run_from_cli
    
    run_from_cli(
        "demo.hello_fastapi.main_with_deployment:app",
        app_name="hello-fastapi",
        default_port=8000,
        description="Hello FastAPI application with deployment configuration",
    )
