//go:build integration

// This file guards issue #2600's Testing section: the full GET /link/whagent
// + POST /link/whagent/confirm flow against a real Postgres, real handlers,
// and a test JWKS server standing in for `ui` -- mirroring
// audience_score_system/web/invite/invite_integration_test.go's shape (see
// that file's package doc comment for the dbtest/embedded-migrations
// pattern this task's Implementation phase wires up here). Gated behind
// `//go:build integration` so `bazel test //...` never compiles or runs it;
// see //libs/go/dbtest's README for why.
//
// Scaffold phase: every test below is a named TODO stub. The Testing phase
// replaces this file's bodies (not its names) with the real assertions this
// task's Testing section lists.
package link_test

import "testing"

// TestHandleShow_ValidAssertionSignedIn_RendersConfirmationBothSidesNoWrite
// asserts a valid assertion + signed-in Operator renders the confirmation
// page naming both sides (FR5) with zero person_oidc_identity rows written.
func TestHandleShow_ValidAssertionSignedIn_RendersConfirmationBothSidesNoWrite(t *testing.T) {
	// TODO: Implement test.
}

// TestHandleConfirm_Creates_RedirectsLinked asserts confirming a fresh
// assertion writes exactly one person_oidc_identity row (LinkCreated) and
// redirects to the return URL with the "linked" outcome (FR6/FR10).
func TestHandleConfirm_Creates_RedirectsLinked(t *testing.T) {
	// TODO: Implement test.
}

// TestHandleConfirm_Twice_SecondIsAlreadyLinkedNoDuplicate asserts
// confirming the same (iss, sub)/person pair twice yields "already linked"
// on the second call with no duplicate row (FR9).
func TestHandleConfirm_Twice_SecondIsAlreadyLinkedNoDuplicate(t *testing.T) {
	// TODO: Implement test.
}

// TestHandleConfirm_LinkedToOtherPerson_Conflict asserts an assertion whose
// (iss, sub) pair is already linked to a different Person yields the
// "conflict" outcome and leaves the existing row unchanged (FR8).
func TestHandleConfirm_LinkedToOtherPerson_Conflict(t *testing.T) {
	// TODO: Implement test.
}

// TestRejectedAssertions_ZeroWrites asserts a tampered signature, an
// expired assertion, and an already-consumed jti are each rejected --
// redirecting with the "rejected assertion" outcome and writing zero
// person_oidc_identity rows in every case (FR3).
func TestRejectedAssertions_ZeroWrites(t *testing.T) {
	// TODO: Implement test.
}

// TestHandleShow_Unauthenticated_RoutesToSignInAndResumesSameAssertion
// asserts an unauthenticated GET /link/whagent is routed to Google sign-in
// via the existing `?next=` continuation and, after signing in, lands back
// on the confirmation page for the SAME assertion (FR4) with no re-mint.
func TestHandleShow_Unauthenticated_RoutesToSignInAndResumesSameAssertion(t *testing.T) {
	// TODO: Implement test.
}

// TestHandleConfirm_Unauthenticated_Rejected asserts an unauthenticated
// POST /link/whagent/confirm is rejected and writes nothing.
func TestHandleConfirm_Unauthenticated_Rejected(t *testing.T) {
	// TODO: Implement test.
}

// TestHandleShow_NeverWrites asserts GET /link/whagent alone never writes a
// person_oidc_identity row, by comparing row counts before/after.
func TestHandleShow_NeverWrites(t *testing.T) {
	// TODO: Implement test.
}

// TestHandleConfirm_ReturnURLWrongOrigin_Rejected asserts a return URL
// pointing at a host other than ASS_WHAGENT_UI_ISSUER's origin is rejected,
// never redirected to (open-redirect guard).
func TestHandleConfirm_ReturnURLWrongOrigin_Rejected(t *testing.T) {
	// TODO: Implement test.
}

// TestNFR3LogLevels_NoRawTokenLogged asserts a successful link logs INFO
// with person_id and the (iss, sub) pair, a rejection or conflict logs
// WARNING, and no test log output contains the raw assertion token (NFR3).
func TestNFR3LogLevels_NoRawTokenLogged(t *testing.T) {
	// TODO: Implement test.
}

// TestConsumeMustHappenOnConfirmNotShow is this task's required red/green
// proof: moving the Consume call into HandleShow must make the
// "abandon confirmation then retry" case go red, proving HandleConfirm (not
// HandleShow) owns the consuming write -- see this task's Testing section.
func TestConsumeMustHappenOnConfirmNotShow(t *testing.T) {
	// TODO: Implement test.
}
