package main

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/whale-net/everything/manmanv2/ui/components/ui"
)

func TestHelloComponent(t *testing.T) {
	// Create a test HTTP response recorder
	w := httptest.NewRecorder()
	
	// Render the component with a context
	component := ui.Hello("World")
	err := component.Render(context.Background(), w)
	
	if err != nil {
		t.Fatalf("Failed to render component: %v", err)
	}
	
	// Check the response
	body := w.Body.String()
	if body == "" {
		t.Fatal("Component rendered empty body")
	}
	
	// Check for expected content
	if !contains(body, "Hello, World!") {
		t.Errorf("Expected 'Hello, World!' in output, got: %s", body)
	}
	
	if !contains(body, "This is a templ component") {
		t.Errorf("Expected 'This is a templ component' in output, got: %s", body)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
