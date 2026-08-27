package audit

import "fmt"

// Registration is the action/entity_kind pair a write RPC's audit record
// must carry -- the value half of the write-method audit registry each
// service declares (e.g. leaflab/api's auditWriteMethods/auditRegistrations
// in audit_registry.go).
type Registration struct {
	Action     string
	EntityKind string
}

// ValidateRegistrations reports an error if any method in writeMethods has
// no entry in registrations. This is the runtime half of "a write RPC
// shipped with no audit record fails loudly" (FR8; the build-failing half
// is NFR1.b's conformance check, #1351) -- called once at server startup,
// not per-request, so a missing registration is caught before the server
// ever accepts traffic rather than surfacing as a missing audit row in
// production.
func ValidateRegistrations(writeMethods []string, registrations map[string]Registration) error {
	for _, method := range writeMethods {
		if _, ok := registrations[method]; !ok {
			return fmt.Errorf("audit: write method %q is registered but has no audit registration (FR8)", method)
		}
	}
	return nil
}
