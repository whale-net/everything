package render

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// StoreSource adapts a *store.Store into a Source (FR15): it holds the
// same concrete store.Store a write path would, but the only methods it
// exposes to a caller holding it as a Source are the read methods Source
// declares -- there is no way to reach StoreSource's underlying
// Create/GetOrCreateRef/AddAssociation methods through the Source
// interface, so Render (which only ever holds a Source) has no path to
// any of them either. Construct one with NewStoreSource; krill/render/cmd
// is the only caller in this tree.
type StoreSource struct {
	querier *slice.Querier
	store   *store.Store
}

var _ Source = (*StoreSource)(nil)

// NewStoreSource returns a StoreSource backed by st.
func NewStoreSource(st *store.Store) *StoreSource {
	return &StoreSource{querier: slice.NewQuerier(st), store: st}
}

func (s *StoreSource) GetProductSlice(ctx context.Context, productID uuid.UUID) (slice.Document, error) {
	return s.querier.GetProductSlice(ctx, productID)
}

func (s *StoreSource) ListPersonas(ctx context.Context, productID uuid.UUID) ([]store.Persona, error) {
	return s.store.Personas().ListCurrentByProduct(ctx, productID)
}

func (s *StoreSource) ListNonGoals(ctx context.Context, productID uuid.UUID) ([]store.NonGoal, error) {
	return s.store.NonGoals().ListCurrentByProduct(ctx, productID)
}

func (s *StoreSource) ListMilestoneRefs(ctx context.Context, scopeID, productID uuid.UUID) ([]store.MilestoneRef, error) {
	return s.store.Milestones().ListRefsByProduct(ctx, scopeID, productID)
}

func (s *StoreSource) ListMilestoneAssociations(ctx context.Context, milestoneID uuid.UUID) ([]store.EntityMilestone, error) {
	return s.store.Milestones().ListAssociationsByMilestone(ctx, milestoneID)
}

func (s *StoreSource) ListMilestoneDeferrals(ctx context.Context, milestoneID uuid.UUID) ([]store.MilestoneDeferral, error) {
	return s.store.MilestoneAuthoring().ListDeferrals(ctx, milestoneID)
}

func (s *StoreSource) ListMilestoneShipsAlongside(ctx context.Context, milestoneID uuid.UUID) ([]store.MilestoneShipsAlongside, error) {
	return s.store.MilestoneAuthoring().ListShipsAlongside(ctx, milestoneID)
}

// ListMilestoneStatuses is a read of `milestone_status_event` (migration
// 012) and nothing else -- it adds no write path to what a Source holder
// can reach. See this type's doc comment on why that matters.
func (s *StoreSource) ListMilestoneStatuses(ctx context.Context, milestoneIDs []uuid.UUID) (map[uuid.UUID]store.MilestoneStatus, error) {
	return s.store.MilestoneStatus().CurrentStatuses(ctx, milestoneIDs)
}

// ListProductNotes is a read of `task_note` narrowed to this Product, and
// nothing else.
func (s *StoreSource) ListProductNotes(ctx context.Context, scopeID, productID uuid.UUID) ([]store.Note, error) {
	return s.store.Tasks().ListNotesForEntity(ctx, scopeID, store.NoteEntityKindProduct, productID)
}

// ListActiveProtects is a read of `lb_protects_feature_active` and nothing else.
func (s *StoreSource) ListActiveProtects(ctx context.Context, featureIDs []uuid.UUID) ([]store.LBProtectsFeature, error) {
	return s.store.LBProtects().ListActiveByFeatures(ctx, featureIDs)
}

var _ StampSource = (*StoreSource)(nil)

// ProductSourceTime reports the Product's latest spec change time.
func (s *StoreSource) ProductSourceTime(ctx context.Context, productID uuid.UUID) (time.Time, error) {
	return s.store.ProductSourceTime(ctx, productID)
}
