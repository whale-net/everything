// Package logging provides structured logging, tracing, and metrics for Go
// applications in the everything monorepo. It mirrors the Python logging
// library's structured format and supports console (text) and JSON output,
// with optional OpenTelemetry OTLP export for logs, traces, and metrics.
//
// # Quick Start
//
//	logging.Configure(logging.Config{
//	    ServiceName:   "my-app",
//	    Domain:        "api",
//	    Environment:   "production",
//	    JSONFormat:    true,
//	    EnableOTLP:    true,   // logs
//	    EnableTracing: true,   // distributed tracing
//	    EnableMetrics: true,   // metrics collection
//	})
//	defer logging.Shutdown(context.Background())
//
//	// Logging
//	logger := logging.Get("mypackage")
//	logger.Info("handling request", "request_id", "abc-123")
//
//	// Tracing
//	tracer := logging.Tracer("mypackage")
//	ctx, span := tracer.Start(ctx, "handle-request")
//	defer span.End()
//
//	// Metrics
//	meter := logging.Meter("mypackage")
//	counter, _ := meter.Int64Counter("requests_total")
//	counter.Add(ctx, 1)
//
// # Environment Auto-Detection
//
// When fields are not set in Config, they are read from environment variables:
//
//   - APP_NAME -> ServiceName
//   - APP_DOMAIN -> Domain
//   - APP_TYPE -> AppType
//   - APP_VERSION -> Version
//   - APP_ENV / ENVIRONMENT -> Environment
//   - GIT_COMMIT / COMMIT_SHA -> CommitSHA
//   - POD_NAME / HOSTNAME -> PodName
//   - NAMESPACE / POD_NAMESPACE -> Namespace
//   - NODE_NAME -> NodeName
//   - OTEL_EXPORTER_OTLP_ENDPOINT -> OTLPEndpoint
package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
)

// Config controls logging behavior. Zero-value fields are auto-detected
// from environment variables (see package doc).
type Config struct {
	// Service identity
	ServiceName string
	Domain      string
	AppType     string
	Version     string
	Environment string
	CommitSHA   string

	// Kubernetes context
	PodName   string
	Namespace string
	NodeName  string

	// Output
	Level      slog.Level // default: slog.LevelInfo
	JSONFormat bool       // true = JSON lines, false = human-readable text
	Writer     io.Writer  // default: os.Stdout

	// OpenTelemetry
	EnableOTLP           bool
	EnableTracing        bool
	EnableMetrics        bool
	OTLPEndpoint         string        // default: localhost:4317
	MetricExportInterval time.Duration // default: 60s
}

var (
	mu             sync.Mutex
	configured     bool
	loggerProvider *sdklog.LoggerProvider
	globalConfig   Config
)

// Configure sets up the global slog default logger and, optionally, an OTLP
// exporter. Call once at application startup.
func Configure(cfg Config) {
	mu.Lock()
	defer mu.Unlock()

	applyDefaults(&cfg)
	globalConfig = cfg

	if cfg.Writer == nil {
		cfg.Writer = os.Stdout
	}

	// Build the base slog handler (console or JSON).
	var handler slog.Handler
	if cfg.JSONFormat {
		handler = newJSONHandler(cfg)
	} else {
		handler = newConsoleHandler(cfg)
	}

	slog.SetDefault(slog.New(handler))

	// Set up OTLP log export if requested.
	if cfg.EnableOTLP {
		if err := setupOTLP(cfg); err != nil {
			slog.Error("failed to initialize OTLP log exporter", "error", err)
		}
	}

	// Set up distributed tracing if requested.
	if cfg.EnableTracing {
		if err := setupTracing(cfg); err != nil {
			slog.Error("failed to initialize OTLP trace exporter", "error", err)
		}
	}

	// Set up metrics if requested.
	if cfg.EnableMetrics {
		if err := setupMetrics(cfg); err != nil {
			slog.Error("failed to initialize OTLP metric exporter", "error", err)
		}
	}

	configured = true

	slog.Info("logging configured",
		"service_name", cfg.ServiceName,
		"environment", cfg.Environment,
		"domain", cfg.Domain,
		"json_format", cfg.JSONFormat,
		"otlp_enabled", cfg.EnableOTLP,
		"tracing_enabled", cfg.EnableTracing,
		"metrics_enabled", cfg.EnableMetrics,
	)
}

// Shutdown flushes pending OTLP logs, traces, and metrics. Call before
// process exit.
func Shutdown(ctx context.Context) error {
	mu.Lock()
	defer mu.Unlock()

	var firstErr error
	capture := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if loggerProvider != nil {
		capture(loggerProvider.Shutdown(ctx))
		loggerProvider = nil
	}
	capture(shutdownTracing(ctx))
	capture(shutdownMetrics(ctx))

	return firstErr
}

