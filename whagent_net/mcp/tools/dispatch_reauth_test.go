package tools

// Coverage for issue #2431's Testing section: mid-call
// grpcauth.ErrGrantNeedsReauth handling in dispatch.go's acquireGrantToken.
//
// Red/green (verified by hand, then reverted): every "fails naming scope"
// assertion below was run against a deliberately reverted acquireGrantToken
// (the errors.Is(err, grpcauth.ErrGrantNeedsReauth) branch commented out, so
// the plain scope-naming wrap was returned instead of a reauthRequiredError)
// and failed exactly on the errors.As(err, *reauthRequiredError)/
// "/mcp/consent?scope=" assertions before being reverted back to green --
// proving these tests actually exercise the ErrGrantNeedsReauth-specific
// branch, not just "any error propagates".
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
)

// TestAcquireGrantTokenNeedsReauth_SessionEntryPoint_FailsNamingScope_NoRetry
// is this task's headline scenario for the session-keyed entry point
// (send_turn/stop_session/get_session/read_transcript's shared sequence,
// exercised mid-session exactly as at initial connect): a TokenSource that
// fails with grpcauth.ErrGrantNeedsReauth fails the call with an error
// naming the affected scope, and GrantSource.TokenSource is asked for
// exactly once -- no retry (FR18).
func TestAcquireGrantTokenNeedsReauth_SessionEntryPoint_FailsNamingScope_NoRetry(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-reauth-1"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.sessionScopes["sess-1"] = scopePtr("manmanv2")
	grant := newFakeGrantSourceNeedsReauth()

	outCtx, err := resolveGrantTokenForSession(ctx, scopeResolver, grant, "sess-1")

	require.Error(t, err)
	assert.True(t, errors.Is(err, grpcauth.ErrGrantNeedsReauth), "the sentinel must still be reachable via errors.Is through reauthRequiredError's Unwrap")
	assert.Contains(t, err.Error(), "manmanv2", "the failure must name the affected scope")
	assert.Contains(t, err.Error(), "/mcp/consent?scope=manmanv2", "the failure must point at the standalone per-scope consent route (#2428)")
	assert.True(t, ctx == outCtx, "a failed acquisition must return ctx completely unchanged") //nolint:staticcheck

	require.Len(t, grant.calls, 1, "GrantSource.TokenSource must be asked for exactly one (subject, grant) pair -- no retry")
	assert.Equal(t, fakeTokenSourceCall{subject: "operator-reauth-1", grant: "manmanv2"}, grant.calls[0])
}

// TestAcquireGrantTokenNeedsReauth_AgentEntryPoint_FailsNamingScope proves
// the same detection fires identically at start_session's own dispatch
// sequence (resolveGrantTokenForAgent) -- "at initial connect and
// mid-session alike" (FR18) -- not only at the session-keyed entry point.
func TestAcquireGrantTokenNeedsReauth_AgentEntryPoint_FailsNamingScope(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-reauth-2"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.agentScopes["agent-1"] = scopePtr("audience_score_system")
	grant := newFakeGrantSourceNeedsReauth()

	outCtx, err := resolveGrantTokenForAgent(ctx, scopeResolver, grant, "agent-1")

	require.Error(t, err)
	assert.True(t, errors.Is(err, grpcauth.ErrGrantNeedsReauth))
	assert.Contains(t, err.Error(), "audience_score_system")
	assert.Contains(t, err.Error(), "/mcp/consent?scope=audience_score_system")
	assert.True(t, ctx == outCtx) //nolint:staticcheck

	require.Len(t, grant.calls, 1)
}

