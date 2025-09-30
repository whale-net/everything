"""Tests for hello_worker."""

def test_hello_worker():
    """Test that the worker works."""
    from demo.hello_worker.main import main
    # Just ensure it doesn't crash
    main()
