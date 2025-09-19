import datetime

import pytest

from manman.src.models import ExternalStatusInfo, StatusType


def test_field_validator_create():
    """Test the status_type field validator."""

    status = ExternalStatusInfo.create("TestClass", StatusType.RUNNING, worker_id=1)
    assert status.status_type == StatusType.RUNNING


def test_field_validator_init():
    status = ExternalStatusInfo(
        class_name="TestClass",
        status_type=StatusType.CREATED,
        worker_id=1,
        as_of=datetime.datetime.now(datetime.timezone.utc),
    )
    assert status.status_type == StatusType.CREATED


def test_field_validator_create_string():
    with pytest.raises(ValueError):
        ExternalStatusInfo.create(
            class_name="TestClass",
            status_type="CREATED",
            worker_id=1,
        )


def test_field_validator_string():
    status = ExternalStatusInfo(
        class_name="TestClass",
        status_type="CREATED",
        worker_id=1,
    )
    assert status.status_type == StatusType.CREATED


def test_field_validator_create_invalid_string():
    with pytest.raises(ValueError):
        ExternalStatusInfo(
            class_name="TestClass",
            status_type="INVALID_STATUS",  # Invalid string
            worker_id=1,
        )


def test_field_validator_create_invalid_id():
    """Test the status_type field validator."""

    with pytest.raises(ValueError):
        ExternalStatusInfo.create(
            "TestClass", StatusType.RUNNING, worker_id=1, game_server_instance_id=1
        )