// TestNeedsReauth_NoSubstitution_HealthyOtherScopeGrantNeverUsed is the
// Testing section's own example, verbatim: "an operator holding a healthy
// manmanv2 grant plus a needs_reauth audience_score_system grant, calling
// into an audience_score_system session, fails -- the manmanv2 grant is
// never used." grant.tokenFunc below would happily mint a real token if
// ever asked for "manmanv2" -- proving the failure is specifically about
// audience_score_system being needs_reauth, not merely "grant always
// fails in this test".
func TestNeedsReauth_NoSubstitution_HealthyOtherScopeGrantNeverUsed(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-reauth-3"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.sessionScopes["sess-ass"] = scopePtr("audience_score_system")

	grant := &fakeGrantSource{
		tokenFunc: func(_ context.Context, _, grantKey string) (*oauth2.Token, error) {
			if grantKey == "manmanv2" {
				return &oauth2.Token{AccessToken: "manmanv2-token-must-never-be-minted-here"}, nil
			}
			return nil, fmt.Errorf("grpcauth: grant %q rejected by keycloak: %w", grantKey, grpcauth.ErrGrantNeedsReauth)
		},
	}

	outCtx, err := resolveGrantTokenForSession(ctx, scopeResolver, grant, "sess-ass")

	require.Error(t, err)
	assert.True(t, errors.Is(err, grpcauth.ErrGrantNeedsReauth))
	assert.Contains(t, err.Error(), "audience_score_system")
	assert.NotContains(t, err.Error(), "manmanv2-token-must-never-be-minted-here")
	assert.True(t, ctx == outCtx) //nolint:staticcheck

	require.Len(t, grant.calls, 1, "exactly one scope must ever be requested for this call")
	assert.Equal(t, "audience_score_system", grant.calls[0].grant, "the healthy manmanv2 grant must never be requested as a substitute for the needs_reauth scope actually resolved")
}

// TestNeedsReauth_NoFallbackToManualTokenPath proves a resolved-identity
// caller (the browser-OAuth2 path) that hits ErrGrantNeedsReauth never
// falls back to forwarding whatever raw bearer token happens to already be
// on ctx (the manual-token path's own mechanism, dispatch.go's file doc
// comment) -- the call fails outright through the registered tool, and the
// underlying pb.SessionServiceClient (which would receive that ctx, manual
// token and all, if dispatch-time acquisition were ever skipped) is never
// invoked at all.
func TestNeedsReauth_NoFallbackToManualTokenPath(t *testing.T) {
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-reauth-4"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)
	// A raw bearer token is already on ctx, exactly as the manual-token
	// path (../server/auth.go's AuthMiddleware) would place one -- present
	// here specifically to prove acquireGrantToken's failure is not
	// papered over by silently falling back to it.
	ctx = grpcauth.WithUserToken(ctx, "manual-token-should-never-reach-api")

	scopeResolver := newFakeScopeResolver()
	scopeResolver.sessionScopes["sess-1"] = scopePtr("manmanv2")
	grant := newFakeGrantSourceNeedsReauth()

	fc := &fakeSessionServiceClient{}
	tool := &getSessionTool{client: fc, scopeResolver: scopeResolver, grant: grant}

	_, out, err := tool.call(ctx, nil, GetSessionInput{SessionID: "sess-1"})

	require.Error(t, err)
	assert.True(t, errors.Is(err, grpcauth.ErrGrantNeedsReauth))
	assert.Equal(t, GetSessionOutput{}, out)
	assert.Empty(t, fc.calls, "the underlying RPC -- and therefore the manual token sitting on ctx -- must never be reached once ErrGrantNeedsReauth has already failed the call")
}

