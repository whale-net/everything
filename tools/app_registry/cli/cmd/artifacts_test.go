package cmd

import "testing"

// TestNewArtifactsAdoptCmd_RegistersRequiredFlags locks in the flag surface
// `artifacts adopt` exposes (AR-7e, issue #558) -- a future accidental
// rename would break OPERATIONS.md's documented disaster-recovery runbook
// silently otherwise. --reason is the one flag that matters most here: it
// is the audit trail for a deliberately rare, human-triggered operation.
func TestNewArtifactsAdoptCmd_RegistersRequiredFlags(t *testing.T) {
	c := newArtifactsAdoptCmd()

	for _, name := range []string{"kind", "owner", "repository", "version", "digest", "reason", "contains", "idempotency-key"} {
		if c.Flags().Lookup(name) == nil {
			t.Fatalf("expected a --%s flag", name)
		}
	}

	required := map[string]bool{}
	for _, name := range []string{"kind", "owner", "version", "digest", "reason"} {
		required[name] = true
	}
	for name := range required {
		f := c.Flags().Lookup(name)
		if f.Annotations["cobra_annotation_bash_completion_one_required_flag"] == nil {
			t.Errorf("expected --%s to be marked required", name)
		}
	}
	// --idempotency-key is deliberately NOT required here (unlike
	// `artifacts record`/`begin-publish`/`fail-publish`, the CI write
	// paths): this is a human action, so a UUID is generated if omitted --
	// see promoteIdempotencyKey.
	if idem := c.Flags().Lookup("idempotency-key"); idem.Annotations["cobra_annotation_bash_completion_one_required_flag"] != nil {
		t.Error("expected --idempotency-key to NOT be required (a UUID is generated if omitted)")
	}

	if c.Use != "adopt" {
		t.Errorf("Use = %q, want %q", c.Use, "adopt")
	}
}

// TestNewArtifactsListCmd_RegistersProvenanceFlagAndOptionalOwner covers
// AR-7e's `artifacts list --provenance adopted` -- the owner positional arg
// must be OPTIONAL so "every adopted row across every owner" is answerable
// in one call, matching the exit criterion's "distinguishable in one
// query."
func TestNewArtifactsListCmd_RegistersProvenanceFlagAndOptionalOwner(t *testing.T) {
	c := newArtifactsListCmd()

	if c.Flags().Lookup("provenance") == nil {
		t.Fatal("expected a --provenance flag")
	}
	if err := c.Args(c, nil); err != nil {
		t.Errorf("expected zero args to be valid (owner is optional), got %v", err)
	}
	if err := c.Args(c, []string{"demo-svc"}); err != nil {
		t.Errorf("expected one arg to still be valid, got %v", err)
	}
	if err := c.Args(c, []string{"a", "b"}); err == nil {
		t.Error("expected two args to be rejected")
	}
}
