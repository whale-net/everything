"""Tests for FastAPI utilities."""

import datetime
import json

import pytest
from fastapi import FastAPI
from fastapi.testclient import TestClient

from libs.python.fastapi_utils import RFC3339JSONResponse, configure_fastapi_datetime_serialization


def test_rfc3339_json_response_with_naive_datetime():
    """Test that naive datetimes are serialized with UTC timezone."""
    app = FastAPI(default_response_class=RFC3339JSONResponse)

    @app.get("/test")
    def get_datetime():
        # Naive datetime (no timezone)
        return {"timestamp": datetime.datetime(2025, 10, 25, 14, 30, 0)}

    client = TestClient(app)
    response = client.get("/test")
    
    assert response.status_code == 200
    data = response.json()
    
    # Should have 'Z' suffix for UTC
    assert data["timestamp"].endswith("Z") or "+00:00" in data["timestamp"]
    assert "2025-10-25T14:30:00" in data["timestamp"]


def test_rfc3339_json_response_with_aware_datetime():
    """Test that timezone-aware datetimes are serialized correctly."""
    app = FastAPI(default_response_class=RFC3339JSONResponse)

    @app.get("/test")
    def get_datetime():
        # Timezone-aware datetime
        dt = datetime.datetime(2025, 10, 25, 14, 30, 0, tzinfo=datetime.timezone.utc)
        return {"timestamp": dt}

    client = TestClient(app)
    response = client.get("/test")
    
    assert response.status_code == 200
    data = response.json()
    
    # Should have timezone info
    assert "2025-10-25T14:30:00" in data["timestamp"]
    assert data["timestamp"].endswith("Z") or "+00:00" in data["timestamp"]


def test_configure_fastapi_datetime_serialization():
    """Test the configuration helper function."""
    app = FastAPI()
    configure_fastapi_datetime_serialization(app)
    
    assert app.default_response_class == RFC3339JSONResponse

    @app.get("/test")
    def get_datetime():
        return {"timestamp": datetime.datetime(2025, 10, 25, 14, 30, 0)}

    client = TestClient(app)
    response = client.get("/test")
    
    assert response.status_code == 200
    data = response.json()
    assert "2025-10-25T14:30:00" in data["timestamp"]
    assert data["timestamp"].endswith("Z") or "+00:00" in data["timestamp"]


def test_multiple_datetimes_in_response():
    """Test that multiple datetime fields are all serialized correctly."""
    app = FastAPI(default_response_class=RFC3339JSONResponse)

    @app.get("/test")
    def get_datetimes():
        return {
            "created": datetime.datetime(2025, 10, 25, 10, 0, 0),
            "updated": datetime.datetime(2025, 10, 25, 14, 30, 0),
            "nested": {
                "timestamp": datetime.datetime(2025, 10, 25, 18, 45, 0),
            },
        }

    client = TestClient(app)
    response = client.get("/test")
    
    assert response.status_code == 200
    data = response.json()
    
    # All timestamps should have timezone info
    for key in ["created", "updated"]:
        assert "2025-10-25T" in data[key]
        assert data[key].endswith("Z") or "+00:00" in data[key]
    
    assert "2025-10-25T" in data["nested"]["timestamp"]
    assert data["nested"]["timestamp"].endswith("Z") or "+00:00" in data["nested"]["timestamp"]
