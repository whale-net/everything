package logging

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	obscontext "github.com/whale-net/everything/libs/go/observability/context"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// otlpHandler is a slog.Handler that exports logs via OTLP
type otlpHandler struct {
	logger   log.Logger
	obsCtx   *obscontext.ObservabilityContext
	attrs    []slog.Attr
	group    string
}

func newOTLPHandler(provider *sdklog.LoggerProvider, obsCtx *obscontext.ObservabilityContext) *otlpHandler {
	logger := provider.Logger(obsCtx.AppName)
	return &otlpHandler{
		logger: logger,
		obsCtx: obsCtx,
	}
}

func (h *otlpHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *otlpHandler) Handle(ctx context.Context, r slog.Record) error {
	// Get observability context from Go context
	obsCtx := obscontext.FromContext(ctx)
	if obsCtx == nil {
		obsCtx = h.obsCtx
	}
	
	// Create log record
	var logRecord log.Record
	logRecord.SetTimestamp(r.Time)
	logRecord.SetBody(log.StringValue(r.Message))
	logRecord.SetSeverity(slogLevelToOTLP(r.Level))
	logRecord.SetSeverityText(r.Level.String())
	
	// Add trace context if available
	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		spanCtx := span.SpanContext()
		logRecord.SetTraceID(spanCtx.TraceID())
		logRecord.SetSpanID(spanCtx.SpanID())
		logRecord.SetTraceFlags(spanCtx.TraceFlags())
	}
	
	// Collect attributes
	attrs := make([]log.KeyValue, 0, r.NumAttrs()+20)
	
	// Add record attributes
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, slogAttrToOTLP(a))
		return true
	})
	
	// Add handler attributes (from WithAttrs)
	for _, a := range h.attrs {
		attrs = append(attrs, slogAttrToOTLP(a))
	}
	
	// Add context attributes
	attrs = appendContextAttrs(attrs, obsCtx)
	
	logRecord.SetAttributes(attrs...)
	
	// Emit the log record
	h.logger.Emit(ctx, logRecord)
	
	return nil
}

func (h *otlpHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newAttrs := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(newAttrs, h.attrs)
	copy(newAttrs[len(h.attrs):], attrs)
	
	return &otlpHandler{
		logger: h.logger,
		obsCtx: h.obsCtx,
		attrs:  newAttrs,
		group:  h.group,
	}
}

func (h *otlpHandler) WithGroup(name string) slog.Handler {
	return &otlpHandler{
		logger: h.logger,
		obsCtx: h.obsCtx,
		attrs:  h.attrs,
		group:  name,
	}
}

// appendContextAttrs adds observability context as OTLP attributes
func appendContextAttrs(attrs []log.KeyValue, obsCtx *obscontext.ObservabilityContext) []log.KeyValue {
	if obsCtx == nil {
		return attrs
	}
	
	// Request identification
	if obsCtx.RequestID != "" {
		attrs = append(attrs, log.String("request.id", obsCtx.RequestID))
	}
	if obsCtx.CorrelationID != "" {
		attrs = append(attrs, log.String("correlation.id", obsCtx.CorrelationID))
	}
	if obsCtx.UserID != "" {
		attrs = append(attrs, log.String("enduser.id", obsCtx.UserID))
	}
	if obsCtx.SessionID != "" {
		attrs = append(attrs, log.String("session.id", obsCtx.SessionID))
	}
	
	// Multi-tenancy
	if obsCtx.TenantID != "" {
		attrs = append(attrs, log.String("tenant.id", obsCtx.TenantID))
	}
	if obsCtx.OrgID != "" {
		attrs = append(attrs, log.String("organization.id", obsCtx.OrgID))
	}
	
	// HTTP context (semantic conventions)
	if obsCtx.HTTPMethod != "" {
		attrs = append(attrs, log.String("http.request.method", obsCtx.HTTPMethod))
	}
	if obsCtx.HTTPPath != "" {
		attrs = append(attrs, log.String("http.route", obsCtx.HTTPPath))
		attrs = append(attrs, log.String("url.path", obsCtx.HTTPPath))
	}
	if obsCtx.HTTPStatusCode > 0 {
		attrs = append(attrs, log.Int("http.response.status_code", obsCtx.HTTPStatusCode))
	}
	if obsCtx.ClientIP != "" {
		attrs = append(attrs, log.String("client.address", obsCtx.ClientIP))
	}
	if obsCtx.UserAgent != "" {
		attrs = append(attrs, log.String("user_agent.original", obsCtx.UserAgent))
	}
	
	// Worker/Job context
	if obsCtx.WorkerID != "" {
		attrs = append(attrs, log.String("worker.id", obsCtx.WorkerID))
	}
	if obsCtx.TaskID != "" {
		attrs = append(attrs, log.String("task.id", obsCtx.TaskID))
	}
	if obsCtx.JobID != "" {
		attrs = append(attrs, log.String("job.id", obsCtx.JobID))
	}
	
	// Operation metadata
	if obsCtx.Operation != "" {
		attrs = append(attrs, log.String("operation.name", obsCtx.Operation))
	}
	if obsCtx.ResourceID != "" {
		attrs = append(attrs, log.String("resource.id", obsCtx.ResourceID))
	}
	if obsCtx.EventType != "" {
		attrs = append(attrs, log.String("event.type", obsCtx.EventType))
	}
	
	// Custom attributes
	for k, v := range obsCtx.Custom {
		// Prefix custom attributes to avoid conflicts
		attrs = append(attrs, log.String("app."+k, toString(v)))
	}
	
	return attrs
}

// slogLevelToOTLP converts slog.Level to OTLP severity
func slogLevelToOTLP(level slog.Level) log.Severity {
	switch {
	case level >= slog.LevelError:
		return log.SeverityError
	case level >= slog.LevelWarn:
		return log.SeverityWarn
	case level >= slog.LevelInfo:
		return log.SeverityInfo
	case level >= slog.LevelDebug:
		return log.SeverityDebug
	default:
		return log.SeverityTrace
	}
}

// slogAttrToOTLP converts slog.Attr to OTLP KeyValue
func slogAttrToOTLP(a slog.Attr) log.KeyValue {
	switch a.Value.Kind() {
	case slog.KindString:
		return log.String(a.Key, a.Value.String())
	case slog.KindInt64:
		return log.Int64(a.Key, a.Value.Int64())
	case slog.KindUint64:
		return log.Int64(a.Key, int64(a.Value.Uint64()))
	case slog.KindFloat64:
		return log.Float64(a.Key, a.Value.Float64())
	case slog.KindBool:
		return log.Bool(a.Key, a.Value.Bool())
	case slog.KindDuration:
		return log.Int64(a.Key, a.Value.Duration().Milliseconds())
	case slog.KindTime:
		return log.Int64(a.Key, a.Value.Time().Unix())
	default:
		return log.String(a.Key, a.Value.String())
	}
}

// toString converts any value to string
func toString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	case int32:
		return fmt.Sprintf("%d", val)
	case int16:
		return fmt.Sprintf("%d", val)
	case int8:
		return fmt.Sprintf("%d", val)
	case uint:
		return fmt.Sprintf("%d", val)
	case uint64:
		return fmt.Sprintf("%d", val)
	case uint32:
		return fmt.Sprintf("%d", val)
	case uint16:
		return fmt.Sprintf("%d", val)
	case uint8:
		return fmt.Sprintf("%d", val)
	case float64:
		return fmt.Sprintf("%f", val)
	case float32:
		return fmt.Sprintf("%f", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case time.Time:
		return val.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", v)
	}
}
