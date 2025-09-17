"""
Tests for the FastAPI application
"""

import pytest
from fastapi.testclient import TestClient

from demo.hello_fastapi.main import app

client = TestClient(app)


def test_read_root():
    """Test the root endpoint returns hello world"""
    response = client.get("/")
    assert response.status_code == 200
    assert response.json() == {"message": "hello world"}


def test_response_content_type():
    """Test that the response has the correct content type"""
    response = client.get("/")
    assert response.headers["content-type"] == "application/json"