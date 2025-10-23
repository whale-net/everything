"""Demo application showing consolidated logging usage.

This demonstrates the auto-detection features of libs.python.logging library.

Environment variables (auto-detected from container/Kubernetes):
- APP_NAME: Application name (e.g., "hello-logging")
- APP_DOMAIN: Domain/category (e.g., "demo")
- APP_TYPE: Application type (e.g., "worker")
- APP_VERSION: Version (e.g., "v1.2.3")
- APP_ENV: Environment (e.g., "dev", "staging", "prod")
- GIT_COMMIT: Git commit SHA
- POD_NAME, POD_NAMESPACE, NODE_NAME: Kubernetes context
- OTEL_EXPORTER_OTLP_ENDPOINT: OTLP collector endpoint

All these are automatically set by:
1. Container image (APP_NAME, APP_DOMAIN, APP_TYPE)
2. Helm charts (APP_VERSION, APP_ENV, GIT_COMMIT, POD_*, OTLP_*)
"""

import time
from libs.python.logging import configure_logging, get_logger, update_context


def main():
    """Main function demonstrating zero-config logging.
    
    Everything is auto-detected from environment variables!
    No need to hardcode service name, version, or environment.
    """
    
    # ZERO-CONFIG LOGGING: Everything auto-detected!
    # The library reads all metadata from environment variables:
    # - APP_NAME, APP_DOMAIN, APP_TYPE (from container image)
    # - APP_VERSION, GIT_COMMIT (from Helm chart)
    # - POD_NAME, NAMESPACE (from Kubernetes downward API)
    # - OTEL_EXPORTER_OTLP_ENDPOINT (from Helm values)
    configure_logging(
        # Only specify what you need to override:
        enable_console=True,  # Also show in console for local dev
        log_level="DEBUG",    # Override default INFO level
    )
    
    # That's it! All metadata auto-detected and sent to OTLP.
    
    # Get a logger for this module
    logger = get_logger(__name__)
    
    # Basic logging - all sent to OTLP with full context
    logger.debug("Debug message - detailed diagnostic info")
    logger.info("Info message - general information")
    logger.warning("Warning message - something unexpected")
    logger.error("Error message - something went wrong")
    
    # Logging with additional context - sent as OTLP log attributes
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
    """Simulate handling an HTTP request with context.
    
    All context updates are sent as OTLP log attributes following
    semantic conventions (http.request.method, http.route, etc.)
    """
    logger = get_logger(__name__)
    
    # Set request-specific context - sent as OTLP attributes
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
