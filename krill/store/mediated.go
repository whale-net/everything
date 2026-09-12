// This file (issue #2546, krill M2, FR9/FR10/NFR2) is MediatedWriteStore --
// the mediated-intake write path: a producer-role Agent turns a
// Requirement Contributor's plain-language submission into Feature/
// Requirement rows, with the entity create(s) and the describing
// revision_event sharing exactly one transaction. It reuses
// revision_event.go's appendRevisionEventTx so ProposeEntities never opens
// a second transaction for its Append half -- there is exactly one
// transaction and one code path, which is what makes NFR2 ("no code path
// writes an entity without a corresponding revision event carrying both
// identities") structural rather than conventional.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MediatedEntityKind discriminates which of M1's two proposable entity
// kinds one MediatedEntityProposal creates -- Feature or Requirement only,
// per FR9's "feature, FR, or NFR rows under the existing Product ->
// FeatureSet -> Feature -> {FR, NFR} chain". FeatureSet is never itself
// proposable on this path: every Feature proposal's parent is an existing
// FeatureSet (ParentID), never another proposal in the same batch.
type MediatedEntityKind string

const (
	MediatedEntityKindFeature     MediatedEntityKind = "feature"
	MediatedEntityKindRequirement MediatedEntityKind = "requirement"
)

// MediatedEntityProposal is one entity ProposeEntities creates.
//
// Exactly one of ParentID / ParentProposalIndex must be set:
//   - ParentID names an existing current row -- a FeatureSet.ID for a
//     Feature proposal, a Feature.ID for a Requirement proposal --
//     checked with the same currentRowExists helper (scope-qualified)
//     every other Create* in this package uses.
//   - ParentProposalIndex instead names an earlier element of the same
//     MediatedProposal.Proposals slice (its 0-based index, which must be
//     less than this proposal's own index and must itself be a
//     MediatedEntityKindFeature proposal) whose freshly minted id becomes
//     this proposal's parent. This is the only way a Requirement can
//     attach to a Feature proposed in the very same call: that Feature's
//     surrogate id does not exist anywhere a caller could name it
//     directly until ProposeEntities mints it mid-transaction. Only a
//     Requirement proposal may set this -- a Feature proposal's parent
//     (a FeatureSet) is never itself proposable on this path.
//
// RequirementKind is required (FR or NFR) when Kind is
// MediatedEntityKindRequirement, and must be empty otherwise.
//
// SummaryLine is the caller-supplied one-line human summary this
// proposal's entity_delta carries on the appended revision_event (mirrors
// AppendRevisionEventHandler's per-delta summary_line) -- the producer
// Agent writes it; it is never derived automatically from Name.
type MediatedEntityProposal struct {
	Kind                MediatedEntityKind
	ParentID            *uuid.UUID
	ParentProposalIndex *int
	Name                string
	Body                *string
	Position            int
	RequirementKind     RequirementKind
	SummaryLine         string
}

// MediatedProposal is MediatedWriteStore.ProposeEntities' input: the
// gating design_session/scope, both LB4 identity triples, the revision
// event's EventType/VerifiedAgainst, and the ordered entity proposals
// themselves.
//
// EventType must be EventTypeDraft -- the mediated path only ever appends
// a draft round; VerifiedAgainst is required (FR3: draft requires it).
type MediatedProposal struct {
	SessionID       uuid.UUID
	ScopeID         uuid.UUID
	Acting          Subject
	OnBehalfOf      Subject
	EventType       EventType
	VerifiedAgainst *string
	Proposals       []MediatedEntityProposal
}

// ProposedEntity is one entity ProposeEntities created -- its kind and its
// freshly minted surrogate id (LB2). Returned in the same order as the
// MediatedProposal.Proposals slice that produced it.
type ProposedEntity struct {
	Kind MediatedEntityKind
	ID   uuid.UUID
}

// ErrEmptyMediatedProposal is returned by ProposeEntities when
// MediatedProposal.Proposals is empty -- an empty mediated write is a
// caller bug, not a no-op.
var ErrEmptyMediatedProposal = errors.New("krill/store: mediated proposal list must not be empty")

// ErrMediatedIdentitySame is returned by ProposeEntities when Acting and
// OnBehalfOf are the same identity (all six fields equal) -- FR10's "each
// populated distinctly -- never both slots set to the same identity for
// this path". Checked here, in the store, so no future caller (an HTTP
// handler, an MCP tool, or otherwise) can bypass it by calling the store
// directly.
var ErrMediatedIdentitySame = errors.New("krill/store: mediated write requires acting and on-behalf-of to be distinct identities")

