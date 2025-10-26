"""Custom OpenTelemetry logging handler with context injection.

Extends the standard OTEL LoggingHandler to inject request/operation context
as log record attributes following OTEL semantic conventions.
"""

import logging
from typing import Optional

from libs.python.logging.context import get_context

try:
    from opentelemetry import trace
    from opentelemetry.sdk._logs import LoggerProvider, LoggingHandler
    from opentelemetry.sdk._logs import LogRecord as OTELLogRecord
    
    OTEL_AVAILABLE = True
    
    class OTELContextHandler(LoggingHandler):
        """OTEL logging handler that injects context as log record attributes.
        
        This extends the standard OTEL LoggingHandler to automatically add:
        - Request/operation context from LogContext
        - Trace/span correlation from active span
        - HTTP semantic conventions
        - Custom attributes
        
        All attributes follow OTEL semantic conventions where applicable.
        """
        
        def emit(self, record: logging.LogRecord) -> None:
            """Emit a log record with context attributes.
            
            Args:
                record: Python logging record
            """
            # Get current context
            context = get_context()
            
            # Add context attributes to the record's extra dict
            # These will be picked up by the OTEL SDK and sent as log attributes
            if context:
                attributes = {}
                
                # Request identification attributes
                if context.request_id:
                    attributes["request.id"] = context.request_id
                if context.correlation_id:
                    attributes["correlation.id"] = context.correlation_id
                if context.user_id:
                    attributes["enduser.id"] = context.user_id  # OTEL semantic convention
                if context.session_id:
                    attributes["session.id"] = context.session_id
                
                # Multi-tenancy attributes
                if context.tenant_id:
                    attributes["tenant.id"] = context.tenant_id
                if context.organization_id:
                    attributes["organization.id"] = context.organization_id
                
                # HTTP semantic conventions
                # https://opentelemetry.io/docs/specs/semconv/http/http-spans/
                if context.http_method:
                    attributes["http.request.method"] = context.http_method
                if context.http_path:
                    attributes["http.route"] = context.http_path
                    attributes["url.path"] = context.http_path
                if context.http_status_code:
                    attributes["http.response.status_code"] = context.http_status_code
                if context.client_ip:
                    attributes["client.address"] = context.client_ip
                if context.user_agent:
                    attributes["user_agent.original"] = context.user_agent
                
                # Worker/job attributes (custom)
                if context.worker_id:
                    attributes["worker.id"] = context.worker_id
                if context.task_id:
                    attributes["task.id"] = context.task_id
                if context.job_id:
                    attributes["job.id"] = context.job_id
                
                # Operation metadata
                if context.operation:
                    attributes["operation.name"] = context.operation
                if context.resource_id:
                    attributes["resource.id"] = context.resource_id
                if context.event_type:
                    attributes["event.type"] = context.event_type
                
                # Process attributes (if not already in resource)
                if context.process_id:
                    attributes["process.pid"] = context.process_id
                if context.thread_id:
                    attributes["thread.id"] = context.thread_id
                
                # Add custom attributes
                if context.custom:
                    for key, value in context.custom.items():
                        # Prefix custom attributes to avoid conflicts
                        attributes[f"app.{key}"] = value
                
                # Add attributes to the record
                # The OTEL SDK will pick these up from the LogRecord
                if not hasattr(record, 'otel_attributes'):
                    record.otel_attributes = {}
                record.otel_attributes.update(attributes)
            
            # Get trace context from active span
            span = trace.get_current_span()
            if span.is_recording():
                span_context = span.get_span_context()
                if not hasattr(record, 'otel_attributes'):
                    record.otel_attributes = {}
                record.otel_attributes["trace_id"] = format(span_context.trace_id, "032x")
                record.otel_attributes["span_id"] = format(span_context.span_id, "016x")
                record.otel_attributes["trace_flags"] = span_context.trace_flags
            
            # Call parent emit to send to OTLP
            super().emit(record)
        
        def _translate(self, record: logging.LogRecord) -> OTELLogRecord:
            """Translate Python LogRecord to OTEL LogRecord with attributes.
            
            Args:
                record: Python logging record
                
            Returns:
                OTEL LogRecord with attributes
            """
            # Call parent translation
            otel_record = super()._translate(record)
            
            # Add our custom attributes
            if hasattr(record, 'otel_attributes'):
                if otel_record.attributes is None:
                    otel_record.attributes = {}
                otel_record.attributes.update(record.otel_attributes)
            
            # Add any extra attributes from logging call
            if hasattr(record, '__dict__'):
                standard_fields = {
                    'name', 'msg', 'args', 'created', 'filename', 'funcName', 'levelname',
                    'levelno', 'lineno', 'module', 'msecs', 'message', 'pathname', 'process',
                    'processName', 'relativeCreated', 'thread', 'threadName', 'exc_info',
                    'exc_text', 'stack_info', 'getMessage', 'taskName', 'otel_attributes',
                }
                
                if otel_record.attributes is None:
                    otel_record.attributes = {}
                
                for key, value in record.__dict__.items():
                    if key not in standard_fields and not key.startswith('_'):
                        # Add extra fields from logging call
                        otel_record.attributes[key] = value
            
            return otel_record
            
except ImportError:
    OTEL_AVAILABLE = False
    OTELContextHandler = None  # Not available when OTEL is not installed
