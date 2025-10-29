// Package logging provides structured logging with console and OTLP export.
// It follows OpenTelemetry semantic conventions and auto-detects configuration
// from environment variables.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	obscontext "github.com/whale-net/everything/libs/go/observability/context"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// Config holds logging configuration
type Config struct {
	// Service identification (auto-detected from environment if not provided)
	ServiceName    string
	ServiceVersion string
	Environment    string
	
	// Log level
	Level slog.Level
	
	// Console output
	EnableConsole bool
	ConsoleWriter io.Writer
	JSONFormat    bool // If false, uses text format for development
	
	// OTLP export
	EnableOTLP   bool
	OTLPEndpoint string
}

// DefaultConfig returns a Config with defaults and auto-detection from environment
func DefaultConfig() *Config {
	obsCtx := obscontext.FromEnvironment()
	
	otlpEndpoint := getEnv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
		getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"))
	
	return &Config{
		ServiceName:    obsCtx.AppName,
		ServiceVersion: obsCtx.Version,
		Environment:    obsCtx.Environment,
		Level:          slog.LevelInfo,
		EnableConsole:  true,
		ConsoleWriter:  os.Stdout,
		JSONFormat:     false, // Simple text for local development
		EnableOTLP:     true,  // OTLP-first
		OTLPEndpoint:   otlpEndpoint,
	}
}

// Logger wraps slog.Logger with context-aware methods
type Logger struct {
	*slog.Logger
	obsCtx *obscontext.ObservabilityContext
}

var (
	defaultLogger *Logger
	loggerProvider *log.LoggerProvider
)

// Configure sets up logging based on the provided configuration.
// It should be called once at application startup.
func Configure(cfg *Config) (*Logger, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	
	// Get or create observability context
	obsCtx := obscontext.GetGlobalContext()
	if obsCtx.AppName == "" {
		obsCtx = obscontext.FromEnvironment()
		obscontext.SetGlobalContext(obsCtx)
	}
	
	// Override with config values if provided
	if cfg.ServiceName != "" {
		obsCtx.AppName = cfg.ServiceName
	}
	if cfg.ServiceVersion != "" {
		obsCtx.Version = cfg.ServiceVersion
	}
	if cfg.Environment != "" {
		obsCtx.Environment = cfg.Environment
	}
	
	var handlers []slog.Handler
	
	// Setup console handler
	if cfg.EnableConsole {
		var consoleHandler slog.Handler
		handlerOpts := &slog.HandlerOptions{
			Level: cfg.Level,
		}
		
		if cfg.JSONFormat {
			consoleHandler = slog.NewJSONHandler(cfg.ConsoleWriter, handlerOpts)
		} else {
			consoleHandler = slog.NewTextHandler(cfg.ConsoleWriter, handlerOpts)
		}
		
		handlers = append(handlers, consoleHandler)
	}
	
	// Setup OTLP handler
	if cfg.EnableOTLP {
		otlpHandler, err := setupOTLP(cfg, obsCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to setup OTLP: %w", err)
		}
		if otlpHandler != nil {
			handlers = append(handlers, otlpHandler)
		}
	}
	
	// Combine handlers
	var handler slog.Handler
	if len(handlers) == 1 {
		handler = handlers[0]
	} else if len(handlers) > 1 {
		handler = &multiHandler{handlers: handlers}
	} else {
		// Fallback to console
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.Level})
	}
	
	logger := &Logger{
		Logger: slog.New(handler),
		obsCtx: obsCtx,
	}
	
	defaultLogger = logger
	slog.SetDefault(logger.Logger)
	
	logger.Info("Logging configured",
		"service_name", obsCtx.AppName,
		"environment", obsCtx.Environment,
		"version", obsCtx.Version,
		"otlp_enabled", cfg.EnableOTLP,
	)
	
	return logger, nil
}

// setupOTLP configures OpenTelemetry Protocol logging export
func setupOTLP(cfg *Config, obsCtx *obscontext.ObservabilityContext) (slog.Handler, error) {
	// Create OTLP exporter
	exporter, err := otlploggrpc.New(
		context.Background(),
		otlploggrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlploggrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP exporter: %w", err)
	}
	
	// Create resource with OpenTelemetry semantic conventions
	resourceAttrs := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceName(obsCtx.AppName),
			semconv.ServiceVersion(obsCtx.Version),
			semconv.DeploymentEnvironment(obsCtx.Environment),
		),
	}
	
	// Add domain/namespace if available
	if obsCtx.Domain != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.ServiceNamespace(obsCtx.Domain)))
	}
	
	// Add Kubernetes attributes
	if obsCtx.PodName != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.K8SPodName(obsCtx.PodName)))
	}
	if obsCtx.Namespace != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.K8SNamespaceName(obsCtx.Namespace)))
	}
	if obsCtx.NodeName != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.K8SNodeName(obsCtx.NodeName)))
	}
	if obsCtx.ContainerName != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.K8SContainerName(obsCtx.ContainerName)))
	}
	
	// Add commit SHA
	if obsCtx.CommitSha != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.ServiceInstanceID(obsCtx.CommitSha[:8])))
	}
	
	res, err := resource.New(context.Background(), resourceAttrs...)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}
	
	// Create logger provider
	loggerProvider = log.NewLoggerProvider(
		log.WithResource(res),
		log.WithProcessor(log.NewBatchProcessor(exporter)),
	)
	
	global.SetLoggerProvider(loggerProvider)
	
	// Create OTLP handler
	handler := newOTLPHandler(loggerProvider, obsCtx)
	
	return handler, nil
}

