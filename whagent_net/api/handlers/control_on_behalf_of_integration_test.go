//go:build integration

// This file proves canControl's two independent control rules (FR10, FR13,
// NFR1):
//
//	(a) the caller is the session's on_behalf_of subject (the identity the
//	    session runs as), OR
//	(b) the caller is the session's subject (the identity that started it) AND
//	    the *current call's* Keycloak client_id is on the on-behalf-of
//	    allowlist.
//
// Branch (b) is what lets the allowlisted client that started a delegated
// session keep driving it after handing it off, without widening control to
// arbitrary callers. These tests pin both branches, the fail-closed empty
// allowlist, and the "removal takes effect on the very next call" (no-caching)
// property, for both SendTurn and StopSession -- the two RPCs that consult
// canControl.
//
// Like start_on_behalf_of_integration_test.go, these tests drive
// SessionServer.SendTurn/StopSession directly under a context built with
// grpcauth.ContextWithClaims rather than over a real gRPC/bufconn call: the
// gate reads grpcauth.Claims.ClientID (and the reconstructed subject) out of
// ctx exactly as it would off a verified Keycloak token, and the interceptor
// chain cannot inject a caller with a chosen client_id -- which is the entire
// subject of these cases.
package handlers_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/whagent_net/api/handlers"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
)

// fcmServiceSubject is the subject_* identity of a delegated session started
// by friendly_computing_machine: the authenticated caller. fcmServiceClaims
// reconstructs exactly this subject off a token carrying Sub "fcm-service".
var fcmServiceSubject = session.Subject{Iss: testIssuer, Sub: "fcm-service", Kind: session.SubjectKindService}

// delegatedUserSubject is the on_behalf_of_* identity a delegated session runs
// as. Its iss is deliberately the server's own issuer: canControl's
// callerIdentity always stamps the reconstructed caller's Iss with the
// server's issuer, so an on_behalf_of subject can only match branch (a) if it
// shares that iss (subjectsEqual compares (iss, sub), LB2).
var delegatedUserSubject = session.Subject{Iss: testIssuer, Sub: "slack-user-1", Kind: session.SubjectKindHuman}

// createDelegatedSession inserts the canonical delegated fixture: subject =
// fcmServiceSubject, on_behalf_of = delegatedUserSubject, running. The two
// identity columns are deliberately unequal so a passing test proves the
// intended branch, not an incidental "both columns match" coincidence.
func createDelegatedSession(t *testing.T, ctx context.Context, store *session.Store) *session.Session {
	t.Helper()
	return createSessionWithSubjects(t, ctx, store, fcmServiceSubject, delegatedUserSubject, session.StatusRunning)
}

// fcmServiceClaims / delegatedUserClaims / thirdPartyClaims build the three
// caller identities the cases below distinguish. thirdPartyClaims carries the
// *allowlisted* client_id but a different `sub`, so it exercises "allowlisted
// but not the session's subject" (case 4). Every subject here uses the
// server's issuer so the reconstructed caller can match a stored column.
func fcmServiceClaims(clientID string) context.Context {
	return delegatingClaims("fcm-service", clientID)
}

func delegatedUserClaims() context.Context {
	return delegatingClaims("slack-user-1", "user-frontend")
}

func thirdPartyClaims() context.Context {
	return delegatingClaims("third-party", fcmClientID)
}

// --- Case 1: allowlisted starting caller controls a delegated session (OK) ---

// TestSendTurn_AllowlistedStarter_ControlsDelegatedSession proves branch (b):
// the allowlisted client that started a delegated session (subject) can send a
// turn to it even though it is not the on_behalf_of subject. The session is
// running, so a full success (nil error, response carrying the session) is the
// signal -- PermissionDenied would mean canControl refused branch (b).
func TestSendTurn_AllowlistedStarter_ControlsDelegatedSession(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	sess := createDelegatedSession(t, ctx, store)

	resp, err := srv.SendTurn(fcmServiceClaims(fcmClientID), &pb.SendTurnRequest{
		SessionId: sess.SessionID.String(),
		Input:     "status?",
	})
	require.NoError(t, err, "the allowlisted starter must be able to control its delegated session (branch b)")
	assert.Equal(t, sess.SessionID.String(), resp.Session.SessionId)
}

