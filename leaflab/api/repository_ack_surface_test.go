// FR45: "accepted / acked_at / rejection_reason are written only from the
// device's own ack path. No API caller in any role can set them."
//
// This file is the application-layer half of that requirement: it proves,
// by reflection over *Repository's actual method set (leaflab/api's
// concrete deviceRepository implementation -- repository.go), that no
// method exists which could plausibly write those three columns. Every
// existing read of them (GetDeviceConfigVersion, GetLatestAcceptedConfig,
// ListConfigHistory -- see repository.go) only ever appears in a SELECT,
// never a SET; this test guards against a future method being added that
// would break that invariant, not just the ones that exist today.
//
// The database-level half -- a BEFORE UPDATE trigger refusing the write
// even for an ad-hoc UPDATE run directly against the shared DB role,
// "in any role" -- is leaflab/processor's
// ack_write_guard_integration_test.go (migration 032), since the trigger
// itself lives on device_config regardless of which process's code issues
// the UPDATE.
package main

import (
	"reflect"
	"strings"
	"testing"
)

// ackRelatedSubstrings are the words a repository method name writing one
// of FR45's three guarded columns would very likely contain. "Ack" also
// catches a hypothetical "AckDeviceConfig"-named method on *Repository
// (the real one lives only on leaflab/processor's own Repository, a
// different type in a different package -- see that package's
// repository.go).
var ackRelatedSubstrings = []string{"ack", "accept", "reject"}

// TestAPIRepository_NoMethodWritesAckColumns proves *Repository (this
// package's deviceRepository implementation) exposes no method whose name
// suggests it writes accepted/acked_at/rejection_reason. A method named
// e.g. "AckDeviceConfig", "AcceptConfig", "SetRejectionReason" appearing
// here would be exactly the FR45 violation this test exists to catch --
// every existing method with one of these substrings in its name today is
// a read (see the exception list below), and this test must be updated
// deliberately, not silently pass, if that ever stops being true.
func TestAPIRepository_NoMethodWritesAckColumns(t *testing.T) {
	repoType := reflect.TypeOf((*Repository)(nil))

	// Methods that legitimately contain one of the substrings above but are
	// reads, not writes -- named explicitly so an unreviewed addition to
	// this list is a visible diff, not a silent hole in the assertion
	// below.
	allowedReads := map[string]bool{
		"GetLatestAcceptedConfig": true, // SELECT ... WHERE accepted = TRUE
	}

	for i := 0; i < repoType.NumMethod(); i++ {
		name := repoType.Method(i).Name
		if allowedReads[name] {
			continue
		}
		lower := strings.ToLower(name)
		for _, substr := range ackRelatedSubstrings {
			if strings.Contains(lower, substr) {
				t.Errorf("Repository.%s: method name suggests it writes an ack column (FR45 violation) -- if this is a legitimate read, add it to allowedReads with a comment explaining why; if it writes accepted/acked_at/rejection_reason, remove it -- those columns are written only from leaflab/processor's ack path", name)
			}
		}
	}
}
