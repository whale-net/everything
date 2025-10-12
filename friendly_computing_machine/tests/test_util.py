import datetime

from friendly_computing_machine.util import datetime_to_ts, ts_to_datetime


def test_ts_to_datetime():
    # Test round-trip conversion
    original_dt = datetime.datetime(2023, 10, 26, 10, 30, 15)
    ts = original_dt.timestamp()
    result_dt = ts_to_datetime(str(ts))
    assert result_dt == original_dt

    # Test with another timestamp
    ts_str_2 = "0.0"
    dt_epoch_local = datetime.datetime.fromtimestamp(0)
    assert ts_to_datetime(ts_str_2) == dt_epoch_local


def test_datetime_to_ts():
    # Test with a specific datetime
    dt = datetime.datetime(2023, 3, 15, 12, 0, 0)
    expected_ts_float = dt.timestamp()
    expected_ts_str = str(expected_ts_float)
    assert datetime_to_ts(dt) == expected_ts_str

    # Test with another datetime (e.g., epoch)
    # dt_epoch = datetime.datetime(1970, 1, 1, 0, 0, 0) # This is naive. Timestamp will be timezone dependent.
    # To make it robust, let's create it from a timestamp
    dt_from_ts_zero = datetime.datetime.fromtimestamp(0)
    expected_ts_str_epoch = str(dt_from_ts_zero.timestamp())
    assert datetime_to_ts(dt_from_ts_zero) == expected_ts_str_epoch


def test_datetime_ts_conversion_reversibility():
    original_dt = datetime.datetime(2024, 5, 29, 10, 0, 0)
    ts_str = datetime_to_ts(original_dt)
    reverted_dt = ts_to_datetime(ts_str)
    assert reverted_dt == original_dt

    # Test with microseconds
    original_dt_micros = datetime.datetime(2024, 5, 29, 10, 0, 0, 123456)
    ts_str_micros = datetime_to_ts(original_dt_micros)
    reverted_dt_micros = ts_to_datetime(ts_str_micros)
    # Due to float precision of timestamp, microseconds might be lost or slightly off
    # We should compare them with a tolerance or by comparing year, month, day, hour, minute, second
    assert reverted_dt_micros.year == original_dt_micros.year
    assert reverted_dt_micros.month == original_dt_micros.month
    assert reverted_dt_micros.day == original_dt_micros.day
    assert reverted_dt_micros.hour == original_dt_micros.hour
    assert reverted_dt_micros.minute == original_dt_micros.minute
    assert reverted_dt_micros.second == original_dt_micros.second
    # Microsecond precision can be an issue with float(str(float_ts)))
    # Allow a small delta for microsecond comparison if direct equality fails
    assert (
        abs(reverted_dt_micros.microsecond - original_dt_micros.microsecond) <= 1000
    )  # allow 1ms diff
