"""Simple app that uses the generated OpenAPI client."""
from generated.demo.internal_api.api.default_api import DefaultApi
from generated.demo.internal_api.configuration import Configuration

def main():
    """
    Demonstrate using the generated OpenAPI client.
    This provides build-time integration testing of the OpenAPI generation pipeline.
    """
    # Create configuration
    config = Configuration(
        host="http://hello-internal-api:8000"
    )
    
    # Create API client instance
    api_client = DefaultApi()
    
    print("OpenAPI client integration test")
    print(f"Client configured for: {config.host}")
    print(f"API client type: {type(api_client).__name__}")
    print("✅ OpenAPI client dependency loaded successfully!")
    
    # We don't actually call the API since it won't be running
    # This test just verifies the generated client can be imported and instantiated
    return 0

if __name__ == "__main__":
    exit(main())