// ErrInvalidMediatedProposal is returned by ProposeEntities when a
// MediatedEntityProposal itself is malformed: an unrecognized Kind, a
// missing or doubly-specified parent reference, a ParentProposalIndex that
// does not name an earlier MediatedEntityKindFeature proposal, a missing
// Name, or RequirementKind used/omitted inconsistently with Kind.
var ErrInvalidMediatedProposal = errors.New("krill/store: invalid mediated proposal")

// MediatedWriteStore is the mediated-intake write path (FR9, FR10, NFR2):
// a producer-role Agent turns a Requirement Contributor's plain-language
// submission into Feature/Requirement rows.
type MediatedWriteStore interface {
	// ProposeEntities creates every entity in in.Proposals and appends
	// exactly one revision_event describing all of them, in a single
	// transaction (NFR2) -- a crash partway through leaves zero entities
	// and zero revision_event rows behind, never a partial write. Returns
	// the new revision_event and, in the same order as in.Proposals, the
	// created entities' kinds and ids.
	//
	// Rejects, before ever opening a transaction: an empty Proposals
	// slice (ErrEmptyMediatedProposal); Acting == OnBehalfOf
	// (ErrMediatedIdentitySame, FR10); any malformed proposal
	// (ErrInvalidMediatedProposal); and an EventType other than
	// EventTypeDraft or a missing VerifiedAgainst (ErrInvalidRevisionEvent,
	// via the same validateNewRevisionEvent revision_event.go's Append
	// uses).
	//
	// Rejects, inside the transaction (rolling back everything already
	// written this call, including any entity created earlier in the same
	// Proposals slice): an unknown or cross-scope ParentID (ErrNotFound,
	// via errParentNotFound).
	ProposeEntities(ctx context.Context, in MediatedProposal) (RevisionEvent, []ProposedEntity, error)
}

// mediatedWriteStore is the pgx-backed MediatedWriteStore implementation.
type mediatedWriteStore struct{ pool *pgxpool.Pool }

var _ MediatedWriteStore = mediatedWriteStore{}

// sameSubject reports whether a and b are the same (iss, sub, kind)
// identity triple -- FR10's "all six fields equal" check.
func sameSubject(a, b Subject) bool {
	return a.Iss == b.Iss && a.Sub == b.Sub && a.Kind == b.Kind
}

// validateMediatedProposals checks every structural rule ProposeEntities'
// doc comment lists for proposals that does not require the database --
// run once, up front, before a transaction is ever opened, so a malformed
// batch never touches Postgres at all.
func validateMediatedProposals(proposals []MediatedEntityProposal) error {
	for i, p := range proposals {
		if p.Name == "" {
			return fmt.Errorf("%w: proposals[%d]: name is required", ErrInvalidMediatedProposal, i)
		}

		hasParentID := p.ParentID != nil
		hasParentIndex := p.ParentProposalIndex != nil
		if hasParentID == hasParentIndex {
			return fmt.Errorf("%w: proposals[%d]: exactly one of parent_id/parent_proposal_index must be set", ErrInvalidMediatedProposal, i)
		}
		if hasParentIndex {
			idx := *p.ParentProposalIndex
			if idx < 0 || idx >= i {
				return fmt.Errorf("%w: proposals[%d]: parent_proposal_index %d does not name an earlier proposal", ErrInvalidMediatedProposal, i, idx)
			}
			if proposals[idx].Kind != MediatedEntityKindFeature {
				return fmt.Errorf("%w: proposals[%d]: parent_proposal_index %d must name a feature proposal", ErrInvalidMediatedProposal, i, idx)
			}
		}

		switch p.Kind {
		case MediatedEntityKindFeature:
			if p.RequirementKind != "" {
				return fmt.Errorf("%w: proposals[%d]: requirement_kind must be empty for a feature proposal", ErrInvalidMediatedProposal, i)
			}
			if hasParentIndex {
				return fmt.Errorf("%w: proposals[%d]: a feature proposal's parent must be an existing feature_set (parent_id), not another proposal", ErrInvalidMediatedProposal, i)
			}
		case MediatedEntityKindRequirement:
			if p.RequirementKind != RequirementKindFR && p.RequirementKind != RequirementKindNFR {
				return fmt.Errorf("%w: proposals[%d]: requirement_kind must be FR or NFR", ErrInvalidMediatedProposal, i)
			}
		default:
			return fmt.Errorf("%w: proposals[%d]: unrecognized kind %q", ErrInvalidMediatedProposal, i, p.Kind)
		}
	}
	return nil
}

