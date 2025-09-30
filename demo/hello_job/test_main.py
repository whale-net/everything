"""Tests for hello_job."""

def test_hello_job():
    """Test that the job works."""
    from demo.hello_job.main import main
    result = main()
    assert result == 0
