package context

import (
	"context"
	"os"
	"testing"
)

func TestNewContext(t *testing.T) {
	ctx := NewContext()
	if ctx == nil {
		t.Fatal("NewContext returned nil")
	}
	if ctx.Custom == nil {
		t.Error("Custom map should be initialized")
	}
}

func TestFromEnvironment(t *testing.T) {
	// Set test environment variables
	os.Setenv("APP_NAME", "test-app")
	os.Setenv("APP_DOMAIN", "test-domain")
	os.Setenv("APP_TYPE", "external-api")
	os.Setenv("APP_VERSION", "v1.0.0")
	os.Setenv("APP_ENV", "testing")
	defer func() {
		os.Unsetenv("APP_NAME")
		os.Unsetenv("APP_DOMAIN")
		os.Unsetenv("APP_TYPE")
		os.Unsetenv("APP_VERSION")
		os.Unsetenv("APP_ENV")
	}()

	ctx := FromEnvironment()

	if ctx.AppName != "test-app" {
		t.Errorf("Expected AppName 'test-app', got '%s'", ctx.AppName)
	}
	if ctx.Domain != "test-domain" {
		t.Errorf("Expected Domain 'test-domain', got '%s'", ctx.Domain)
	}
	if ctx.AppType != "external-api" {
		t.Errorf("Expected AppType 'external-api', got '%s'", ctx.AppType)
	}
	if ctx.Version != "v1.0.0" {
		t.Errorf("Expected Version 'v1.0.0', got '%s'", ctx.Version)
	}
	if ctx.Environment != "testing" {
		t.Errorf("Expected Environment 'testing', got '%s'", ctx.Environment)
	}
}

func TestFromEnvironmentDefaults(t *testing.T) {
	// Clear any existing env vars
	os.Unsetenv("APP_NAME")
	os.Unsetenv("APP_ENV")
	os.Unsetenv("ENVIRONMENT")

	ctx := FromEnvironment()

	if ctx.AppName != "unknown-app" {
		t.Errorf("Expected default AppName 'unknown-app', got '%s'", ctx.AppName)
	}
	if ctx.Version != "latest" {
		t.Errorf("Expected default Version 'latest', got '%s'", ctx.Version)
	}
	if ctx.Environment != "development" {
		t.Errorf("Expected default Environment 'development', got '%s'", ctx.Environment)
	}
}

func TestClone(t *testing.T) {
	original := NewContext()
	original.AppName = "test-app"
	original.RequestID = "req-123"
	original.Custom["key"] = "value"

	clone := original.Clone()

	// Check values are copied
	if clone.AppName != original.AppName {
		t.Error("Clone should have same AppName")
	}
	if clone.RequestID != original.RequestID {
		t.Error("Clone should have same RequestID")
	}
	if clone.Custom["key"] != "value" {
		t.Error("Clone should have custom values")
	}

	// Modify clone to verify deep copy
	clone.AppName = "modified"
	clone.Custom["key"] = "modified"

	if original.AppName == "modified" {
		t.Error("Modifying clone should not affect original")
	}
	if original.Custom["key"] == "modified" {
		t.Error("Modifying clone custom map should not affect original")
	}
}

func TestWithContextAndFromContext(t *testing.T) {
	obsCtx := NewContext()
	obsCtx.RequestID = "req-123"
	obsCtx.UserID = "user-456"

	ctx := context.Background()
	ctx = WithContext(ctx, obsCtx)

	retrieved := FromContext(ctx)

	if retrieved == nil {
		t.Fatal("FromContext returned nil")
	}
	if retrieved.RequestID != "req-123" {
		t.Errorf("Expected RequestID 'req-123', got '%s'", retrieved.RequestID)
	}
	if retrieved.UserID != "user-456" {
		t.Errorf("Expected UserID 'user-456', got '%s'", retrieved.UserID)
	}
}

func TestFromContextFallbackToGlobal(t *testing.T) {
	// Set global context
	globalCtx := NewContext()
	globalCtx.AppName = "global-app"
	SetGlobalContext(globalCtx)

	// Create context without ObservabilityContext
	ctx := context.Background()

	// Should fall back to global
	retrieved := FromContext(ctx)
	if retrieved.AppName != "global-app" {
		t.Errorf("Expected fallback to global context, got '%s'", retrieved.AppName)
	}
}

func TestSetAndGetGlobalContext(t *testing.T) {
	ctx := NewContext()
	ctx.AppName = "test-global"
	ctx.Version = "v2.0.0"

	SetGlobalContext(ctx)
	retrieved := GetGlobalContext()

	if retrieved.AppName != "test-global" {
		t.Errorf("Expected AppName 'test-global', got '%s'", retrieved.AppName)
	}
	if retrieved.Version != "v2.0.0" {
		t.Errorf("Expected Version 'v2.0.0', got '%s'", retrieved.Version)
	}
}

func TestUpdateGlobalContext(t *testing.T) {
	ctx := NewContext()
	ctx.AppName = "initial"
	SetGlobalContext(ctx)

	UpdateGlobalContext(func(c *ObservabilityContext) {
		c.AppName = "updated"
		c.Version = "v1.0.0"
	})

	retrieved := GetGlobalContext()
	if retrieved.AppName != "updated" {
		t.Errorf("Expected updated AppName 'updated', got '%s'", retrieved.AppName)
	}
	if retrieved.Version != "v1.0.0" {
		t.Errorf("Expected Version 'v1.0.0', got '%s'", retrieved.Version)
	}
}
