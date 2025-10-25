"""FastAPI utilities for consistent API behavior."""

import datetime
import json
from typing import Any

from fastapi.responses import JSONResponse


class RFC3339JSONResponse(JSONResponse):
    """Custom JSON response that serializes datetimes in RFC3339 format with timezone.
    
    This ensures compatibility with OpenAPI clients (especially Go clients) that expect
    RFC3339-compliant datetime strings with timezone information.
    
    Naive datetimes (without timezone) are assumed to be UTC and serialized with 'Z' suffix.
    Timezone-aware datetimes are serialized with their timezone offset.
    
    Example:
        >>> from fastapi import FastAPI
        >>> app = FastAPI(default_response_class=RFC3339JSONResponse)
    """

    def render(self, content: Any) -> bytes:
        """Render content as JSON with RFC3339 datetime formatting."""

        def datetime_handler(obj):
            if isinstance(obj, datetime.datetime):
                # Ensure timezone-aware datetime, assume UTC if naive
                if obj.tzinfo is None:
                    obj = obj.replace(tzinfo=datetime.timezone.utc)
                return obj.isoformat()
            raise TypeError(f"Object of type {type(obj)} is not JSON serializable")

        return json.dumps(
            content,
            ensure_ascii=False,
            allow_nan=False,
            indent=None,
            separators=(",", ":"),
            default=datetime_handler,
        ).encode("utf-8")


def configure_fastapi_datetime_serialization(app):
    """Configure a FastAPI app to use RFC3339 datetime serialization.
    
    This is a convenience function that sets the default response class to RFC3339JSONResponse.
    
    Args:
        app: FastAPI application instance
        
    Example:
        >>> from fastapi import FastAPI
        >>> from libs.python.fastapi_utils import configure_fastapi_datetime_serialization
        >>> 
        >>> app = FastAPI()
        >>> configure_fastapi_datetime_serialization(app)
    """
    app.default_response_class = RFC3339JSONResponse
