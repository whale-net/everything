# Wrapper for platform-agnostic requirements using rules_pycross
# This provides a simple interface for BUILD files to reference Python dependencies
# pycross automatically handles platform-specific resolution

def requirement(name):
    """Get a requirement from the unified @pypi repository.
    
    pycross handles platform selection automatically based on the target platform.
    No need for separate requirement_amd64/requirement_arm64 functions anymore.
    
    Args:
        name: Package name (e.g., "fastapi", "uvicorn")
    
    Returns:
        Label for the package in the @pypi repository
    """
    return "@pypi//" + name.replace("-", "_")