// Get returns a *slog.Logger with the given name attached as an attribute.
// This mirrors the Python get_logger(__name__) pattern.
func Get(name string) *slog.Logger {
	return slog.Default().With("logger", name)
}

// --- internal ---

func applyDefaults(cfg *Config) {
	if cfg.ServiceName == "" {
		cfg.ServiceName = envOr("APP_NAME", "unknown-app")
	}
	if cfg.Domain == "" {
		cfg.Domain = os.Getenv("APP_DOMAIN")
	}
	if cfg.AppType == "" {
		cfg.AppType = os.Getenv("APP_TYPE")
	}
	if cfg.Version == "" {
		cfg.Version = envOr("APP_VERSION", "latest")
	}
	if cfg.Environment == "" {
		cfg.Environment = envOr("APP_ENV", os.Getenv("ENVIRONMENT"))
		if cfg.Environment == "" {
			cfg.Environment = "development"
		}
	}
	if cfg.CommitSHA == "" {
		cfg.CommitSHA = envOr("GIT_COMMIT", os.Getenv("COMMIT_SHA"))
	}
	if cfg.PodName == "" {
		cfg.PodName = envOr("POD_NAME", os.Getenv("HOSTNAME"))
	}
	if cfg.Namespace == "" {
		cfg.Namespace = envOr("NAMESPACE", os.Getenv("POD_NAMESPACE"))
	}
	if cfg.NodeName == "" {
		cfg.NodeName = os.Getenv("NODE_NAME")
	}
	if cfg.OTLPEndpoint == "" {
		cfg.OTLPEndpoint = envOr("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// setupOTLP creates an OTLP gRPC log exporter and registers it as the
// global OTel LoggerProvider.
func setupOTLP(cfg Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exporter, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlploggrpc.WithInsecure(),
	)
	if err != nil {
		return fmt.Errorf("create OTLP log exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", cfg.Version),
			attribute.String("deployment.environment", cfg.Environment),
		),
	)
	if err != nil {
		return fmt.Errorf("create OTLP resource: %w", err)
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(res),
	)

	global.SetLoggerProvider(provider)
	loggerProvider = provider
	return nil
}

// emitOTEL sends a log record to the OTel LoggerProvider. Called by the
// slog handlers when OTLP is enabled. The context carries trace correlation.
func emitOTEL(ctx context.Context, r slog.Record, cfg Config) {
	provider := global.GetLoggerProvider()
	if provider == nil {
		return
	}

	logger := provider.Logger(cfg.ServiceName)

	var rec otellog.Record
	rec.SetTimestamp(r.Time)
	rec.SetBody(otellog.StringValue(r.Message))
	rec.SetSeverity(slogToOTELSeverity(r.Level))
	rec.SetSeverityText(r.Level.String())

	// Collect attributes from the slog record.
	var attrs []otellog.KeyValue
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, otellog.String(a.Key, a.Value.String()))
		return true
	})
	rec.AddAttributes(attrs...)

	// Emit with context so the OTLP exporter can correlate with active span.
	logger.Emit(ctx, rec)
}

func slogToOTELSeverity(l slog.Level) otellog.Severity {
	switch {
	case l >= slog.LevelError:
		return otellog.SeverityError
	case l >= slog.LevelWarn:
		return otellog.SeverityWarn
	case l >= slog.LevelInfo:
		return otellog.SeverityInfo
	default:
		return otellog.SeverityDebug
	}
}

// --- JSON handler ---
// Produces output matching the Python StructuredFormatter:
//
//	{"timestamp":"...","severity":"INFO","message":"...","app_name":"...","environment":"...", ...}

type jsonHandler struct {
	cfg    Config
	w      io.Writer
	level  slog.Level
	attrs  []slog.Attr
	groups []string
	mu     *sync.Mutex
}

func newJSONHandler(cfg Config) *jsonHandler {
	return &jsonHandler{
		cfg:   cfg,
		w:     cfg.Writer,
		level: cfg.Level,
		mu:    &sync.Mutex{},
	}
}

