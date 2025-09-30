"""Internal API using FastAPI - only accessible within the cluster."""
from fastapi import FastAPI

app = FastAPI(
    title="Internal API",
    description="This API is only accessible within the cluster (no ingress)",
    version="1.0.0",
)

@app.get("/")
async def root():
    """Root endpoint."""
    return {
        "message": "Hello from internal API!",
        "type": "internal-api",
        "note": "This service is only accessible within the cluster"
    }

@app.get("/health")
async def health():
    """Health check endpoint."""
    return {"status": "healthy", "service": "internal-api"}

@app.get("/internal/data")
async def internal_data():
    """Internal data endpoint - only for cluster services."""
    return {
        "data": "sensitive internal data",
        "accessible": "cluster-only",
        "version": "1.0.0"
    }

def main():
    """Run the FastAPI application."""
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8000)

if __name__ == "__main__":
    main()
