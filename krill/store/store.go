package store

import "github.com/jackc/pgx/v5/pgxpool"

// Store is the pgx-backed repository over `product`, `feature_set`,
// `feature`, `requirement`, `load_bearing_decision`, `persona`, and
// `non_goal` (migration 002), `milestone_ref` and `entity_milestone`
// (migration 004, issue #2492), `scope` (migration 001, read-only here),
// and `pointer_artifact` (migration 005, issue #2496). Built over
// //libs/go/db's *pgxpool.Pool,
// mirroring audience_score_system/store.Store's shape: a thin holder whose
// accessors hand back per-entity Store implementations, kept as separate
// concrete types because e.g. ProductStore.GetCurrentByID and
// FeatureStore.GetCurrentByID share a method name but return different
// types.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store backed by pool (see //libs/go/db.NewPool).
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Products returns the ProductStore implementation.
func (s *Store) Products() ProductStore { return productStore{pool: s.pool} }

// FeatureSets returns the FeatureSetStore implementation.
func (s *Store) FeatureSets() FeatureSetStore { return featureSetStore{pool: s.pool} }

// Features returns the FeatureStore implementation.
func (s *Store) Features() FeatureStore { return featureStore{pool: s.pool} }

// Requirements returns the RequirementStore implementation.
func (s *Store) Requirements() RequirementStore { return requirementStore{pool: s.pool} }

// Decisions returns the LoadBearingDecisionStore implementation.
func (s *Store) Decisions() LoadBearingDecisionStore { return decisionStore{pool: s.pool} }

// Personas returns the PersonaStore implementation.
func (s *Store) Personas() PersonaStore { return personaStore{pool: s.pool} }

// NonGoals returns the NonGoalStore implementation.
func (s *Store) NonGoals() NonGoalStore { return nonGoalStore{pool: s.pool} }

// Slices returns the SliceStore implementation -- the cross-table reads
// krill/slice's query layer needs on top of the per-entity accessors
// above (see slice.go's doc comment).
func (s *Store) Slices() SliceStore { return sliceStore{pool: s.pool} }

// Milestones returns the MilestoneStore implementation.
func (s *Store) Milestones() MilestoneStore { return milestoneStore{pool: s.pool} }

// Amend returns the AmendStore implementation -- the SCD2 close-and-open
// write path (FR12, issue #2493) for Requirement and LoadBearingDecision.
func (s *Store) Amend() AmendStore { return amendStore{pool: s.pool} }

// History returns the HistoryStore implementation -- as-of reads and
// version lists (FR11, issue #2493) for Requirement and
// LoadBearingDecision.
func (s *Store) History() HistoryStore { return historyStore{pool: s.pool} }

// Scopes returns the ScopeStore implementation.
func (s *Store) Scopes() ScopeStore { return scopeStore{pool: s.pool} }

// PointerArtifacts returns the PointerArtifactStore implementation.
func (s *Store) PointerArtifacts() PointerArtifactStore { return pointerArtifactStore{pool: s.pool} }
