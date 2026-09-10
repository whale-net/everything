package migrate

import "testing"

func TestValidateSources_RejectsEmptyName(t *testing.T) {
	err := validateSources([]Source{{Name: ""}})
	if err == nil {
		t.Fatal("expected error for empty Name, got nil")
	}
}

func TestValidateSources_RejectsDuplicateNames(t *testing.T) {
	err := validateSources([]Source{{Name: "htmxauth"}, {Name: "htmxauth"}})
	if err == nil {
		t.Fatal("expected error for duplicate Name, got nil")
	}
}

func TestValidateSources_AcceptsDistinctNames(t *testing.T) {
	err := validateSources([]Source{{Name: "htmxauth"}, {Name: "other"}})
	if err != nil {
		t.Fatalf("expected no error for distinct names, got: %v", err)
	}
}

func TestValidateSources_AcceptsEmptySources(t *testing.T) {
	if err := validateSources(nil); err != nil {
		t.Fatalf("expected no error for no sources, got: %v", err)
	}
}
