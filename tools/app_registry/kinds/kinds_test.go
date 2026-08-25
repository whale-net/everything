package kinds

import (
	"testing"
)

// TestBinaryKindRegistration verifies that the binary kind is registered
// and can be retrieved.
func TestBinaryKindRegistration(t *testing.T) {
	// Binary kind should be auto-registered at init time
	kind := Get("binary")
	if kind == nil {
		t.Fatal("binary kind not registered")
	}
	if kind.Name() != "binary" {
		t.Errorf("kind.Name() = %q, want %q", kind.Name(), "binary")
	}
}

// TestBinaryKindHookSet verifies that the binary kind supplies all eight hooks.
func TestBinaryKindHookSet(t *testing.T) {
	kind := Get("binary")
	if kind == nil {
		t.Fatal("binary kind not registered")
	}

	hooks := kind.Hooks()
	if hooks == nil {
		t.Fatal("kind.Hooks() returned nil")
	}

	if hooks.H1() == nil || hooks.H1().Name() != "H1" {
		t.Errorf("H1 hook missing or malformed")
	}
	if hooks.H2() == nil || hooks.H2().Name() != "H2" {
		t.Errorf("H2 hook missing or malformed")
	}
	if hooks.H3() == nil || hooks.H3().Name() != "H3" {
		t.Errorf("H3 hook missing or malformed")
	}
	if hooks.H4() == nil || hooks.H4().Name() != "H4" {
		t.Errorf("H4 hook missing or malformed")
	}
	if hooks.H5() == nil || hooks.H5().Name() != "H5" {
		t.Errorf("H5 hook missing or malformed")
	}
	if hooks.H6() == nil || hooks.H6().Name() != "H6" {
		t.Errorf("H6 hook missing or malformed")
	}
	if hooks.H7() == nil || hooks.H7().Name() != "H7" {
		t.Errorf("H7 hook missing or malformed")
	}
	if hooks.H8() == nil || hooks.H8().Name() != "H8" {
		t.Errorf("H8 hook missing or malformed")
	}
}

// TestHookValueShapedClassification verifies that hooks are correctly
// classified as value-shaped or structural.
func TestHookValueShapedClassification(t *testing.T) {
	kind := Get("binary")
	hooks := kind.Hooks()

	tests := []struct {
		name         string
		hook         Hook
		wantShaped   bool
	}{
		{"H1", hooks.H1(), false}, // Structural
		{"H2", hooks.H2(), false}, // Structural
		{"H3", hooks.H3(), true},  // Value-shaped
		{"H4", hooks.H4(), true},  // Value-shaped
		{"H5", hooks.H5(), true},  // Value-shaped
		{"H6", hooks.H6(), true},  // Value-shaped
		{"H7", hooks.H7(), false}, // Structural
		{"H8", hooks.H8(), true},  // Value-shaped
	}

	for _, tc := range tests {
		if tc.hook.ValueShaped() != tc.wantShaped {
			t.Errorf("%s.ValueShaped() = %v, want %v", tc.name, tc.hook.ValueShaped(), tc.wantShaped)
		}
	}
}
