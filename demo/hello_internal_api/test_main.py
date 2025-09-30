"""Tests for hello_internal_api."""
from fastapi.testclient import TestClient
from demo.hello_internal_api.main import app

client = TestClient(app)

def test_root():
    """Test root endpoint."""
    response = client.get("/")
    assert response.status_code == 200
    data = response.json()
    assert data["message"] == "Hello from internal API!"
    assert data["type"] == "internal-api"

def test_health():
    """Test health endpoint."""
    response = client.get("/health")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "healthy"
    assert data["service"] == "internal-api"

def test_internal_data():
    """Test internal data endpoint."""
    response = client.get("/internal/data")
    assert response.status_code == 200
    data = response.json()
    assert data["accessible"] == "cluster-only"
    assert "data" in data