// TestStopSession_AllowlistedStarter_ControlsDelegatedSession is
// TestSendTurn_AllowlistedStarter_ControlsDelegatedSession's proof for
// StopSession.
func TestStopSession_AllowlistedStarter_ControlsDelegatedSession(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	sess := createDelegatedSession(t, ctx, store)

	resp, err := srv.StopSession(fcmServiceClaims(fcmClientID), &pb.StopSessionRequest{
		SessionId: sess.SessionID.String(),
	})
	require.NoError(t, err, "the allowlisted starter must be able to stop its delegated session (branch b)")
	assert.Equal(t, sess.SessionID.String(), resp.Session.SessionId)
}

// --- Case 2: removal from the allowlist takes effect on the next call ---

// TestSendTurn_StarterRemovedFromAllowlist_Denied proves that control is
// re-checked live per-call, never cached: the SAME session and the SAME
// allowlisted-caller identity that succeed above are denied the moment a server
// whose allowlist no longer contains fcm evaluates them. Only the allowlist
// changed between the two calls.
func TestSendTurn_StarterRemovedFromAllowlist_Denied(t *testing.T) {
	ctx := context.Background()
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	sess := createDelegatedSession(t, ctx, store)
	fcm := fcmServiceClaims(fcmClientID)

	// Precondition: while fcm is allowlisted, the starter controls the session.
	_, err := srv.SendTurn(fcm, &pb.SendTurnRequest{SessionId: sess.SessionID.String(), Input: "hi"})
	require.NoError(t, err, "precondition: the allowlisted starter must control while allowlisted")

	// A second server over the SAME store, same session row, but an allowlist
	// that no longer contains fcm.
	removed := handlers.NewSessionServer(ctx, store, testIssuer, &fakeTemporalClient{}, "test-task-queue", nil, nil, []string{"some-other-client"})
	_, err = removed.SendTurn(fcm, &pb.SendTurnRequest{SessionId: sess.SessionID.String(), Input: "hi"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "removing fcm from the allowlist must deny control on the very next call")
}

// TestStopSession_StarterRemovedFromAllowlist_Denied is
// TestSendTurn_StarterRemovedFromAllowlist_Denied's proof for StopSession.
func TestStopSession_StarterRemovedFromAllowlist_Denied(t *testing.T) {
	ctx := context.Background()
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	sess := createDelegatedSession(t, ctx, store)
	fcm := fcmServiceClaims(fcmClientID)

	// Precondition: while fcm is allowlisted, the starter can stop the session.
	_, err := srv.StopSession(fcm, &pb.StopSessionRequest{SessionId: sess.SessionID.String()})
	require.NoError(t, err, "precondition: the allowlisted starter must stop while allowlisted")

	removed := handlers.NewSessionServer(ctx, store, testIssuer, &fakeTemporalClient{}, "test-task-queue", nil, nil, []string{"some-other-client"})
	_, err = removed.StopSession(fcm, &pb.StopSessionRequest{SessionId: sess.SessionID.String()})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "removing fcm from the allowlist must deny stop on the very next call")
}

// --- Case 3: empty allowlist fails closed (NFR1) ---

// TestSendTurn_EmptyAllowlist_StarterDenied proves NFR1: with an unset/empty
// allowlist, branch (b) never grants, so even the session's own subject (fcm)
// cannot control its delegated session through the allowlist path.
func TestSendTurn_EmptyAllowlist_StarterDenied(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t) // no allowlist entries
	ctx := context.Background()
	sess := createDelegatedSession(t, ctx, store)

	_, err := srv.SendTurn(fcmServiceClaims(fcmClientID), &pb.SendTurnRequest{
		SessionId: sess.SessionID.String(),
		Input:     "hi",
	})
	require.Error(t, err, "an empty allowlist must fail closed for the starter (NFR1)")
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestStopSession_EmptyAllowlist_StarterDenied is
// TestSendTurn_EmptyAllowlist_StarterDenied's proof for StopSession.
func TestStopSession_EmptyAllowlist_StarterDenied(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t) // no allowlist entries
	ctx := context.Background()
	sess := createDelegatedSession(t, ctx, store)

	_, err := srv.StopSession(fcmServiceClaims(fcmClientID), &pb.StopSessionRequest{
		SessionId: sess.SessionID.String(),
	})
	require.Error(t, err, "an empty allowlist must fail closed for the starter (NFR1)")
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// --- Case 4: a third, allowlisted caller is still denied ---

