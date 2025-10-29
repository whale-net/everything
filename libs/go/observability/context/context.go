// Package context provides thread-safe context management for observability.
// It stores request, operation, and user context that can be automatically
// injected into logs and traces.
package context

import (
	"context"
	"os"
	"sync"
)

// ObservabilityContext holds contextual information for logging and tracing.
// This mirrors the Python LogContext design.
type ObservabilityContext struct {
	// Application metadata (typically auto-detected from environment)
	AppName      string
	Domain       string
	AppType      string
	Version      string
	Environment  string
	CommitSha    string
	BazelTarget  string
	
	// Infrastructure metadata (from Kubernetes/Helm)
	PodName       string
	Namespace     string
	NodeName      string
	ContainerName string
	ChartName     string
	ReleaseName   string
	Hostname      string
	Platform      string
	Architecture  string
	
	// Request/operation context
	RequestID     string
	CorrelationID string
	UserID        string
	SessionID     string
	TenantID      string
	OrgID         string
	
	// HTTP context
	HTTPMethod     string
	HTTPPath       string
	HTTPStatusCode int
	ClientIP       string
	UserAgent      string
	
	// Worker/Job context
	WorkerID  string
	TaskID    string
	JobID     string
	Operation string
	
	// Resource identification
	ResourceID string
	EventType  string
	ProcessID  int
	ThreadID   int
	
	// Custom attributes
	Custom map[string]interface{}
}

// contextKey is a private type for context keys to avoid collisions
type contextKey struct{}

var (
	// obsContextKey is the key for storing ObservabilityContext in context.Context
	obsContextKey = contextKey{}
	
	// globalContext stores the application-wide default context
	globalContext *ObservabilityContext
	globalMutex   sync.RWMutex
)

// NewContext creates a new ObservabilityContext with defaults.
func NewContext() *ObservabilityContext {
	return &ObservabilityContext{
		Custom: make(map[string]interface{}),
	}
}

// FromEnvironment creates an ObservabilityContext from environment variables.
// This auto-detects application metadata set by release_app macro and Helm charts.
func FromEnvironment() *ObservabilityContext {
	ctx := NewContext()
	
	// Application metadata
	ctx.AppName = getEnv("APP_NAME", "unknown-app")
	ctx.Domain = getEnv("APP_DOMAIN", "")
	ctx.AppType = getEnv("APP_TYPE", "")
	ctx.Version = getEnv("APP_VERSION", "latest")
	ctx.Environment = getEnv("APP_ENV", getEnv("ENVIRONMENT", "development"))
	ctx.CommitSha = getEnv("GIT_COMMIT", getEnv("COMMIT_SHA", ""))
	ctx.BazelTarget = getEnv("BAZEL_TARGET", "")
	
	// Infrastructure metadata
	ctx.PodName = getEnv("POD_NAME", "")
	ctx.Namespace = getEnv("NAMESPACE", getEnv("POD_NAMESPACE", ""))
	ctx.NodeName = getEnv("NODE_NAME", "")
	ctx.ContainerName = getEnv("CONTAINER_NAME", "")
	ctx.ChartName = getEnv("HELM_CHART_NAME", "")
	ctx.ReleaseName = getEnv("HELM_RELEASE_NAME", "")
	ctx.Hostname = getEnv("HOSTNAME", "")
	ctx.Platform = getEnv("PLATFORM", "")
	ctx.Architecture = getEnv("ARCHITECTURE", "")
	
	return ctx
}

// getEnv gets an environment variable with a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// Clone creates a deep copy of the context
func (c *ObservabilityContext) Clone() *ObservabilityContext {
	clone := *c
	clone.Custom = make(map[string]interface{})
	for k, v := range c.Custom {
		clone.Custom[k] = v
	}
	return &clone
}

// WithContext adds the ObservabilityContext to a context.Context
func WithContext(ctx context.Context, obsCtx *ObservabilityContext) context.Context {
	return context.WithValue(ctx, obsContextKey, obsCtx)
}

// FromContext retrieves the ObservabilityContext from a context.Context
func FromContext(ctx context.Context) *ObservabilityContext {
	if obsCtx, ok := ctx.Value(obsContextKey).(*ObservabilityContext); ok {
		return obsCtx
	}
	return GetGlobalContext()
}

// SetGlobalContext sets the application-wide default context
func SetGlobalContext(ctx *ObservabilityContext) {
	globalMutex.Lock()
	defer globalMutex.Unlock()
	globalContext = ctx
}

// GetGlobalContext returns the application-wide default context
func GetGlobalContext() *ObservabilityContext {
	globalMutex.RLock()
	defer globalMutex.RUnlock()
	if globalContext != nil {
		return globalContext
	}
	return NewContext()
}

// UpdateGlobalContext updates fields in the global context
func UpdateGlobalContext(updater func(*ObservabilityContext)) {
	globalMutex.Lock()
	defer globalMutex.Unlock()
	if globalContext == nil {
		globalContext = NewContext()
	}
	updater(globalContext)
}
