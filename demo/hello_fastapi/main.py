"""
Simple FastAPI application that returns "hello world" on GET /
"""

from fastapi import FastAPI

app = FastAPI()


@app.get("/")
def read_root():
    """Returns a simple hello world message"""
    return {"message": "hello world"}


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8000)