// ProposeEntities implements MediatedWriteStore.
func (s mediatedWriteStore) ProposeEntities(ctx context.Context, in MediatedProposal) (RevisionEvent, []ProposedEntity, error) {
	if len(in.Proposals) == 0 {
		return RevisionEvent{}, nil, ErrEmptyMediatedProposal
	}
	if sameSubject(in.Acting, in.OnBehalfOf) {
		return RevisionEvent{}, nil, ErrMediatedIdentitySame
	}
	if in.EventType != EventTypeDraft {
		return RevisionEvent{}, nil, fmt.Errorf("%w: mediated proposals must use event_type \"draft\"", ErrInvalidRevisionEvent)
	}
	if err := validateMediatedProposals(in.Proposals); err != nil {
		return RevisionEvent{}, nil, err
	}

	newEvent := NewRevisionEvent{
		ScopeID:         in.ScopeID,
		SessionID:       in.SessionID,
		Acting:          in.Acting,
		OnBehalfOf:      in.OnBehalfOf,
		EventType:       in.EventType,
		VerifiedAgainst: in.VerifiedAgainst,
	}
	if err := validateNewRevisionEvent(newEvent); err != nil {
		return RevisionEvent{}, nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RevisionEvent{}, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	createdIDs := make([]uuid.UUID, len(in.Proposals))
	proposed := make([]ProposedEntity, len(in.Proposals))
	deltas := make([]EntityDelta, len(in.Proposals))

	for i, p := range in.Proposals {
		var parentID uuid.UUID
		var parentTable string // non-empty iff parentID must be checked via currentRowExists

		switch p.Kind {
		case MediatedEntityKindFeature:
			parentTable = "feature_set"
			parentID = *p.ParentID
		case MediatedEntityKindRequirement:
			if p.ParentProposalIndex != nil {
				// Validated above: this index names an earlier
				// MediatedEntityKindFeature proposal, whose id we just
				// minted this same transaction -- no currentRowExists
				// check needed, and none would even see an uncommitted
				// row from a different connection anyway.
				parentID = createdIDs[*p.ParentProposalIndex]
			} else {
				parentTable = "feature"
				parentID = *p.ParentID
			}
		}

		if parentTable != "" {
			exists, err := currentRowExists(ctx, tx, parentTable, parentID, in.ScopeID)
			if err != nil {
				return RevisionEvent{}, nil, err
			}
			if !exists {
				return RevisionEvent{}, nil, errParentNotFound(parentTable, parentID)
			}
		}

		var newID uuid.UUID
		switch p.Kind {
		case MediatedEntityKindFeature:
			err := tx.QueryRow(ctx, `
				INSERT INTO feature (scope_id, feature_set_id, name, description, position)
				VALUES ($1, $2, $3, $4, $5)
				RETURNING id
			`, in.ScopeID, parentID, p.Name, p.Body, p.Position).Scan(&newID)
			if err != nil {
				return RevisionEvent{}, nil, fmt.Errorf("insert feature (proposals[%d]): %w", i, err)
			}
		case MediatedEntityKindRequirement:
			err := tx.QueryRow(ctx, `
				INSERT INTO requirement (scope_id, feature_id, kind, name, body, position)
				VALUES ($1, $2, $3, $4, $5, $6)
				RETURNING id
			`, in.ScopeID, parentID, string(p.RequirementKind), p.Name, p.Body, p.Position).Scan(&newID)
			if err != nil {
				return RevisionEvent{}, nil, fmt.Errorf("insert requirement (proposals[%d]): %w", i, err)
			}
		}

		createdIDs[i] = newID
		proposed[i] = ProposedEntity{Kind: p.Kind, ID: newID}
		deltas[i] = EntityDelta{EntityID: newID, Change: EntityDeltaChangeCreated, SummaryLine: p.SummaryLine}
	}

	newEvent.EntityDeltas = deltas
	ev, err := appendRevisionEventTx(ctx, tx, newEvent)
	if err != nil {
		return RevisionEvent{}, nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return RevisionEvent{}, nil, fmt.Errorf("commit: %w", err)
	}
	return ev, proposed, nil
}
