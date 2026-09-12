package tools

// fakeScopeResolver is an in-memory ScopeResolver double for dependent
// tasks' tests (issue #2427's Testing section: "A fake/in-memory
// ScopeResolver is added under mcp/tools' test helpers ... for dependent
// tasks to use").
//
// Unlike fakeSessionServiceClient's panic-if-unset convention
// (fake_client_test.go), lookups default to a not-found error rather than
// panicking: a dependent test wiring up only agentScopes (or only
// sessionScopes) for the one lookup its scenario needs should not have
// to pre-populate the other map just to avoid a panic on an incidental
// call.
//
// A registered *string value of nil is a distinct, successful outcome
// from an unregistered key: it means "this agent/session resolves, but
// carries no scope" (dispatch.go's nil-scope no-op case), never an error.
// An unregistered key is the only case that returns errFakeScopeNotFound.
import (
	"context"
	"fmt"
)

// scopePtr is a small test-only helper for populating fakeScopeResolver's
// map[string]*string fields with a non-nil scope literal.
func scopePtr(scope string) *string { return &scope }

// errFakeScopeNotFound is fakeScopeResolver's own not-found sentinel --
// deliberately not whagent_net/mcpscope.ErrNotFound, since this package
// must never import whagent_net/mcpscope (or anything it pulls in --
// deps_test.go's TestBUILD_NoStoreOrTemporalDependency). A dependent
// test asserting "not found" behavior should match on this error (or the
// tool-level 4xx-shaped error it maps to), not on mcpscope's sentinel.
var errFakeScopeNotFound = fmt.Errorf("fakeScopeResolver: not found")

// fakeScopeResolver implements this package's own ScopeResolver
// interface (scope.go) against two plain maps, keyed by agent id and
// session id respectively. agentCalls/sessionCalls record, in order,
// every id each method was actually invoked with -- issue #2430's Testing
// section requires proving ScopeForAgent is never called for the
// session-keyed tools (send_turn/stop_session/get_session/
// read_transcript), which a plain lookup-miss alone can't distinguish
// from "called but not found".
type fakeScopeResolver struct {
	agentScopes   map[string]*string
	sessionScopes map[string]*string

	agentCalls   []string
	sessionCalls []string
}

var _ ScopeResolver = (*fakeScopeResolver)(nil)

func newFakeScopeResolver() *fakeScopeResolver {
	return &fakeScopeResolver{
		agentScopes:   map[string]*string{},
		sessionScopes: map[string]*string{},
	}
}

func (f *fakeScopeResolver) ScopeForAgent(_ context.Context, agentID string) (*string, error) {
	f.agentCalls = append(f.agentCalls, agentID)
	scope, ok := f.agentScopes[agentID]
	if !ok {
		return nil, fmt.Errorf("%w: agent id %q", errFakeScopeNotFound, agentID)
	}
	return scope, nil
}

func (f *fakeScopeResolver) ScopeForSession(_ context.Context, sessionID string) (*string, error) {
	f.sessionCalls = append(f.sessionCalls, sessionID)
	scope, ok := f.sessionScopes[sessionID]
	if !ok {
		return nil, fmt.Errorf("%w: session id %q", errFakeScopeNotFound, sessionID)
	}
	return scope, nil
}
