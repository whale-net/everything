"""Demo application showing consolidated logging usage.

This demonstrates all the features of the libs.python.logging library.
"""

import time
from libs.python.logging import configure_logging, get_logger, update_context


def main():
    """Main function demonstrating logging features."""
    
    # Configure logging once at startup
    configure_logging(
        app_name="hello-logging",
        domain="demo",
        app_type="worker",
        environment="development",
        version="v1.0.0",
        log_level="DEBUG",
        enable_otlp=False,  # Set to True to enable OTLP export
        json_format=True,   # Set to False for colored console output
        # Additional context
        commit_sha="abc123def456",
        platform="linux/arm64",
    )
    
    # Get a logger for this module
    logger = get_logger(__name__)
    
    # Basic logging
    logger.debug("Debug message - detailed diagnostic info")
    logger.info("Info message - general information")
    logger.warning("Warning message - something unexpected")
    logger.error("Error message - something went wrong")
    
    # Logging with additional context
    logger.info(
        "Processing user request",
        extra={
            "request_id": "req-123-456",
            "user_id": "user-789",
            "operation": "create_order",
        }
    )
    
    # Simulate a request handler with context updates
    simulate_request_handler()
    
    # Simulate error handling
    simulate_error_handling()
    
    # Demonstrate different app types
    demonstrate_worker_logging()


def simulate_request_handler():
    """Simulate handling an HTTP request with context."""
    logger = get_logger(__name__)
    
    # Set request-specific context
    update_context(
        request_id="req-abc-123",
        correlation_id="corr-xyz-789",
        http_method="POST",
        http_path="/api/orders",
        client_ip="192.168.1.100",
        user_id="user-42",
    )
    
    logger.info("Received HTTP request")
    
    # Process request
    time.sleep(0.1)  # Simulate work
    
    logger.info("Validating request payload")
    
    time.sleep(0.1)
    
    # Set response context
    update_context(http_status_code=201)
    
    logger.info("Request completed successfully")


def simulate_error_handling():
    """Simulate error handling with exception logging."""
    logger = get_logger(__name__)
    
    try:
        # Simulate an error
        result = 1 / 0
    except ZeroDivisionError as e:
        logger.exception(
            "Error processing calculation",
            extra={
                "error_code": "MATH_ERROR",
                "operation": "division",
                "resource_id": "calc-123",
            }
        )


def demonstrate_worker_logging():
    """Demonstrate worker/background task logging."""
    logger = get_logger(__name__)
    
    # Set worker context
    update_context(
        worker_id="worker-5",
        task_id="task-abc-123",
        operation="process_batch",
    )
    
    logger.info("Starting batch processing")
    
    # Process items
    for i in range(3):
        update_context(
            resource_id=f"item-{i}",
            custom={"batch_index": i, "total_items": 3}
        )
        logger.debug(f"Processing item {i}")
        time.sleep(0.05)
    
    logger.info("Batch processing completed")


if __name__ == "__main__":
    main()
