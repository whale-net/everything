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
	// Deferrals maps a MilestoneRef.ID to the milestone_deferral rows
	// ListMilestoneDeferrals should return for it.
	Deferrals map[uuid.UUID][]store.MilestoneDeferral
	// ShipsAlongside maps a MilestoneRef.ID to its Ships alongside rows.
	ShipsAlongside map[uuid.UUID][]store.MilestoneShipsAlongside
	// Statuses is what ListMilestoneStatuses returns wholesale. Leave nil
	// to model a Source that reports no status history at all.
	Statuses map[uuid.UUID]store.MilestoneStatus
	// StatusCalls records each id batch ListMilestoneStatuses was asked
	// for, so a test can assert the roadmap reads statuses in one batch
	// rather than one round trip per milestone.
	StatusCalls [][]uuid.UUID
	// Notes is what ListProductNotes returns.
	Notes []store.Note
	// Protects is what ListActiveProtects filters by feature id.
	Protects []store.LBProtectsFeature
	// FeatureNotes is what ListFeatureNotes returns per Feature id.
	FeatureNotes map[uuid.UUID][]store.Note
}

var _ render.FeatureNoteSource = (*fakeSource)(nil)

func (f *fakeSource) ListFeatureNotes(ctx context.Context, scopeID, featureID uuid.UUID) ([]store.Note, error) {
	return f.FeatureNotes[featureID], nil
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

func (f *fakeSource) ListMilestoneDeferrals(ctx context.Context, milestoneID uuid.UUID) ([]store.MilestoneDeferral, error) {
	return f.Deferrals[milestoneID], nil
}

func (f *fakeSource) ListMilestoneShipsAlongside(ctx context.Context, milestoneID uuid.UUID) ([]store.MilestoneShipsAlongside, error) {
	return f.ShipsAlongside[milestoneID], nil
}

func (f *fakeSource) ListMilestoneStatuses(ctx context.Context, milestoneIDs []uuid.UUID) (map[uuid.UUID]store.MilestoneStatus, error) {
	f.StatusCalls = append(f.StatusCalls, milestoneIDs)
	return f.Statuses, nil
}

func (f *fakeSource) ListProductNotes(ctx context.Context, scopeID, productID uuid.UUID) ([]store.Note, error) {
	return f.Notes, nil
}

func (f *fakeSource) ListActiveProtects(ctx context.Context, featureIDs []uuid.UUID) ([]store.LBProtectsFeature, error) {
	want := map[uuid.UUID]bool{}
	for _, id := range featureIDs {
		want[id] = true
	}
	var out []store.LBProtectsFeature
	for _, e := range f.Protects {
		if want[e.FeatureID] {
			out = append(out, e)
		}
	}
	return out, nil
}
