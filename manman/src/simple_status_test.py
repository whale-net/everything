"""Simple test to verify pytest is working."""


def test_simple():
    """Simple test that should always pass."""
    assert True


def test_imports():
    """Test that our status processor modules can be imported."""
    # Test imports
    from manman.src.models import ExternalStatusInfo, StatusType

    # Basic functionality test
    status_info = ExternalStatusInfo.create(
        "TestClass", StatusType.CREATED, worker_id=-1
    )
    assert status_info.class_name == "TestClass"
    assert status_info.status_type == StatusType.CREATED
