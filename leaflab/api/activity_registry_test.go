package main

import (
	"testing"

	"github.com/whale-net/everything/leaflab/api/activity"
	"github.com/whale-net/everything/leaflab/api/audit"
)

// TestActivityRegistry_CoversEveryAuditedAction is this task's Validation
// criterion, "Every audit action registered by the audit task has a
// rendering -- assert exhaustively", proven against the actual production
// auditRegistrations map (audit_registry.go), not a hand-copied list of
// action names that could silently drift from it. Also covers
// SupportReferenceResolveAction (audited via RecordSupportReferenceResolve
// directly, not through a declaredWriteMethods entry -- see
// support_reference.go's doc comment) and the ClaimAttempt/board synthetic
// entry (FR76.7, sourced from claim_challenge, never audit_log).
//
// Every current registration's acting principal is human (see this file's
// package-level grep: no leaflab/api call site constructs an audit.Entry
// with ActorKindSystem yet), so audit.ActorKindHuman is the correct third
// element of the Key this test looks up for every entry -- a future
// registration with a system actor would need this test's fixture updated
// alongside it, which is exactly the kind of change that should draw a
// reviewer's eye to whether Registry needs its own ActorKindSystem entry
// too.
//
// This is the test that "must fail when a new action is added without a
// sentence" (this task's Testing section): adding an entry to
// auditRegistrations with no corresponding leaflab/api/activity.Registry
// entry makes activity.Render return ok=false here, immediately, with no
// separate wiring required.
func TestActivityRegistry_CoversEveryAuditedAction(t *testing.T) {
	type auditedAction struct {
		label      string
		action     string
		entityKind string
	}

	var audited []auditedAction
	for method, reg := range auditRegistrations {
		audited = append(audited, auditedAction{label: method, action: reg.Action, entityKind: reg.EntityKind})
	}
	// SupportReferenceResolve: a real audited action with no
	// declaredWriteMethods entry of its own (it's a branch inside
	// ResolveToHousehold, already registered above under a different
	// action name) -- see leaflab/api/activity's Registry doc comment.
	audited = append(audited, auditedAction{
		label:      "SupportReferenceResolve (branch of ResolveToHousehold)",
		action:     supportReferenceResolveAction,
		entityKind: "support_reference",
	})
	// FR76.7's synthetic claim-attempt entry: never has an audit_log row at
	// all (CompleteClaim only audits a successful claim against a
	// never-claimed/Unadopted board), but ListHouseholdActivity still must
	// be able to render it.
	audited = append(audited, auditedAction{
		label:      "ClaimAttempt/board synthetic entry (FR76.7)",
		action:     activity.ClaimAttemptAction,
		entityKind: activity.ClaimAttemptEntityKind,
	})

	if len(audited) == 0 {
		t.Fatal("no audited actions found to check -- this test would silently pass on a broken build")
	}

	for _, a := range audited {
		a := a
		t.Run(a.label, func(t *testing.T) {
			sentence, ok := activity.Render(a.action, a.entityKind, audit.ActorKindHuman, activity.RenderInput{
				ActorLabel:  "you",
				EntityLabel: "a household member",
				Outcome:     activity.ClaimAttemptNotDischarged, // harmless no-op for every Template except ClaimAttempt's
			})
			if !ok {
				t.Fatalf("no leaflab/api/activity.Registry entry for (action=%q, entity_kind=%q, actor_kind=human) -- %s has no rendering (FR9)", a.action, a.entityKind, a.label)
			}
			if sentence == "" {
				t.Fatalf("leaflab/api/activity.Render(%q, %q) returned an empty sentence", a.action, a.entityKind)
			}
		})
	}
}
