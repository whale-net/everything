// Package observability provides a unified interface for logging and tracing
// with console and OTLP support for Go applications.
//
// This package re-exports the main types and functions from the logging,
// tracing, and context subpackages for convenient access.
package observability

import (
	"context"

	obscontext "github.com/whale-net/everything/libs/go/observability/context"
	"github.com/whale-net/everything/libs/go/observability/logging"
	"github.com/whale-net/everything/libs/go/observability/tracing"
)

// Context types and functions
type ObservabilityContext = obscontext.ObservabilityContext

var (
	NewContext         = obscontext.NewContext
	FromEnvironment    = obscontext.FromEnvironment
	WithContext        = obscontext.WithContext
	FromContext        = obscontext.FromContext
	SetGlobalContext   = obscontext.SetGlobalContext
	GetGlobalContext   = obscontext.GetGlobalContext
	UpdateGlobalContext = obscontext.UpdateGlobalContext
)

// Logging types and functions
type (
	LogConfig = logging.Config
	Logger    = logging.Logger
)

var (
	DefaultLogConfig = logging.DefaultConfig
	ConfigureLogging = logging.Configure
	DefaultLogger    = logging.Default
	ShutdownLogging  = logging.Shutdown
)

// Tracing types and functions
type TraceConfig = tracing.Config

var (
	DefaultTraceConfig = tracing.DefaultConfig
	ConfigureTracing   = tracing.Configure
	Tracer            = tracing.Tracer
	StartSpan         = tracing.StartSpan
	StartSpanWithContext = tracing.StartSpanWithContext
	ShutdownTracing   = tracing.Shutdown
)

// ConfigureAll is a convenience function to configure both logging and tracing
// with default settings from environment variables.
func ConfigureAll() error {
	// Configure logging
	if _, err := ConfigureLogging(nil); err != nil {
		return err
	}
	
	// Configure tracing
	if _, err := ConfigureTracing(nil); err != nil {
		return err
	}
	
	return nil
}

// ShutdownAll shuts down both logging and tracing
func ShutdownAll(ctx context.Context) error {
	if err := ShutdownLogging(ctx); err != nil {
		return err
	}
	if err := ShutdownTracing(ctx); err != nil {
		return err
	}
	return nil
}