// TestSendTurn_ThirdCaller_Allowlisted_StillDenied proves branch (b) does not
// grant control to arbitrary allowlisted clients: the caller carries the
// allowlisted client_id but a different `sub` than the session's subject, so
// subjectsEqual(sess.Subject, caller) is false and control is denied.
func TestSendTurn_ThirdCaller_Allowlisted_StillDenied(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	sess := createDelegatedSession(t, ctx, store)

	_, err := srv.SendTurn(thirdPartyClaims(), &pb.SendTurnRequest{
		SessionId: sess.SessionID.String(),
		Input:     "hi",
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "an allowlisted client_id alone must not grant control to a third caller")
}

// TestStopSession_ThirdCaller_Allowlisted_StillDenied is
// TestSendTurn_ThirdCaller_Allowlisted_StillDenied's proof for StopSession.
func TestStopSession_ThirdCaller_Allowlisted_StillDenied(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t, fcmClientID)
	ctx := context.Background()
	sess := createDelegatedSession(t, ctx, store)

	_, err := srv.StopSession(thirdPartyClaims(), &pb.StopSessionRequest{
		SessionId: sess.SessionID.String(),
	})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "an allowlisted client_id alone must not grant control to a third caller")
}

// --- Case 5: the on_behalf_of subject is allowed regardless of allowlist ---

// TestSendTurn_OnBehalfOfSubject_AllowedRegardlessOfAllowlist proves branch
// (a) is independent of the allowlist: the session's on_behalf_of subject
// controls it directly even with an EMPTY allowlist (so branch (b) cannot be
// what granted this).
func TestSendTurn_OnBehalfOfSubject_AllowedRegardlessOfAllowlist(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t) // empty allowlist: only branch (a) can fire
	ctx := context.Background()
	sess := createDelegatedSession(t, ctx, store)

	_, err := srv.SendTurn(delegatedUserClaims(), &pb.SendTurnRequest{
		SessionId: sess.SessionID.String(),
		Input:     "hi",
	})
	require.NoError(t, err, "the on_behalf_of subject must be able to control with no allowlist at all (branch a)")
}

// TestStopSession_OnBehalfOfSubject_AllowedRegardlessOfAllowlist is
// TestSendTurn_OnBehalfOfSubject_AllowedRegardlessOfAllowlist's proof for
// StopSession.
func TestStopSession_OnBehalfOfSubject_AllowedRegardlessOfAllowlist(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t) // empty allowlist: only branch (a) can fire
	ctx := context.Background()
	sess := createDelegatedSession(t, ctx, store)

	_, err := srv.StopSession(delegatedUserClaims(), &pb.StopSessionRequest{
		SessionId: sess.SessionID.String(),
	})
	require.NoError(t, err, "the on_behalf_of subject must be able to stop with no allowlist at all (branch a)")
}

// --- Case 6: a non-delegated session behaves exactly as before ---

// TestSendTurn_NonDelegatedSession_Unchanged proves a non-delegated session
// (subject == on_behalf_of) is unaffected by the new allowlist branch: with an
// EMPTY allowlist its subject still controls it, so FR10 only ever *adds* the
// delegated-starter case and never changes the ordinary (subject ==
// on_behalf_of) path.
func TestSendTurn_NonDelegatedSession_Unchanged(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t) // empty allowlist
	ctx := context.Background()
	sess := createSessionWithSubjects(t, ctx, store, fcmServiceSubject, fcmServiceSubject, session.StatusRunning)

	_, err := srv.SendTurn(fcmServiceClaims(fcmClientID), &pb.SendTurnRequest{
		SessionId: sess.SessionID.String(),
		Input:     "hi",
	})
	require.NoError(t, err, "a non-delegated session's subject must still control it, allowlist or not")
}

// TestStopSession_NonDelegatedSession_Unchanged is
// TestSendTurn_NonDelegatedSession_Unchanged's proof for StopSession.
func TestStopSession_NonDelegatedSession_Unchanged(t *testing.T) {
	srv, store := newOnBehalfOfTestServer(t) // empty allowlist
	ctx := context.Background()
	sess := createSessionWithSubjects(t, ctx, store, fcmServiceSubject, fcmServiceSubject, session.StatusRunning)

	_, err := srv.StopSession(fcmServiceClaims(fcmClientID), &pb.StopSessionRequest{
		SessionId: sess.SessionID.String(),
	})
	require.NoError(t, err, "a non-delegated session's subject must still stop it, allowlist or not")
}