// TestNeedsReauth_EveryToolEntryPoint_ProducesScopeNamingError is the
// Testing section's "start_session and each existing-session handler each
// produce the same scope-naming error" bullet, run through every
// registered tool's own call() method (not dispatch.go's helpers
// directly), proving the detection is uniform across every tool dispatch
// path, not just the two resolveGrantTokenForXxx entry points in
// isolation.
func TestNeedsReauth_EveryToolEntryPoint_ProducesScopeNamingError(t *testing.T) {
	const scope = "manmanv2"
	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-reauth-5"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	newResolver := func() *fakeScopeResolver {
		r := newFakeScopeResolver()
		r.agentScopes["agent-1"] = scopePtr(scope)
		r.sessionScopes["sess-1"] = scopePtr(scope)
		return r
	}

	cases := []struct {
		name string
		call func(fc *fakeSessionServiceClient, dr ScopeResolver, gr GrantSource) error
	}{
		{"start_session", func(fc *fakeSessionServiceClient, dr ScopeResolver, gr GrantSource) error {
			tool := &startSessionTool{client: fc, scopeResolver: dr, grant: gr}
			_, _, err := tool.call(ctx, nil, StartSessionInput{AgentID: "agent-1"})
			return err
		}},
		{"send_turn", func(fc *fakeSessionServiceClient, dr ScopeResolver, gr GrantSource) error {
			tool := &sendTurnTool{client: fc, scopeResolver: dr, grant: gr}
			_, _, err := tool.call(ctx, nil, SendTurnInput{SessionID: "sess-1", Input: "x"})
			return err
		}},
		{"stop_session", func(fc *fakeSessionServiceClient, dr ScopeResolver, gr GrantSource) error {
			tool := &stopSessionTool{client: fc, scopeResolver: dr, grant: gr}
			_, _, err := tool.call(ctx, nil, StopSessionInput{SessionID: "sess-1"})
			return err
		}},
		{"get_session", func(fc *fakeSessionServiceClient, dr ScopeResolver, gr GrantSource) error {
			tool := &getSessionTool{client: fc, scopeResolver: dr, grant: gr}
			_, _, err := tool.call(ctx, nil, GetSessionInput{SessionID: "sess-1"})
			return err
		}},
		{"read_transcript", func(fc *fakeSessionServiceClient, dr ScopeResolver, gr GrantSource) error {
			tool := &readTranscriptTool{client: fc, scopeResolver: dr, grant: gr}
			_, _, err := tool.call(ctx, nil, ReadTranscriptInput{SessionID: "sess-1"})
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeSessionServiceClient{}
			grant := newFakeGrantSourceNeedsReauth()

			err := tc.call(fc, newResolver(), grant)

			require.Error(t, err)
			assert.True(t, errors.Is(err, grpcauth.ErrGrantNeedsReauth))
			assert.Contains(t, err.Error(), scope)
			assert.Contains(t, err.Error(), "/mcp/consent?scope="+scope)
			assert.Empty(t, fc.calls, "the underlying RPC must never be attempted once dispatch-time acquisition has already failed with ErrGrantNeedsReauth")
			require.Len(t, grant.calls, 1, "no retry")
		})
	}
}

// TestNeedsReauth_LogsAtWarningWithScopeField_NeverLogsSecret proves the
// mid-call reauth path logs at WARNING (not ERROR: the system is behaving
// correctly, a human just needs to act -- AGENTS.md's logging levels
// table) with the scope in a structured field, and never logs anything
// from cause -- specifically never a refresh token or client secret, even
// when the underlying error happens to carry one.
//
// Captures the actual process-wide slog default (dispatch.go's own doc
// comment on why acquireGrantToken calls logging.Get fresh at the log
// site, rather than caching a package-level *slog.Logger, exists
// specifically so this is possible).
func TestNeedsReauth_LogsAtWarningWithScopeField_NeverLogsSecret(t *testing.T) {
	prevDefault := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prevDefault) })

	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))

	identity := mcpidentity.Identity{Iss: "https://keycloak.example.test/realms/whagent", Sub: "operator-reauth-6"}
	ctx := mcpidentity.ContextWithIdentity(context.Background(), identity)

	scopeResolver := newFakeScopeResolver()
	scopeResolver.sessionScopes["sess-1"] = scopePtr("manmanv2")

	const secretRefreshToken = "super-secret-refresh-token-must-never-be-logged"
	grant := &fakeGrantSource{
		tokenFunc: func(context.Context, string, string) (*oauth2.Token, error) {
			// The underlying failure carries a value that must never
			// reach the log line -- acquireGrantToken's WARN call only
			// ever passes scope/subject, never cause itself.
			return nil, fmt.Errorf("grpcauth: grant %q rejected by keycloak (leaked refresh token %s): %w", "manmanv2", secretRefreshToken, grpcauth.ErrGrantNeedsReauth)
		},
	}

	_, err := resolveGrantTokenForSession(ctx, scopeResolver, grant, "sess-1")
	require.Error(t, err)

	var record map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record), "expected exactly one JSON log line: %s", buf.String())

	assert.Equal(t, "WARN", record["level"], "a genuine deviation requiring human action must log at WARNING, not INFO or ERROR")
	assert.Equal(t, "manmanv2", record["scope"])
	assert.Equal(t, "operator-reauth-6", record["subject"])
	assert.NotContains(t, buf.String(), secretRefreshToken, "the refresh token must never reach the log line")
}
