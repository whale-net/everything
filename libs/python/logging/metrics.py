"""OpenTelemetry metrics configuration.

Provides centralized metrics setup for applications with OTLP export.
"""

import logging
import os
from typing import Optional

logger = logging.getLogger(__name__)

try:
    from opentelemetry.sdk.metrics import MeterProvider
    from opentelemetry.sdk.resources import Resource
    from opentelemetry.exporter.otlp.proto.grpc.metric_exporter import OTLPMetricExporter
    from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
    from opentelemetry.metrics import set_meter_provider
    
    OTEL_METRICS_AVAILABLE = True
except ImportError:
    OTEL_METRICS_AVAILABLE = False


# Global state
_metrics_configured = False


def configure_metrics(
    service_name: Optional[str] = None,
    service_version: Optional[str] = None,
    deployment_environment: Optional[str] = None,
    domain: Optional[str] = None,
    otlp_endpoint: Optional[str] = None,
    export_interval_millis: int = 60000,
    force_reconfigure: bool = False,
) -> bool:
    """Configure OpenTelemetry metrics with OTLP export.
    
    This sets up automatic metrics collection and export to an OTLP collector.
    All parameters are optional and will be auto-detected from environment variables.
    
    Environment variables used:
    - APP_NAME: Service name (e.g., "experience-api")
    - APP_VERSION: Service version (e.g., "v1.2.3")
    - APP_DOMAIN: Service domain/namespace (e.g., "manman")
    - APP_ENV / ENVIRONMENT: Environment (dev, staging, prod)
    - OTEL_EXPORTER_OTLP_METRICS_ENDPOINT: Metrics-specific endpoint
    - OTEL_EXPORTER_OTLP_ENDPOINT: General OTLP endpoint
    
    Args:
        service_name: Service name (auto-detected from APP_NAME if not provided)
        service_version: Service version (auto-detected from APP_VERSION if not provided)
        deployment_environment: Environment (auto-detected from APP_ENV if not provided)
        domain: Service namespace (auto-detected from APP_DOMAIN if not provided)
        otlp_endpoint: OTLP collector endpoint (defaults to env or http://localhost:4317)
        export_interval_millis: Metrics export interval in milliseconds (default: 60000)
        force_reconfigure: Force reconfiguration even if already configured
        
    Returns:
        True if metrics were configured, False if already configured or OTEL not available
        
    Example:
        >>> from libs.python.logging import configure_metrics
        >>> 
        >>> # Minimal usage - everything auto-detected
        >>> configure_metrics()
        >>> 
        >>> # Override specific values
        >>> configure_metrics(
        ...     service_name="my-api",
        ...     export_interval_millis=30000,  # Export every 30 seconds
        ... )
    """
    global _metrics_configured
    
    # Check if already configured
    if _metrics_configured and not force_reconfigure:
        logger.debug("Metrics already configured, skipping")
        return False
    
    # Check if OTEL is available
    if not OTEL_METRICS_AVAILABLE:
        logger.warning(
            "OpenTelemetry metrics requested but dependencies not available. "
            "Install with: pip install opentelemetry-api opentelemetry-sdk opentelemetry-exporter-otlp"
        )
        return False
    
    # Auto-detect from environment
    service_name = service_name or os.getenv('APP_NAME', 'unknown-service')
    service_version = service_version or os.getenv('APP_VERSION', 'unknown')
    deployment_environment = deployment_environment or os.getenv('APP_ENV') or os.getenv('ENVIRONMENT', 'development')
    domain = domain or os.getenv('APP_DOMAIN', 'default')
    
    # Build resource attributes
    resource_attrs = {
        "service.name": service_name,
        "service.namespace": domain,
        "service.version": service_version,
        "deployment.environment": deployment_environment,
    }
    
    # Add additional metadata if available
    if commit_sha := os.getenv('GIT_COMMIT') or os.getenv('COMMIT_SHA'):
        resource_attrs["service.instance.id"] = commit_sha[:8]
        resource_attrs["vcs.commit.id"] = commit_sha
    
    if app_type := os.getenv('APP_TYPE'):
        resource_attrs["service.type"] = app_type
    
    # Kubernetes attributes
    if pod_name := os.getenv('POD_NAME'):
        resource_attrs["k8s.pod.name"] = pod_name
    if namespace := os.getenv('NAMESPACE'):
        resource_attrs["k8s.namespace.name"] = namespace
    if node_name := os.getenv('NODE_NAME'):
        resource_attrs["k8s.node.name"] = node_name
    
    resource = Resource.create(resource_attrs)
    
    # Configure OTLP endpoint
    endpoint = (
        otlp_endpoint
        or os.getenv('OTEL_EXPORTER_OTLP_METRICS_ENDPOINT')
        or os.getenv('OTEL_EXPORTER_OTLP_ENDPOINT')
        or 'http://localhost:4317'
    )
    
    # Create metric reader with OTLP exporter
    metric_reader = PeriodicExportingMetricReader(
        OTLPMetricExporter(endpoint=endpoint, insecure=True),
        export_interval_millis=export_interval_millis,
    )
    
    # Create and set meter provider
    meter_provider = MeterProvider(
        resource=resource,
        metric_readers=[metric_reader]
    )
    set_meter_provider(meter_provider)
    
    _metrics_configured = True
    
    logger.info(
        f"Metrics configured for {service_name}",
        extra={
            "environment": deployment_environment,
            "domain": domain,
            "version": service_version,
            "otlp_endpoint": endpoint,
            "export_interval_ms": export_interval_millis,
        }
    )
    
    return True


def is_metrics_configured() -> bool:
    """Check if metrics have been configured.
    
    Returns:
        True if configure_metrics has been called successfully
    """
    return _metrics_configured