func (h *jsonHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *jsonHandler) Handle(ctx context.Context, r slog.Record) error {
	m := make(map[string]any, 16)

	// Base fields matching Python StructuredFormatter
	m["timestamp"] = r.Time.Format(time.RFC3339Nano)
	m["severity"] = r.Level.String()
	m["message"] = r.Message

	// Source location
	if r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := fs.Next()
		m["source"] = map[string]any{
			"function": f.Function,
			"file":     f.File,
			"line":     f.Line,
		}
	}

	// Trace context — inject trace_id/span_id when a span is active
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		sc := span.SpanContext()
		m["trace_id"] = sc.TraceID().String()
		m["span_id"] = sc.SpanID().String()
		m["trace_flags"] = sc.TraceFlags().String()
	}

	// Service context (matches Python LogContext fields)
	m["app_name"] = h.cfg.ServiceName
	m["environment"] = h.cfg.Environment
	if h.cfg.Domain != "" {
		m["domain"] = h.cfg.Domain
	}
	if h.cfg.AppType != "" {
		m["app_type"] = h.cfg.AppType
	}
	if h.cfg.Version != "" {
		m["version"] = h.cfg.Version
	}
	if h.cfg.CommitSHA != "" {
		m["commit_sha"] = h.cfg.CommitSHA
	}
	if h.cfg.PodName != "" {
		m["pod_name"] = h.cfg.PodName
	}
	if h.cfg.Namespace != "" {
		m["namespace"] = h.cfg.Namespace
	}
	if h.cfg.NodeName != "" {
		m["node_name"] = h.cfg.NodeName
	}

	// Pre-attached attrs (from With())
	for _, a := range h.attrs {
		m[a.Key] = a.Value.String()
	}

	// Per-record attrs
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = resolveAttrValue(a.Value)
		return true
	})

	data, err := json.Marshal(m)
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err = fmt.Fprintf(h.w, "%s\n", data)

	// Also emit to OTLP if configured
	if globalConfig.EnableOTLP {
		emitOTEL(ctx, r, h.cfg)
	}

	return err
}

func (h *jsonHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &jsonHandler{
		cfg:   h.cfg,
		w:     h.w,
		level: h.level,
		attrs: append(cloneAttrs(h.attrs), attrs...),
		mu:    h.mu,
	}
}

func (h *jsonHandler) WithGroup(name string) slog.Handler {
	return &jsonHandler{
		cfg:    h.cfg,
		w:      h.w,
		level:  h.level,
		attrs:  cloneAttrs(h.attrs),
		groups: append(append([]string{}, h.groups...), name),
		mu:     h.mu,
	}
}

// --- Console handler ---
// Human-readable output for local development:
//
//	2024-01-15T10:30:00Z - [my-app] INFO - mypackage - handling request request_id=abc-123

type consoleHandler struct {
	cfg   Config
	w     io.Writer
	level slog.Level
	attrs []slog.Attr
	mu    *sync.Mutex
}

func newConsoleHandler(cfg Config) *consoleHandler {
	return &consoleHandler{
		cfg:   cfg,
		w:     cfg.Writer,
		level: cfg.Level,
		mu:    &sync.Mutex{},
	}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *consoleHandler) Handle(ctx context.Context, r slog.Record) error {
	ts := r.Time.Format(time.RFC3339)
	level := r.Level.String()

	// Collect all attrs into key=value pairs
	var kvPairs string
	for _, a := range h.attrs {
		kvPairs += " " + a.Key + "=" + a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		kvPairs += " " + a.Key + "=" + a.Value.String()
		return true
	})

	// Append trace context if a span is active
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		sc := span.SpanContext()
		kvPairs += " trace_id=" + sc.TraceID().String()
		kvPairs += " span_id=" + sc.SpanID().String()
	}

	line := fmt.Sprintf("%s - [%s] %s - %s%s\n",
		ts, h.cfg.ServiceName, level, r.Message, kvPairs)

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, line)

	if globalConfig.EnableOTLP {
		emitOTEL(ctx, r, h.cfg)
	}

	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &consoleHandler{
		cfg:   h.cfg,
		w:     h.w,
		level: h.level,
		attrs: append(cloneAttrs(h.attrs), attrs...),
		mu:    h.mu,
	}
}

func (h *consoleHandler) WithGroup(_ string) slog.Handler {
	// Groups not meaningful for console output; just return self.
	return h
}

// --- helpers ---

func cloneAttrs(attrs []slog.Attr) []slog.Attr {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]slog.Attr, len(attrs))
	copy(out, attrs)
	return out
}

func resolveAttrValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindGroup:
		m := make(map[string]any)
		for _, a := range v.Group() {
			m[a.Key] = resolveAttrValue(a.Value)
		}
		return m
	case slog.KindInt64:
		return v.Int64()
	case slog.KindUint64:
		return v.Uint64()
	case slog.KindFloat64:
		return v.Float64()
	case slog.KindBool:
		return v.Bool()
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	default:
		return v.String()
	}
}
