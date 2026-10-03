package grantflow

import (
	"context"
	"errors"

	"github.com/whale-net/everything/libs/go/grpcauth"
)

// ErrUnauthenticated means the caller must restart OAuth (no usable grant).
var ErrUnauthenticated = errors.New("grantflow: no usable grant for caller")

// GrantSource yields the token source for one (subject, grant);
// *grpcauth.DelegatedGrantSource implements it.
type GrantSource interface {
	TokenSource(subject, grant string) grpcauth.GrantTokenSource
}

// Exchanger is the MCP half: it turns the identity of a verified credential
// (a Keycloak `sub`) into that user's fresh access token and verified claims.
type Exchanger struct {
	Source   GrantSource
	Grant    string
	Verifier grpcauth.TokenVerifier
}

// Exchange returns a user access token and its claims for subject.
func (e Exchanger) Exchange(ctx context.Context, subject string) (string, *grpcauth.Claims, error) {
	tok, err := e.Source.TokenSource(subject, e.Grant).Token(ctx)
	if err != nil {
		return "", nil, errors.Join(ErrUnauthenticated, err)
	}
	claims, err := e.Verifier.Verify(ctx, tok.AccessToken)
	if err != nil || claims == nil || claims.Subject != subject {
		return "", nil, ErrUnauthenticated
	}
	return tok.AccessToken, claims, nil
}
