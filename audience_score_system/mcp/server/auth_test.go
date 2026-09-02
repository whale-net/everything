package server

// Pure-Go coverage for PersonMiddleware (auth.go) against fakePersonStore
// (fakes_test.go) -- exercises every rejection path (no TokenInfo, nil
// TokenInfo, empty UserID, unparseable UserID, unresolvable Person) plus
// the success path (Person placed on ctx, next invoked exactly once), all
// without a real HTTP request or database. server_integration_test.go
// separately proves the HTTP half (auth.RequireBearerToken -> TokenVerifier
// -> mcp_credential) against a real Postgres-backed CredentialStore.
import (
	"context"
	"testing"

	"github.com/google/uuid"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/audience_score_system/store"
)

// requestWithExtra builds the minimal mcp.Request PersonMiddleware reads:
// only GetExtra() matters to it, so Session/Params are left zero.
func requestWithExtra(extra *mcp.RequestExtra) mcp.Request {
	return &mcp.ServerRequest[*mcp.CallToolParams]{Extra: extra}
}

func TestPersonMiddleware_RejectsWhenUnauthenticated(t *testing.T) {
	ctx := context.Background()
	personID := uuid.New()
	persons := fakePersonStore{byID: map[uuid.UUID]store.Person{
		personID: {ID: personID, Email: "a@example.com", DisplayName: "A"},
	}}

	cases := []struct {
		name string
		req  mcp.Request
	}{
		{"no Extra at all", requestWithExtra(nil)},
		{"Extra with nil TokenInfo", requestWithExtra(&mcp.RequestExtra{})},
		{"TokenInfo with empty UserID", requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{UserID: ""}})},
		{"TokenInfo with unparseable UserID", requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{UserID: "not-a-uuid"}})},
		{"TokenInfo resolves to a Person that doesn't exist", requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{UserID: uuid.NewString()}})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var nextCalled bool
			next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				nextCalled = true
				return nil, nil
			})

			_, err := PersonMiddleware(persons)(next)(ctx, "tools/call", tc.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unauthenticated")
			assert.False(t, nextCalled, "next must never run when caller identity does not resolve")
		})
	}
}

func TestPersonMiddleware_ResolvesPersonAndCallsNext(t *testing.T) {
	ctx := context.Background()
	personID := uuid.New()
	want := store.Person{ID: personID, Email: "a@example.com", DisplayName: "A"}
	persons := fakePersonStore{byID: map[uuid.UUID]store.Person{personID: want}}

	var gotPerson *store.Person
	var nextCalled bool
	next := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		nextCalled = true
		gotPerson = PersonFromContext(ctx)
		return nil, nil
	})

	req := requestWithExtra(&mcp.RequestExtra{TokenInfo: &sdkauth.TokenInfo{UserID: personID.String()}})
	_, err := PersonMiddleware(persons)(next)(ctx, "tools/call", req)
	require.NoError(t, err)
	assert.True(t, nextCalled, "next must run once caller identity resolves")
	require.NotNil(t, gotPerson, "PersonFromContext must see the resolved Person inside next")
	assert.Equal(t, want, *gotPerson)
}

func TestPersonFromContext_NilWhenNothingResolved(t *testing.T) {
	assert.Nil(t, PersonFromContext(context.Background()), "PersonFromContext must return nil outside a request PersonMiddleware handled")
}
