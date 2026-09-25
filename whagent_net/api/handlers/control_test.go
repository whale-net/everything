package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/whagent_net/session"
)

// This file is the non-containerised half of canControl's coverage
// (FR10/FR13/NFR1): canControl is the single gate SendTurn and StopSession
// both consult, and it is a pure function of the session row, the caller's
// subject and the current call's client_id against the server's allowlist --
// no store, no Temporal, no network. These cases pin both branches, the
// fail-closed empty allowlist, and the per-call re-read, without Postgres.
// control_on_behalf_of_integration_test.go proves the same branches end to
// over a real server.

const (
	testIssuer    = "https://test-issuer"
	fcmClientID   = "fcm-client"
	thirdClientID = "third-party-client"
)

// fcmSubject is the identity that STARTS a delegated session (subject column);
// delegatedUserSubject is the identity it runs AS (on_behalf_of column). The
// two differ in sub, so a passing case proves the intended branch fired
// rather than both columns happening to match.
var (
	fcmSubject         = session.Subject{Iss: testIssuer, Sub: "fcm-service", Kind: session.SubjectKindService}
	delegatedUserSub   = session.Subject{Iss: testIssuer, Sub: "slack-user-1", Kind: session.SubjectKindHuman}
	thirdCallerSubject = session.Subject{Iss: testIssuer, Sub: "third-party", Kind: session.SubjectKindService}
)

// newControlServer builds a server carrying only the allowlist, which is the
// single field canControl reads.
func newControlServer(allowedClientIDs ...string) *SessionServer {
	return &SessionServer{onBehalfOfAllowlist: buildAllowlist(allowedClientIDs...)}
}

func buildAllowlist(clientIDs ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(clientIDs))
	for _, id := range clientIDs {
		out[id] = struct{}{}
	}
	return out
}

// delegatedSession is the canonical FR10 shape: a session started by fcm that
// runs as a Slack user.
func delegatedSession() *session.Session {
	return &session.Session{Subject: fcmSubject, OnBehalfOf: delegatedUserSub, Status: session.StatusRunning}
}

func TestCanControl_AllowlistedStarter_ControlsDelegatedSession(t *testing.T) {
	srv := newControlServer(fcmClientID)

	assert.True(t, srv.canControl(delegatedSession(), fcmSubject, fcmClientID),
		"the allowlisted client that started a delegated session must be able to control it (branch b)")
}

func TestCanControl_StarterNotAllowlisted_Denied(t *testing.T) {
	srv := newControlServer("some-other-client")

	assert.False(t, srv.canControl(delegatedSession(), fcmSubject, fcmClientID),
		"branch (b) must not fire when the caller's own client_id is absent from the allowlist")
}

func TestCanControl_EmptyAllowlist_FailsClosed(t *testing.T) {
	srv := newControlServer()

	assert.False(t, srv.canControl(delegatedSession(), fcmSubject, fcmClientID),
		"an empty allowlist must never grant branch (b) (NFR1)")
}

func TestCanControl_NilAllowlist_FailsClosed(t *testing.T) {
	srv := &SessionServer{}

	assert.False(t, srv.canControl(delegatedSession(), fcmSubject, fcmClientID),
		"an unset (nil) allowlist must fail closed exactly like an empty one (NFR1)")
}

func TestCanControl_ThirdCaller_Allowlisted_StillDenied(t *testing.T) {
	srv := newControlServer(fcmClientID)

	assert.False(t, srv.canControl(delegatedSession(), thirdCallerSubject, fcmClientID),
		"an allowlisted client_id alone must not grant control to a caller who is not the session's subject")
}

func TestCanControl_OnBehalfOfSubject_AllowedRegardlessOfAllowlist(t *testing.T) {
	srv := newControlServer() // empty: only branch (a) can fire here

	assert.True(t, srv.canControl(delegatedSession(), delegatedUserSub, "not-on-the-allowlist"),
		"branch (a) is independent of the allowlist")
}

func TestCanControl_NonDelegatedSession_Unchanged(t *testing.T) {
	srv := newControlServer() // empty allowlist
	sess := &session.Session{Subject: fcmSubject, OnBehalfOf: fcmSubject, Status: session.StatusRunning}

	assert.True(t, srv.canControl(sess, fcmSubject, fcmClientID),
		"a non-delegated session's subject still controls it, allowlist or not")
}

func TestCanControl_ClientIDReadPerCall_NotCached(t *testing.T) {
	srv := newControlServer(fcmClientID)
	sess := delegatedSession()

	assert.True(t, srv.canControl(sess, fcmSubject, fcmClientID), "precondition: allowed while allowlisted")

	// Mutate the live allowlist in place: the very next call must be denied,
	// proving canControl re-reads s.onBehalfOfAllowlist per call rather than
	// snapshotting it (or anything derived from it) at construction.
	delete(srv.onBehalfOfAllowlist, fcmClientID)

	assert.False(t, srv.canControl(sess, fcmSubject, fcmClientID),
		"removing the client from the allowlist must take effect on the very next call")
}

func TestCanControl_KindIsNotIdentity(t *testing.T) {
	srv := newControlServer(fcmClientID)
	// Same (iss, sub) as the on_behalf_of subject but a different Kind: Kind
	// is metadata about the identity, not part of it, so branch (a) fires.
	caller := delegatedUserSub
	caller.Kind = session.SubjectKindService

	assert.True(t, srv.canControl(delegatedSession(), caller, "any-client"),
		"subjectsEqual compares (iss, sub) only, so Kind must not change the decision")
}
