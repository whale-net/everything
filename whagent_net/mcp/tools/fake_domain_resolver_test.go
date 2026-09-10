package tools

// fakeDomainResolver is an in-memory DomainResolver double for dependent
// tasks' tests (issue #2427's Testing section: "A fake/in-memory
// DomainResolver is added under mcp/tools' test helpers ... for dependent
// tasks to use"). No tool handler in this package calls DomainResolver
// yet (see domain.go's doc comment), so nothing in this package's own
// test suite constructs one today -- it exists purely as shared
// infrastructure for the dispatch-time-rewiring task that depends on this
// one.
//
// Unlike fakeSessionServiceClient's panic-if-unset convention
// (fake_client_test.go), lookups default to a not-found error rather than
// panicking: a dependent test wiring up only agentDomains (or only
// sessionDomains) for the one lookup its scenario needs should not have
// to pre-populate the other map just to avoid a panic on an incidental
// call.
import (
	"context"
	"fmt"
)

// errFakeDomainNotFound is fakeDomainResolver's own not-found sentinel --
// deliberately not whagent_net/mcpdomain.ErrNotFound, since this package
// must never import whagent_net/mcpdomain (or anything it pulls in --
// deps_test.go's TestBUILD_NoStoreOrTemporalDependency). A dependent
// test asserting "not found" behavior should match on this error (or the
// tool-level 4xx-shaped error it maps to), not on mcpdomain's sentinel.
var errFakeDomainNotFound = fmt.Errorf("fakeDomainResolver: not found")

// fakeDomainResolver implements this package's own DomainResolver
// interface (domain.go) against two plain maps, keyed by agent id and
// session id respectively.
type fakeDomainResolver struct {
	agentDomains   map[string]string
	sessionDomains map[string]string
}

var _ DomainResolver = (*fakeDomainResolver)(nil)

func newFakeDomainResolver() *fakeDomainResolver {
	return &fakeDomainResolver{
		agentDomains:   map[string]string{},
		sessionDomains: map[string]string{},
	}
}

func (f *fakeDomainResolver) DomainForAgent(_ context.Context, agentID string) (string, error) {
	domain, ok := f.agentDomains[agentID]
	if !ok {
		return "", fmt.Errorf("%w: agent id %q", errFakeDomainNotFound, agentID)
	}
	return domain, nil
}

func (f *fakeDomainResolver) DomainForSession(_ context.Context, sessionID string) (string, error) {
	domain, ok := f.sessionDomains[sessionID]
	if !ok {
		return "", fmt.Errorf("%w: session id %q", errFakeDomainNotFound, sessionID)
	}
	return domain, nil
}
