"""
App types and resource configurations for Helm charts.
"""

from dataclasses import dataclass
from enum import Enum
from typing import List, Optional


class AppType(str, Enum):
    """Application deployment types."""
    
    EXTERNAL_API = "external-api"
    INTERNAL_API = "internal-api"
    WORKER = "worker"
    JOB = "job"
    
    def requires_deployment(self) -> bool:
        """Returns True if this app type uses a Deployment."""
        return self in (AppType.EXTERNAL_API, AppType.INTERNAL_API, AppType.WORKER)
    
    def requires_service(self) -> bool:
        """Returns True if this app type needs a Service."""
        return self in (AppType.EXTERNAL_API, AppType.INTERNAL_API)
    
    def requires_ingress(self) -> bool:
        """Returns True if this app type should have an Ingress."""
        return self == AppType.EXTERNAL_API
    
    def requires_job(self) -> bool:
        """Returns True if this app type is a Job."""
        return self == AppType.JOB
    
    def requires_pdb(self) -> bool:
        """Returns True if this app type should have a PodDisruptionBudget."""
        return self in (AppType.EXTERNAL_API, AppType.INTERNAL_API, AppType.WORKER)
    
    def template_artifacts(self) -> List[str]:
        """Returns the list of template files needed for this app type."""
        artifacts = []
        
        if self.requires_deployment():
            artifacts.append("deployment.yaml")
        
        if self.requires_service():
            artifacts.append("service.yaml")
        
        if self.requires_ingress():
            artifacts.append("ingress.yaml")
        
        if self.requires_job():
            artifacts.append("job.yaml")
        
        if self.requires_pdb():
            artifacts.append("pdb.yaml")
        
        return artifacts
    
    def default_resource_config(self) -> "ResourceConfig":
        """Returns sensible defaults based on app type."""
        if self in (AppType.EXTERNAL_API, AppType.INTERNAL_API):
            return ResourceConfig(
                requests_cpu="50m",
                requests_memory="256Mi",
                limits_cpu="100m",
                limits_memory="512Mi"
            )
        elif self == AppType.WORKER:
            return ResourceConfig(
                requests_cpu="50m",
                requests_memory="256Mi",
                limits_cpu="100m",
                limits_memory="512Mi"
            )
        elif self == AppType.JOB:
            return ResourceConfig(
                requests_cpu="100m",
                requests_memory="256Mi",
                limits_cpu="200m",
                limits_memory="512Mi"
            )
        else:
            return ResourceConfig(
                requests_cpu="50m",
                requests_memory="128Mi",
                limits_cpu="100m",
                limits_memory="256Mi"
            )


@dataclass
class ResourceConfig:
    """Resource requests and limits configuration."""
    
    requests_cpu: str
    requests_memory: str
    limits_cpu: str
    limits_memory: str
    
    def to_values_format(self) -> dict:
        """Convert to values.yaml format."""
        return {
            "requests": {
                "cpu": self.requests_cpu,
                "memory": self.requests_memory
            },
            "limits": {
                "cpu": self.limits_cpu,
                "memory": self.limits_memory
            }
        }


@dataclass
class HealthCheckConfig:
    """Health check configuration."""
    
    path: str
    port: Optional[int] = None
    initial_delay_seconds: int = 10
    period_seconds: int = 10
    timeout_seconds: int = 5
    success_threshold: int = 1
    failure_threshold: int = 3


@dataclass
class IngressConfig:
    """Ingress configuration."""
    
    host: str = ""
    tls_secret_name: str = ""


@dataclass
class AppMetadata:
    """Application metadata from release_app."""
    
    name: str
    app_type: str
    version: str = "latest"
    description: str = ""
    registry: str = ""
    repo_name: str = ""
    image_target: str = ""
    domain: str = ""
    language: str = ""
    port: Optional[int] = None
    replicas: Optional[int] = None
    labels: Optional[dict] = None
    annotations: Optional[dict] = None
    dependencies: Optional[List[str]] = None
    health_check: Optional[dict] = None
    ingress: Optional[dict] = None
    command: Optional[List[str]] = None
    args: Optional[List[str]] = None
    
    def get_image(self) -> str:
        """Returns the full image name (registry/repo_name)."""
        if self.registry and self.repo_name:
            return f"{self.registry}/{self.repo_name}"
        return self.repo_name or ""
    
    def get_image_tag(self) -> str:
        """Returns the version tag."""
        return self.version if self.version else "latest"


@dataclass
class AppConfig:
    """Configuration for a single app in values.yaml."""
    
    type: str
    image: str
    image_tag: str
    port: Optional[int] = None
    replicas: int = 1
    resources: Optional[dict] = None
    health_check: Optional[HealthCheckConfig] = None
    command: Optional[List[str]] = None
    args: Optional[List[str]] = None
    env: Optional[dict] = None
    ingress: Optional[IngressConfig] = None


@dataclass
class ManifestFile:
    """Manual Kubernetes manifest file."""
    
    path: str
    content: bytes
    filename: str


def resolve_app_type(app_name: str, app_type_str: str) -> AppType:
    """Validates and returns the app type.
    
    Args:
        app_name: Name of the application
        app_type_str: App type string to validate
        
    Returns:
        AppType enum value
        
    Raises:
        ValueError: If app_type_str is empty or invalid
    """
    if not app_type_str:
        raise ValueError(
            f"app type is required for {app_name} "
            f"(must be one of: external-api, internal-api, worker, job)"
        )
    
    try:
        return AppType(app_type_str)
    except ValueError:
        raise ValueError(
            f"invalid app type: {app_type_str} "
            f"(must be one of: external-api, internal-api, worker, job)"
        )
