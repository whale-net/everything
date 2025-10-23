"""Context management for structured logging.

Provides thread-local and contextvars-based context storage for log attributes
that should be automatically included in all log records.
"""

import contextvars
import os
from dataclasses import dataclass, field, asdict
from typing import Optional, Dict, Any

# Thread-safe context storage
_log_context: contextvars.ContextVar[Optional["LogContext"]] = contextvars.ContextVar(
    "log_context", default=None
)


@dataclass
class LogContext:
    """Standard attributes for structured logging.
    
    These attributes are automatically included in all log records when set.
    Apps can set these once at startup or per-request for automatic inclusion.
    """
    
    # Application metadata (set at startup)
    environment: Optional[str] = None  # dev, staging, prod
    domain: Optional[str] = None  # api, web, worker, etc.
    app_name: Optional[str] = None  # hello-fastapi, manman-worker
    app_type: Optional[str] = None  # external-api, internal-api, worker, job
    version: Optional[str] = None  # v1.2.3 or commit SHA
    commit_sha: Optional[str] = None  # Full git commit SHA
    
    # Kubernetes context (auto-detected)
    pod_name: Optional[str] = None
    container_name: Optional[str] = None
    node_name: Optional[str] = None
    namespace: Optional[str] = None
    
    # Helm context
    chart_name: Optional[str] = None
    release_name: Optional[str] = None
    
    # Request/operation context (set per-request)
    request_id: Optional[str] = None
    correlation_id: Optional[str] = None
    user_id: Optional[str] = None
    session_id: Optional[str] = None
    tenant_id: Optional[str] = None
    organization_id: Optional[str] = None
    
    # HTTP context (for API apps)
    http_method: Optional[str] = None
    http_path: Optional[str] = None
    http_status_code: Optional[int] = None
    client_ip: Optional[str] = None
    user_agent: Optional[str] = None
    
    # Worker/job context
    worker_id: Optional[str] = None
    task_id: Optional[str] = None
    job_id: Optional[str] = None
    
    # Operation metadata
    operation: Optional[str] = None
    resource_id: Optional[str] = None
    event_type: Optional[str] = None
    
    # OpenTelemetry context (auto-populated)
    trace_id: Optional[str] = None
    span_id: Optional[str] = None
    trace_flags: Optional[str] = None
    
    # Process context
    process_id: Optional[int] = None
    thread_id: Optional[int] = None
    hostname: Optional[str] = None
    
    # Platform context
    platform: Optional[str] = None  # linux/amd64, linux/arm64
    architecture: Optional[str] = None  # amd64, arm64
    
    # Build context (Bazel)
    bazel_target: Optional[str] = None
    
    # Custom attributes
    custom: Dict[str, Any] = field(default_factory=dict)
    
    def to_dict(self) -> Dict[str, Any]:
        """Convert to dictionary, excluding None values and empty custom dict."""
        result = {}
        data = asdict(self)
        custom = data.pop("custom", {})
        
        # Add non-None standard attributes
        for key, value in data.items():
            if value is not None:
                result[key] = value
        
        # Add custom attributes
        result.update(custom)
        
        return result
    
    @classmethod
    def from_environment(cls) -> "LogContext":
        """Create LogContext from environment variables.
        
        Automatically detects Kubernetes context and other environment-based attributes.
        """
        return cls(
            # App metadata from env
            environment=os.getenv("APP_ENV") or os.getenv("ENVIRONMENT"),
            app_name=os.getenv("APP_NAME"),
            domain=os.getenv("APP_DOMAIN"),
            app_type=os.getenv("APP_TYPE"),
            version=os.getenv("APP_VERSION"),
            commit_sha=os.getenv("GIT_COMMIT") or os.getenv("COMMIT_SHA"),
            
            # Kubernetes context (standard k8s downward API env vars)
            pod_name=os.getenv("POD_NAME") or os.getenv("HOSTNAME"),
            container_name=os.getenv("CONTAINER_NAME"),
            node_name=os.getenv("NODE_NAME"),
            namespace=os.getenv("NAMESPACE") or os.getenv("POD_NAMESPACE"),
            
            # Helm context
            chart_name=os.getenv("HELM_CHART_NAME"),
            release_name=os.getenv("HELM_RELEASE_NAME"),
            
            # Process context
            hostname=os.getenv("HOSTNAME"),
            platform=os.getenv("PLATFORM"),
            architecture=os.getenv("ARCHITECTURE"),
            
            # Bazel context
            bazel_target=os.getenv("BAZEL_TARGET"),
        )


def set_context(context: LogContext) -> None:
    """Set the current log context.
    
    Args:
        context: LogContext to set as current
    """
    _log_context.set(context)


def get_context() -> Optional[LogContext]:
    """Get the current log context.
    
    Returns:
        Current LogContext or None if not set
    """
    return _log_context.get()


def clear_context() -> None:
    """Clear the current log context."""
    _log_context.set(None)


def update_context(**kwargs) -> None:
    """Update the current context with new values.
    
    Args:
        **kwargs: Attributes to update in the current context
    """
    current = get_context()
    if current is None:
        current = LogContext()
        set_context(current)
    
    # Update standard fields
    for key, value in kwargs.items():
        if key == "custom":
            # Merge custom attributes
            current.custom.update(value)
        elif hasattr(current, key):
            setattr(current, key, value)
        else:
            # Unknown fields go to custom
            current.custom[key] = value
