"""Simple test to verify pytest is working."""


def test_simple():
    """Simple test that should always pass."""
    assert True


def test_imports():
    """Test that our status processor modules can be imported."""
    import sys
    from pathlib import Path

    # Add src to path
    project_root = Path(__file__).parent.parent
    sys.path.insert(0, str(project_root / "src"))

    # Test imports
    from manman.src.models import ExternalStatusInfo, StatusType

    # Basic functionality test
    status_info = ExternalStatusInfo.create(
        "TestClass", StatusType.CREATED, worker_id=-1
    )
    assert status_info.class_name == "TestClass"
    assert status_info.status_type == StatusType.CREATED
