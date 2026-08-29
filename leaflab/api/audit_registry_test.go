package main

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/leaflab/api/audit"
)

// TestMustValidateAuditRegistrations_OKForProductionRegistry is a
// regression guard for the Validation section's "every write RPC on the
// server has an audit registration": exercises the actual production
// declaredWriteMethods/auditRegistrations wiring (not a synthetic one), the
// same call buildServer makes at startup, so a future write method added to
// declaredWriteMethods with no matching auditRegistrations entry fails this
// test -- not just a live server's startup panic discovered later.
func TestMustValidateAuditRegistrations_OKForProductionRegistry(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MustValidateAuditRegistrations panicked against the production registry: %v", r)
		}
	}()
	MustValidateAuditRegistrations()
}

// TestMustValidateAuditRegistrations_PanicsWhenWriteMethodUnregistered is
// the load-bearing test for FR8's "fail loudly at startup for a registered
// write method with no audit registration": temporarily declares a write
// method with no corresponding audit.Registration entry and asserts
// MustValidateAuditRegistrations panics, naming the unregistered method,
// exactly as it would at real server startup (see buildServer in main.go).
func TestMustValidateAuditRegistrations_PanicsWhenWriteMethodUnregistered(t *testing.T) {
	origMethods := declaredWriteMethods
	origRegistrations := auditRegistrations
	t.Cleanup(func() {
		declaredWriteMethods = origMethods
		auditRegistrations = origRegistrations
	})

	const unregisteredMethod = "/leaflab.api.v1.LeafLabAPI/SomeFutureWriteRPC"
	declaredWriteMethods = []string{unregisteredMethod}
	auditRegistrations = map[string]audit.Registration{} // deliberately missing an entry for unregisteredMethod

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustValidateAuditRegistrations did not panic for a write method with no audit registration")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value = %v (%T), want a string mentioning the unregistered method", r, r)
		}
		if !strings.Contains(msg, unregisteredMethod) {
			t.Errorf("panic message = %q, want it to name %q", msg, unregisteredMethod)
		}
	}()
	MustValidateAuditRegistrations()
}
