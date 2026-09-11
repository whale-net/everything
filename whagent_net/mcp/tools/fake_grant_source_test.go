package tools

// fakeGrantSource/fakeGrantTokenSource are GrantSource/grpcauth.GrantTokenSource
// doubles for dependent tests (issue #2430's Testing section: dispatch-time
// token acquisition proven against a fake, never a real Keycloak token
// endpoint). fakeGrantSource.TokenSource records every (subject, grant)
// pair it was asked for, in order, so a test can assert exactly which pair
// a handler resolved before ever calling Token -- e.g. that a mismatched
// or absent grant never substitutes another domain's (NFR2).
//
// Mirrors fakeSessionServiceClient's panic-if-unset convention
// (fake_client_test.go): a handler that calls Token without a test having
// set tokenFunc fails loudly, not with a misleading zero value.
import (
	"context"
	"fmt"

	"golang.org/x/oauth2"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

// fakeTokenSourceCall records one TokenSource(subject, grant) call.
type fakeTokenSourceCall struct {
	subject string
	grant   string
}

// fakeGrantSource implements this package's own GrantSource interface
// (grant.go). tokenFunc, if set, backs every fakeGrantTokenSource.Token
// call TokenSource's returned value produces; nil means "must not be
// called" (mirrors fakeSessionServiceClient).
type fakeGrantSource struct {
	tokenFunc func(ctx context.Context, subject, grant string) (*oauth2.Token, error)

	calls []fakeTokenSourceCall
}

var _ GrantSource = (*fakeGrantSource)(nil)

func (f *fakeGrantSource) TokenSource(subject, grant string) grpcauth.GrantTokenSource {
	f.calls = append(f.calls, fakeTokenSourceCall{subject: subject, grant: grant})
	return &fakeGrantTokenSource{source: f, subject: subject, grant: grant}
}

// fakeGrantTokenSource implements grpcauth.GrantTokenSource, bound to the
// exact (subject, grant) pair TokenSource was called with.
type fakeGrantTokenSource struct {
	source  *fakeGrantSource
	subject string
	grant   string
}

var _ grpcauth.GrantTokenSource = (*fakeGrantTokenSource)(nil)

func (t *fakeGrantTokenSource) Token(ctx context.Context) (*oauth2.Token, error) {
	if t.source.tokenFunc == nil {
		panic(fmt.Sprintf("fakeGrantSource: Token called for (subject=%q, grant=%q) but no tokenFunc set", t.subject, t.grant))
	}
	return t.source.tokenFunc(ctx, t.subject, t.grant)
}
