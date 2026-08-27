package audit

import (
	"strings"
	"testing"
)

// TestValidateRegistrations_AllRegistered_ReturnsNil proves the happy path:
// every declared write method has a corresponding audit.Registration entry,
// so startup should proceed with no error.
func TestValidateRegistrations_AllRegistered_ReturnsNil(t *testing.T) {
	writeMethods := []string{"/svc/MethodA", "/svc/MethodB"}
	registrations := map[string]Registration{
		"/svc/MethodA": {Action: "ActionA", EntityKind: "thing"},
		"/svc/MethodB": {Action: "ActionB", EntityKind: "thing"},
	}
	if err := ValidateRegistrations(writeMethods, registrations); err != nil {
		t.Fatalf("ValidateRegistrations = %v, want nil", err)
	}
}

// TestValidateRegistrations_MissingRegistration_ReturnsError is the load-bearing
// case: a declared write method with no audit.Registration entry must be
// reported, naming the offending method, rather than silently passing --
// this is the runtime half of "a write RPC shipped with no audit record
// fails loudly" (FR8; see MustValidateAuditRegistrations in
// leaflab/api/audit_registry.go, which panics on this same error).
func TestValidateRegistrations_MissingRegistration_ReturnsError(t *testing.T) {
	writeMethods := []string{"/svc/MethodA", "/svc/MethodB"}
	registrations := map[string]Registration{
		"/svc/MethodA": {Action: "ActionA", EntityKind: "thing"},
		// MethodB deliberately absent.
	}
	err := ValidateRegistrations(writeMethods, registrations)
	if err == nil {
		t.Fatal("ValidateRegistrations = nil, want an error naming the unregistered method")
	}
	if !strings.Contains(err.Error(), "/svc/MethodB") {
		t.Errorf("error = %q, want it to name the unregistered method %q", err, "/svc/MethodB")
	}
}

// TestValidateRegistrations_EmptyWriteMethods_ReturnsNil covers the
// degenerate case explicitly: no declared write methods means nothing to
// validate, not an error.
func TestValidateRegistrations_EmptyWriteMethods_ReturnsNil(t *testing.T) {
	if err := ValidateRegistrations(nil, map[string]Registration{}); err != nil {
		t.Fatalf("ValidateRegistrations with no declared write methods = %v, want nil", err)
	}
}
