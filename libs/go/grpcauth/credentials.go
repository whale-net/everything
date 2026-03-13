package grpcauth

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/grpc"
)

// userTokenKey is the context key for per-request user tokens
type userTokenKey struct{}

// WithUserToken stores a user token in the context for forwarding to gRPC calls
func WithUserToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, userTokenKey{}, token)
}

// NewServiceAccountDialOption creates a gRPC DialOption for service account (M2M) auth.
// In AuthModeNone, returns a no-op option. In AuthModeOIDC, uses client credentials
// token source with automatic caching and refresh.
func NewServiceAccountDialOption(config ClientConfig) (grpc.DialOption, error) {
	if config.Mode == AuthModeNone {
		return grpc.WithPerRPCCredentials(&noopCredentials{}), nil
	}

	if config.TokenURL == "" {
		return nil, fmt.Errorf("TokenURL is required for OIDC service account auth")
	}
	if config.ClientID == "" {
		return nil, fmt.Errorf("ClientID is required for OIDC service account auth")
	}

	ccConfig := &clientcredentials.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		TokenURL:     config.TokenURL,
	}
	ts := ccConfig.TokenSource(context.Background())

	return grpc.WithPerRPCCredentials(&serviceAccountCreds{
		ts:                       ts,
		requireTransportSecurity: config.RequireTransportSecurity,
	}), nil
}

// NewUserTokenDialOption creates a gRPC DialOption that forwards the per-request user
// token stored in the call context via WithUserToken.
func NewUserTokenDialOption(mode AuthMode) grpc.DialOption {
	return grpc.WithPerRPCCredentials(&userTokenCreds{mode: mode})
}

// noopCredentials sends empty metadata; used in dev mode
type noopCredentials struct{}

func (n *noopCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (n *noopCredentials) RequireTransportSecurity() bool {
	return false
}

// serviceAccountCreds implements PerRPCCredentials using a cached token source
type serviceAccountCreds struct {
	ts                       oauth2.TokenSource
	requireTransportSecurity bool
}

func (s *serviceAccountCreds) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	token, err := s.ts.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to get service account token: %w", err)
	}
	return map[string]string{
		"authorization": "Bearer " + token.AccessToken,
	}, nil
}

func (s *serviceAccountCreds) RequireTransportSecurity() bool {
	return s.requireTransportSecurity
}

// userTokenCreds implements PerRPCCredentials by reading the token from the call context
type userTokenCreds struct {
	mode AuthMode
}

func (u *userTokenCreds) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	if u.mode == AuthModeNone {
		return map[string]string{}, nil
	}
	token, ok := ctx.Value(userTokenKey{}).(string)
	if !ok || token == "" {
		return nil, fmt.Errorf("no user token in context")
	}
	return map[string]string{
		"authorization": "Bearer " + token,
	}, nil
}

func (u *userTokenCreds) RequireTransportSecurity() bool {
	return false // internal cluster; transport security handled separately
}
