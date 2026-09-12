// Unit tests for CreatePointerArtifactHandler (pointer.go, issue #2496's
// Testing section). fakePointerProductStore/fakePointerScopeStore/
// fakePointerArtifactStore/fakePointerForgeClient stand in for
// krill/store's interfaces and krill/forge.Client, so none of this needs
// Postgres or a real GitHub call (krill/store/pointer_integration_test.go
// and krill/forge/github_test.go cover those real-dependency halves).
package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// fakePointerProductStore backs pointer_test.go: unlike fake_entity_store_
// test.go's fakeProductStore (whose GetCurrentByID always returns
// ErrNotFound, fine for the create handlers that never read a Product
// back), CreatePointerArtifactHandler reads the target Product back before
// minting an issue, so this fake needs a configurable success path too.
type fakePointerProductStore struct {
	product  store.Product
	getErr   error
	gotGetID uuid.UUID
}

func (f *fakePointerProductStore) Create(ctx context.Context, scopeID uuid.UUID, name, vision string) (store.Product, error) {
	return store.Product{}, errors.New("not used by pointer_test.go")
}

func (f *fakePointerProductStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (store.Product, error) {
	f.gotGetID = id
	if f.getErr != nil {
		return store.Product{}, f.getErr
	}
	return f.product, nil
}

func (f *fakePointerProductStore) ListCurrentByScope(ctx context.Context, scopeID uuid.UUID) ([]store.Product, error) {
	return nil, nil
}

// fakePointerScopeStore backs pointer_test.go.
type fakePointerScopeStore struct {
	scope  store.Scope
	getErr error
}

func (f *fakePointerScopeStore) GetByID(ctx context.Context, id uuid.UUID) (store.Scope, error) {
	if f.getErr != nil {
		return store.Scope{}, f.getErr
	}
	return f.scope, nil
}

// fakePointerArtifactStore backs pointer_test.go.
type fakePointerArtifactStore struct {
	createErr error

	gotScopeID     uuid.UUID
	gotProductID   uuid.UUID
	gotIssueNumber int
	gotIssueURL    string
	gotActing      store.Subject
	gotOnBehalfOf  store.Subject
}

func (f *fakePointerArtifactStore) Create(ctx context.Context, scopeID, productID uuid.UUID, issueNumber int, issueURL string, acting, onBehalfOf store.Subject) (store.PointerArtifact, error) {
	f.gotScopeID, f.gotProductID = scopeID, productID
	f.gotIssueNumber, f.gotIssueURL = issueNumber, issueURL
	f.gotActing, f.gotOnBehalfOf = acting, onBehalfOf
	if f.createErr != nil {
		return store.PointerArtifact{}, f.createErr
	}
	return store.PointerArtifact{
		ID: uuid.New(), ScopeID: scopeID, ProductID: productID,
		Kind: "github_issue", IssueNumber: issueNumber, IssueURL: issueURL,
		CreatedByActing: acting, CreatedByOnBehalfOf: onBehalfOf,
	}, nil
}

func (f *fakePointerArtifactStore) ListByProduct(ctx context.Context, productID uuid.UUID) ([]store.PointerArtifact, error) {
	return nil, nil
}

// fakePointerForgeClient backs pointer_test.go -- forge.Client, faked so
// no test here ever makes a real GitHub call.
type fakePointerForgeClient struct {
	createErr error
	called    bool

	gotRepoFullName string
	gotTitle        string
	gotBody         string
}

func (f *fakePointerForgeClient) CreateIssue(ctx context.Context, repoFullName, title, body string) (int, string, error) {
	f.called = true
	f.gotRepoFullName, f.gotTitle, f.gotBody = repoFullName, title, body
	if f.createErr != nil {
		return 0, "", f.createErr
	}
	return 99, "https://github.com/whale-net/everything/issues/99", nil
}

// TestCreatePointerArtifactHandler_Success proves FR20: the handler reads
// the target Product back, mints an issue against the caller's scope's
// forge coordinates, and records the artifact with both LB4 subjects.
func TestCreatePointerArtifactHandler_Success(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	productID := uuid.New()

	products := &fakePointerProductStore{product: store.Product{ID: productID, ScopeID: scopeID, Name: "Krill"}}
	scopes := &fakePointerScopeStore{scope: store.Scope{ID: scopeID, RepoFullName: "whale-net/everything", DefaultBranch: "main"}}
	artifacts := &fakePointerArtifactStore{}
	forgeClient := &fakePointerForgeClient{}

	rec := doGatedRequest(t, handlers.CreatePointerArtifactHandler(products, scopes, artifacts, forgeClient), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	assert.True(t, forgeClient.called, "the forge client must be called to mint the pointer issue")
	assert.Equal(t, "whale-net/everything", forgeClient.gotRepoFullName, "the issue must be created against the caller's scope's own repo coordinates")
	assert.Contains(t, forgeClient.gotTitle, "Krill", "the issue title must point back at the Product")

	assert.Equal(t, scopeID, artifacts.gotScopeID)
	assert.Equal(t, productID, artifacts.gotProductID)
	assert.Equal(t, 99, artifacts.gotIssueNumber)
	assert.Equal(t, "https://github.com/whale-net/everything/issues/99", artifacts.gotIssueURL)
}

// TestCreatePointerArtifactHandler_ActingDiffersFromOnBehalfOf_
// BothRecordedDistinctly proves both LB4 subjects are attributed to the
// created pointer_artifact row, not collapsed into one -- mirrors
// product_test.go's own version of this proof for the entity-create path.
func TestCreatePointerArtifactHandler_ActingDiffersFromOnBehalfOf_BothRecordedDistinctly(t *testing.T) {
	sessions := newFakeSessionStore()
	scopeID := uuid.New()
	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-caller", Kind: store.SubjectKindService}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "human-owner", Kind: store.SubjectKindHuman}
	sessionID := mustInitSession(t, sessions, scopeID, acting, onBehalfOf)

	productID := uuid.New()
	products := &fakePointerProductStore{product: store.Product{ID: productID, ScopeID: scopeID, Name: "Krill"}}
	scopes := &fakePointerScopeStore{scope: store.Scope{ID: scopeID, RepoFullName: "whale-net/everything", DefaultBranch: "main"}}
	artifacts := &fakePointerArtifactStore{}
	forgeClient := &fakePointerForgeClient{}

	rec := doGatedRequest(t, handlers.CreatePointerArtifactHandler(products, scopes, artifacts, forgeClient), sessions, sessionID.String(),
		`{"product_id": "`+productID.String()+`"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, acting, artifacts.gotActing, "the acting subject must be recorded on the pointer artifact")
	assert.Equal(t, onBehalfOf, artifacts.gotOnBehalfOf, "the on-behalf-of subject must be recorded on the pointer artifact")
	assert.NotEqual(t, artifacts.gotActing, artifacts.gotOnBehalfOf, "a pointer-issue create must never collapse acting and on_behalf_of into one subject")
}

// TestCreatePointerArtifactHandler_NoSessionID_Rejected proves FR3's write
// gate sits in front of this handler too -- a caller with no session id is
// rejected before the forge client is ever called.
func TestCreatePointerArtifactHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	productID := uuid.New()

	products := &fakePointerProductStore{product: store.Product{ID: productID, Name: "Krill"}}
	scopes := &fakePointerScopeStore{}
	artifacts := &fakePointerArtifactStore{}
	forgeClient := &fakePointerForgeClient{}

	rec := doGatedRequest(t, handlers.CreatePointerArtifactHandler(products, scopes, artifacts, forgeClient), sessions, "",
		`{"product_id": "`+productID.String()+`"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, forgeClient.called, "no session must mean no GitHub issue is ever minted")
	assert.Equal(t, uuid.Nil, artifacts.gotProductID)
}

// TestCreatePointerArtifactHandler_CrossScopeProduct_RejectedBeforeForgeCall
// proves the order of operations pointer.go's doc comment calls out: a
// cross-scope product_id is rejected with 400 before the forge client is
// ever called, so a caller error never mints a spurious GitHub issue.
func TestCreatePointerArtifactHandler_CrossScopeProduct_RejectedBeforeForgeCall(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	productID := uuid.New()

	// The Product belongs to a different scope than the caller's session.
	products := &fakePointerProductStore{product: store.Product{ID: productID, ScopeID: uuid.New(), Name: "Krill"}}
	scopes := &fakePointerScopeStore{}
	artifacts := &fakePointerArtifactStore{}
	forgeClient := &fakePointerForgeClient{}

	rec := doGatedRequest(t, handlers.CreatePointerArtifactHandler(products, scopes, artifacts, forgeClient), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.False(t, forgeClient.called, "a cross-scope product_id must be rejected before the forge client is ever called")
	assert.Equal(t, uuid.Nil, artifacts.gotProductID)
}

// TestCreatePointerArtifactHandler_UnknownProduct_Returns400 proves an
// unknown product_id is rejected the same way a cross-scope one is (LB2
// parentage, mirrors every other M1 create handler).
func TestCreatePointerArtifactHandler_UnknownProduct_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)

	products := &fakePointerProductStore{getErr: store.ErrNotFound}
	scopes := &fakePointerScopeStore{}
	artifacts := &fakePointerArtifactStore{}
	forgeClient := &fakePointerForgeClient{}

	rec := doGatedRequest(t, handlers.CreatePointerArtifactHandler(products, scopes, artifacts, forgeClient), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.False(t, forgeClient.called)
}

// TestCreatePointerArtifactHandler_ForgeError_Returns500 proves a GitHub
// API failure surfaces as a 500 and never reaches PointerArtifactStore.
func TestCreatePointerArtifactHandler_ForgeError_Returns500(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	productID := uuid.New()

	products := &fakePointerProductStore{product: store.Product{ID: productID, ScopeID: scopeID, Name: "Krill"}}
	scopes := &fakePointerScopeStore{scope: store.Scope{ID: scopeID, RepoFullName: "whale-net/everything", DefaultBranch: "main"}}
	artifacts := &fakePointerArtifactStore{}
	forgeClient := &fakePointerForgeClient{createErr: errors.New("github is down")}

	rec := doGatedRequest(t, handlers.CreatePointerArtifactHandler(products, scopes, artifacts, forgeClient), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`"}`)

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, artifacts.gotProductID, "a forge failure must never reach PointerArtifactStore.Create")
}

// TestCreatePointerArtifactHandler_DuplicateProduct_Returns409 proves
// writeStoreError maps pointer_artifact_product_idx's unique-constraint
// violation onto a 409, the same mapping every other create handler uses.
func TestCreatePointerArtifactHandler_DuplicateProduct_Returns409(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	productID := uuid.New()

	products := &fakePointerProductStore{product: store.Product{ID: productID, ScopeID: scopeID, Name: "Krill"}}
	scopes := &fakePointerScopeStore{scope: store.Scope{ID: scopeID, RepoFullName: "whale-net/everything", DefaultBranch: "main"}}
	artifacts := &fakePointerArtifactStore{createErr: uniqueViolationErr}
	forgeClient := &fakePointerForgeClient{}

	rec := doGatedRequest(t, handlers.CreatePointerArtifactHandler(products, scopes, artifacts, forgeClient), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}