// Default returns the default logger
func Default() *Logger {
	if defaultLogger == nil {
		// Auto-configure if not done yet
		logger, err := Configure(nil)
		if err != nil {
			// Fallback to basic logger
			return &Logger{
				Logger: slog.Default(),
				obsCtx: obscontext.NewContext(),
			}
		}
		return logger
	}
	return defaultLogger
}

// WithContext returns a logger with observability context
func (l *Logger) WithContext(ctx context.Context) *Logger {
	obsCtx := obscontext.FromContext(ctx)
	return &Logger{
		Logger: l.Logger,
		obsCtx: obsCtx,
	}
}

// WithAttrs adds attributes to the logger
func (l *Logger) WithAttrs(attrs ...slog.Attr) *Logger {
	return &Logger{
		Logger: l.Logger.With(attrsToAny(attrs)...),
		obsCtx: l.obsCtx,
	}
}

// InfoContext logs at Info level with context
func (l *Logger) InfoContext(ctx context.Context, msg string, args ...any) {
	args = l.enrichArgs(ctx, args)
	l.Logger.InfoContext(ctx, msg, args...)
}

// DebugContext logs at Debug level with context
func (l *Logger) DebugContext(ctx context.Context, msg string, args ...any) {
	args = l.enrichArgs(ctx, args)
	l.Logger.DebugContext(ctx, msg, args...)
}

// WarnContext logs at Warn level with context
func (l *Logger) WarnContext(ctx context.Context, msg string, args ...any) {
	args = l.enrichArgs(ctx, args)
	l.Logger.WarnContext(ctx, msg, args...)
}

// ErrorContext logs at Error level with context
func (l *Logger) ErrorContext(ctx context.Context, msg string, args ...any) {
	args = l.enrichArgs(ctx, args)
	l.Logger.ErrorContext(ctx, msg, args...)
}

// enrichArgs adds context attributes to log arguments
func (l *Logger) enrichArgs(ctx context.Context, args []any) []any {
	obsCtx := obscontext.FromContext(ctx)
	if obsCtx == nil {
		return args
	}
	
	// Add request/operation context
	enriched := make([]any, 0, len(args)+20)
	enriched = append(enriched, args...)
	
	if obsCtx.RequestID != "" {
		enriched = append(enriched, "request_id", obsCtx.RequestID)
	}
	if obsCtx.CorrelationID != "" {
		enriched = append(enriched, "correlation_id", obsCtx.CorrelationID)
	}
	if obsCtx.UserID != "" {
		enriched = append(enriched, "user_id", obsCtx.UserID)
	}
	if obsCtx.HTTPMethod != "" {
		enriched = append(enriched, "http_method", obsCtx.HTTPMethod)
	}
	if obsCtx.HTTPPath != "" {
		enriched = append(enriched, "http_path", obsCtx.HTTPPath)
	}
	if obsCtx.HTTPStatusCode > 0 {
		enriched = append(enriched, "http_status", obsCtx.HTTPStatusCode)
	}
	if obsCtx.WorkerID != "" {
		enriched = append(enriched, "worker_id", obsCtx.WorkerID)
	}
	if obsCtx.TaskID != "" {
		enriched = append(enriched, "task_id", obsCtx.TaskID)
	}
	if obsCtx.Operation != "" {
		enriched = append(enriched, "operation", obsCtx.Operation)
	}
	
	// Add custom attributes
	for k, v := range obsCtx.Custom {
		enriched = append(enriched, k, v)
	}
	
	return enriched
}

// Shutdown flushes any buffered logs and shuts down the logger provider
func Shutdown(ctx context.Context) error {
	if loggerProvider != nil {
		return loggerProvider.Shutdown(ctx)
	}
	return nil
}

// Helper functions

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func attrsToAny(attrs []slog.Attr) []any {
	result := make([]any, 0, len(attrs)*2)
	for _, attr := range attrs {
		result = append(result, attr.Key, attr.Value)
	}
	return result
}

// multiHandler sends logs to multiple handlers
type multiHandler struct {
	handlers []slog.Handler
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, r.Level) {
			if err := handler.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}
