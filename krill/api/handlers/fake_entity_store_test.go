// fake{Product,FeatureSet,Feature,Requirement,Decision}Store are in-memory
// store.*Store implementations shared by product_test.go/featureset_test.go/
// feature_test.go/requirement_test.go/decision_test.go: the create handlers
// (product.go/featureset.go/feature.go/requirement.go/decision.go, issue
// #2490) depend only on these interfaces, so handler-level tests never need
// a real Postgres (that is krill/store/entities_integration_test.go's job).
// Each fake records the arguments its Create saw (for asserting scope_id
// and parent-id pass-through, LB1/LB2) and can be told to fail with a fixed
// error, so a test can drive writeStoreError's (types.go) three-way mapping
// without a database at all.
package handlers_test

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// fakeProductStore backs product_test.go.
type fakeProductStore struct {
	createErr error

	// gotScopeID/gotName/gotVision record the last Create call's arguments.
	gotScopeID uuid.UUID
	gotName    string
	gotVision  string
}

func (f *fakeProductStore) Create(ctx context.Context, scopeID uuid.UUID, name, vision string) (store.Product, error) {
	f.gotScopeID, f.gotName, f.gotVision = scopeID, name, vision
	if f.createErr != nil {
		return store.Product{}, f.createErr
	}
	return store.Product{ID: uuid.New(), ScopeID: scopeID, Name: name, Vision: vision}, nil
}

func (f *fakeProductStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (store.Product, error) {
	return store.Product{}, store.ErrNotFound
}

func (f *fakeProductStore) ListCurrentByScope(ctx context.Context, scopeID uuid.UUID) ([]store.Product, error) {
	return nil, nil
}

// fakeFeatureSetStore backs featureset_test.go.
type fakeFeatureSetStore struct {
	createErr error

	gotScopeID   uuid.UUID
	gotProductID uuid.UUID
	gotName      string
}

func (f *fakeFeatureSetStore) Create(ctx context.Context, scopeID, productID uuid.UUID, name string, description *string) (store.FeatureSet, error) {
	f.gotScopeID, f.gotProductID, f.gotName = scopeID, productID, name
	if f.createErr != nil {
		return store.FeatureSet{}, f.createErr
	}
	return store.FeatureSet{ID: uuid.New(), ScopeID: scopeID, ProductID: productID, Name: name, Description: description}, nil
}

func (f *fakeFeatureSetStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (store.FeatureSet, error) {
	return store.FeatureSet{}, store.ErrNotFound
}

func (f *fakeFeatureSetStore) ListCurrentByProduct(ctx context.Context, productID uuid.UUID) ([]store.FeatureSet, error) {
	return nil, nil
}

// fakeFeatureStore backs feature_test.go.
type fakeFeatureStore struct {
	createErr error

	gotScopeID      uuid.UUID
	gotFeatureSetID uuid.UUID
	gotName         string
}

func (f *fakeFeatureStore) Create(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, description *string) (store.Feature, error) {
	f.gotScopeID, f.gotFeatureSetID, f.gotName = scopeID, featureSetID, name
	if f.createErr != nil {
		return store.Feature{}, f.createErr
	}
	return store.Feature{ID: uuid.New(), ScopeID: scopeID, FeatureSetID: featureSetID, Name: name, Description: description}, nil
}

func (f *fakeFeatureStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (store.Feature, error) {
	return store.Feature{}, store.ErrNotFound
}

func (f *fakeFeatureStore) ListCurrentByFeatureSet(ctx context.Context, featureSetID uuid.UUID) ([]store.Feature, error) {
	return nil, nil
}

// fakeRequirementStore backs requirement_test.go.
type fakeRequirementStore struct {
	createErr error

	gotScopeID   uuid.UUID
	gotFeatureID uuid.UUID
	gotKind      store.RequirementKind
	gotName      string
}

func (f *fakeRequirementStore) Create(ctx context.Context, scopeID, featureID uuid.UUID, kind store.RequirementKind, name string, body *string) (store.Requirement, error) {
	f.gotScopeID, f.gotFeatureID, f.gotKind, f.gotName = scopeID, featureID, kind, name
	if f.createErr != nil {
		return store.Requirement{}, f.createErr
	}
	return store.Requirement{ID: uuid.New(), ScopeID: scopeID, FeatureID: featureID, Kind: kind, Name: name, Body: body}, nil
}

func (f *fakeRequirementStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (store.Requirement, error) {
	return store.Requirement{}, store.ErrNotFound
}

func (f *fakeRequirementStore) ListCurrentByFeature(ctx context.Context, featureID uuid.UUID) ([]store.Requirement, error) {
	return nil, nil
}

// fakeDecisionStore backs decision_test.go.
type fakeDecisionStore struct {
	createErr error

	gotScopeID      uuid.UUID
	gotFeatureSetID uuid.UUID
	gotName         string
}

func (f *fakeDecisionStore) Create(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, body *string) (store.LoadBearingDecision, error) {
	f.gotScopeID, f.gotFeatureSetID, f.gotName = scopeID, featureSetID, name
	if f.createErr != nil {
		return store.LoadBearingDecision{}, f.createErr
	}
	return store.LoadBearingDecision{ID: uuid.New(), ScopeID: scopeID, FeatureSetID: featureSetID, Name: name, Body: body}, nil
}

func (f *fakeDecisionStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (store.LoadBearingDecision, error) {
	return store.LoadBearingDecision{}, store.ErrNotFound
}

func (f *fakeDecisionStore) ListCurrentByFeatureSet(ctx context.Context, featureSetID uuid.UUID) ([]store.LoadBearingDecision, error) {
	return nil, nil
}
