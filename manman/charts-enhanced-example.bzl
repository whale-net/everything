# Example: Using Enhanced Chart Framework with ManMan

load("//tools:enhanced_helm_chart_release.bzl", "enhanced_helm_chart_release_macro")

# Enhanced helm chart generation with optional components
# This replaces the manual charts in manman/charts/manman-host/
enhanced_helm_chart_release_macro(
    domain = "manman",
    charts = {
        # Production chart with full features
        "host_chart": {
            "description": "ManMan host services with full production features",
            "apps": [
                "experience_api", 
                "status_api", 
                "worker_dal_api", 
                "status_processor"
            ],
            "features": {
                "pdb": "true",      # Enable Pod Disruption Budgets for high availability
                "hpa": "true",      # Enable autoscaling for APIs
                "ingress": "true"   # Enable ingress for external access
            },
            "custom_values": {
                "global.env.APP_ENV": "production",
                "ingress.host": "api.manman.com",
                "ingress.tls.enabled": "true"
            }
        },
        
        # Development chart with minimal features
        "dev_chart": {
            "description": "ManMan development services - minimal configuration",
            "apps": [
                "experience_api",
                "status_api"
            ],
            "features": {
                "ingress": "true"   # Only ingress for development testing
            },
            "custom_values": {
                "global.env.APP_ENV": "dev",
                "ingress.host": "localhost"
            }
        },
        
        # API-only chart for specific deployments
        "api_only_chart": {
            "description": "ManMan API services only - no background processors",
            "apps": [
                "experience_api",
                "status_api", 
                "worker_dal_api"
            ],
            "features": {
                "pdb": "true",
                "hpa": "true",
                "ingress": "true"
            }
        },
        
        # Processor-only chart for worker nodes
        "processor_chart": {
            "description": "ManMan background processors - no exposed APIs",
            "apps": [
                "status_processor"
            ],
            "features": {
                "pdb": "true",  # Still want availability for processors
                "hpa": "true"   # Scale processors based on load
            }
        }
    }
)