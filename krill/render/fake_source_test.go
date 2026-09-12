// fakeSource is an in-memory render.Source used by render_test.go: FR13,
// FR14, and the non-write half of FR15 are all provable against any Source
// implementation, so this package's unit coverage builds one by hand rather
// than going through a real Postgres for every case --
// render_integration_test.go separately proves the same contract wired to a
// real store.Store, including FR15's read-only-role proof, which a fake
// cannot stand in for.
package render_test

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

type fakeSource struct {
	Doc      slice.Document
	Personas []store.Persona
	NonGoals []store.NonGoal

	MilestoneRefs []store.MilestoneRef
	// Associations maps a MilestoneRef.ID to the entity_milestone rows
	// ListMilestoneAssociations should return for it -- the same shape
	// krill/store.MilestoneStore itself returns.
	Associations map[uuid.UUID][]store.EntityMilestone
}

var _ render.Source = (*fakeSource)(nil)

func (f *fakeSource) GetProductSlice(ctx context.Context, productID uuid.UUID) (slice.Document, error) {
	return f.Doc, nil
}

func (f *fakeSource) ListPersonas(ctx context.Context, productID uuid.UUID) ([]store.Persona, error) {
	return f.Personas, nil
}

func (f *fakeSource) ListNonGoals(ctx context.Context, productID uuid.UUID) ([]store.NonGoal, error) {
	return f.NonGoals, nil
}

func (f *fakeSource) ListMilestoneRefs(ctx context.Context, scopeID, productID uuid.UUID) ([]store.MilestoneRef, error) {
	return f.MilestoneRefs, nil
}

func (f *fakeSource) ListMilestoneAssociations(ctx context.Context, milestoneID uuid.UUID) ([]store.EntityMilestone, error) {
	return f.Associations[milestoneID], nil
}
