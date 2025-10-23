"""Custom log formatters for structured logging.

Provides formatters that include context attributes and support JSON output.
"""

import json
import logging
import traceback
from typing import Optional, Dict, Any

from libs.python.logging.context import LogContext, get_context

try:
    from opentelemetry import trace
    
    OTEL_AVAILABLE = True
except ImportError:
    OTEL_AVAILABLE = False


class StructuredFormatter(logging.Formatter):
    """JSON formatter that includes context attributes.
    
    Automatically includes:
    - All LogContext attributes
    - OpenTelemetry trace/span IDs
    - Source location (module, function, line)
    - Timestamp and log level
    """
    
    def __init__(
        self,
        global_context: Optional[LogContext] = None,
        include_trace: bool = True,
        include_source: bool = True,
    ):
        """Initialize structured formatter.
        
        Args:
            global_context: Global context set at startup
            include_trace: Include OpenTelemetry trace/span IDs
            include_source: Include source location (module, function, line)
        """
        super().__init__()
        self.global_context = global_context
        self.include_trace = include_trace and OTEL_AVAILABLE
        self.include_source = include_source
    
    def format(self, record: logging.LogRecord) -> str:
        """Format log record as JSON with context.
        
        Args:
            record: Log record to format
            
        Returns:
            JSON string with all context and log data
        """
        # Start with base log data
        log_data: Dict[str, Any] = {
            "timestamp": self.formatTime(record, self.datefmt),
            "severity": record.levelname,
            "severity_number": record.levelno,
            "message": record.getMessage(),
        }
        
        # Add source location
        if self.include_source:
            log_data["source"] = {
                "module": record.module,
                "function": record.funcName,
                "line": record.lineno,
                "file": record.pathname,
            }
        
        # Add global context (set at startup)
        if self.global_context:
            log_data.update(self.global_context.to_dict())
        
        # Add request/operation context (set per-request)
        current_context = get_context()
        if current_context and current_context != self.global_context:
            # Only add fields that differ from global context
            log_data.update(current_context.to_dict())
        
        # Add OpenTelemetry trace context
        if self.include_trace:
            span = trace.get_current_span()
            if span.is_recording():
                span_context = span.get_span_context()
                log_data["trace_id"] = format(span_context.trace_id, "032x")
                log_data["span_id"] = format(span_context.span_id, "016x")
                log_data["trace_flags"] = format(span_context.trace_flags, "02x")
        
        # Add exception info if present
        if record.exc_info:
            log_data["exception"] = {
                "type": record.exc_info[0].__name__ if record.exc_info[0] else None,
                "message": str(record.exc_info[1]) if record.exc_info[1] else None,
                "stacktrace": self.formatException(record.exc_info),
            }
        
        # Add any extra fields from log call
        # Filter out standard fields and internal fields
        standard_fields = {
            "name", "msg", "args", "created", "filename", "funcName", "levelname",
            "levelno", "lineno", "module", "msecs", "message", "pathname", "process",
            "processName", "relativeCreated", "thread", "threadName", "exc_info",
            "exc_text", "stack_info", "getMessage", "taskName",
        }
        
        for key, value in record.__dict__.items():
            if key not in standard_fields and not key.startswith("_"):
                log_data[key] = value
        
        return json.dumps(log_data, default=str)


class ColoredConsoleFormatter(logging.Formatter):
    """Colored text formatter for development console output.
    
    Provides color-coded log levels and includes context in a readable format.
    """
    
    # ANSI color codes
    COLORS = {
        "DEBUG": "\033[36m",      # Cyan
        "INFO": "\033[32m",       # Green
        "WARNING": "\033[33m",    # Yellow
        "ERROR": "\033[31m",      # Red
        "CRITICAL": "\033[35m",   # Magenta
    }
    RESET = "\033[0m"
    BOLD = "\033[1m"
    
    def __init__(
        self,
        global_context: Optional[LogContext] = None,
        use_colors: bool = True,
    ):
        """Initialize colored console formatter.
        
        Args:
            global_context: Global context set at startup
            use_colors: Enable color output (auto-detected from terminal)
        """
        super().__init__()
        self.global_context = global_context
        self.use_colors = use_colors
    
    def format(self, record: logging.LogRecord) -> str:
        """Format log record with colors and context.
        
        Args:
            record: Log record to format
            
        Returns:
            Formatted string with colors and context
        """
        # Build base message
        timestamp = self.formatTime(record, self.datefmt)
        
        # Color the level
        level = record.levelname
        if self.use_colors and level in self.COLORS:
            level = f"{self.COLORS[level]}{level}{self.RESET}"
        
        # Build context string
        context_parts = []
        if self.global_context:
            if self.global_context.app_name:
                context_parts.append(self.global_context.app_name)
            if self.global_context.environment:
                context_parts.append(self.global_context.environment)
        
        current_context = get_context()
        if current_context:
            if current_context.request_id:
                context_parts.append(f"req={current_context.request_id[:8]}")
            if current_context.trace_id:
                context_parts.append(f"trace={current_context.trace_id[:8]}")
        
        context_str = f"[{' | '.join(context_parts)}] " if context_parts else ""
        
        # Build message
        message = record.getMessage()
        
        # Add source location
        source = f"{record.module}.{record.funcName}:{record.lineno}"
        
        # Format: timestamp - [context] level - source - message
        formatted = f"{timestamp} - {context_str}{level} - {source} - {message}"
        
        # Add exception if present
        if record.exc_info:
            formatted += "\n" + self.formatException(record.exc_info)
        
        return formatted
