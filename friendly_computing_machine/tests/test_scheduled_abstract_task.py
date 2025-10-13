from datetime import datetime, timedelta

import pytest

from friendly_computing_machine.src.friendly_computing_machine.bot.task.abstracttask import (
    ScheduledAbstractTask,
)


def test_no_run():
    result = ScheduledAbstractTask._get_last_expected_run_datetime(
        start_date=datetime.max, period=timedelta(minutes=5), now=datetime.now()
    )
    assert result == datetime.min


def test_recent_run():
    now = datetime.now()
    result = ScheduledAbstractTask._get_last_expected_run_datetime(
        start_date=now - timedelta(minutes=6), period=timedelta(minutes=5), now=now
    )
    assert result == now - timedelta(minutes=1)


@pytest.mark.parametrize(
    "offset, expected",
    [
        (-1, datetime(2024, 11, 24)),
        (0, datetime(2024, 11, 25)),
        (1, datetime(2024, 11, 25)),
        (1000, datetime(2024, 11, 25)),
        (timedelta(days=1).total_seconds(), datetime(2024, 11, 26)),
    ],
)
def test_multiple_runs(offset: int, expected: datetime):
    now = datetime(2024, 11, 25) + timedelta(seconds=offset)
    result = ScheduledAbstractTask._get_last_expected_run_datetime(
        start_date=datetime(2024, 11, 20), period=timedelta(days=1), now=now
    )
    assert result == expected
