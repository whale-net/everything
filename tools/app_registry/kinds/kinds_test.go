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

// TestBinaryHookPolicies verifies that the binary kind's hook implementations
// return proper policy values.
func TestBinaryHookPolicies(t *testing.T) {
	kind := Get("binary")
	if kind == nil {
		t.Fatal("binary kind not registered")
	}

	hooks := kind.Hooks()

	// H1: artifact set composition
	h1 := hooks.H1()
	if h1.Policy() == "" {
		t.Error("H1.Policy() returned empty string")
	}

	// H2: variant dimensions
	h2 := hooks.H2()
	dims := h2.Dimensions()
	if len(dims) == 0 {
		t.Error("H2.Dimensions() returned empty slice")
	}
	if dims[0] != "os" || dims[1] != "arch" {
		t.Errorf("H2.Dimensions() = %v, want [os arch]", dims)
	}

	// H3: content type
	h3 := hooks.H3()
	if h3.ContentType() != "application/octet-stream" {
		t.Errorf("H3.ContentType() = %q, want application/octet-stream", h3.ContentType())
	}

	// H4: encoding policy
	h4 := hooks.H4()
	if h4.Encoding() != "gzip" {
		t.Errorf("H4.Encoding() = %q, want gzip", h4.Encoding())
	}

	// H5: consumer-facing file naming
	h5 := hooks.H5()
	if h5.FileNaming() == "" {
		t.Error("H5.FileNaming() returned empty string")
	}

	// H6: checksum manifest policy
	h6 := hooks.H6()
	if h6.ManifestPolicy() != "checksums.txt, SHA256, one per line" {
		t.Errorf("H6.ManifestPolicy() = %q, want checksums.txt, SHA256, one per line", h6.ManifestPolicy())
	}

	// H7: app-type mapping
	h7 := hooks.H7()
	mapping := h7.AppTypeMapping()
	if len(mapping) == 0 {
		t.Error("H7.AppTypeMapping() returned empty slice")
	}

	// H8: pre-cutover template (legitimately empty for binary kind)
	h8 := hooks.H8()
	_ = h8.PreCutoverTemplate() // Should be empty for binary kind
}
