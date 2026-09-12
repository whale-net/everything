// This file (issue #2547) is krill's design-session MCP tool group: six
// thin wrappers -- three write, three read -- over store.DesignSessionStore/
// store.RevisionEventStore/store.MediatedWriteStore and this package's own
// slice.Querier, mirroring krill/api/handlers' HTTP surface for the same
// FR1-FR10 capability (design_session.go, revision_event.go, mediated.go,
// open_questions.go, session_slice.go) rather than reimplementing it.
// Registered on server's design-session mount (../server/transport.go's
// designMountPath, "/mcp/design"), never on the FR5-FR8 spec mount
// slice.go registers on -- see RegisterDesignAll's doc comment and
// ../../ARCHITECTURE.md "The design-session MCP surface".
//
// LB7 applied literally, exactly like slice.go: no tool handler in this
// file defines its own bespoke response shape. Every Out type below is
// either krill/slice.Document itself (get_design_session_slice, unchanged,
// the same value krill/api/handlers/session_slice.go serializes) or one of
// krill/api/handlers' own exported wire types (handlers.IDResponse,
// handlers.DesignSessionResponse, handlers.RevisionEventCreatedResponse,
// handlers.ProposeEntitiesResponse, handlers.ListOpenQuestionsResponse),
// built by the exact same handlers.NewXxx constructor (or, for
// IDResponse/RevisionEventCreatedResponse, the same struct literal) the
// corresponding HTTP handler uses -- never a second, MCP-local
// projection of the same data.
package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// krillSessionInput is embedded (anonymously) by every write tool's input
// type below: the krill session id each write tool's HTTP twin takes via
// the X-Krill-Session-Id header (api/handlers/gate.go's RequireSession),
// which an MCP tool call has no header to carry, so it travels as an
// ordinary input field instead. requireKrillSession (below) is the one
// place that field is ever resolved -- every write tool calls it before
// its own mutation runs, so the check is factored once here, not
// reimplemented per tool (this task's Scope section, item 5).
type krillSessionInput struct {
	KrillSessionID string `json:"krill_session_id" jsonschema:"The krill session id minted by POST /sessions/init (api/handlers/session.go) that this write is attributed to -- resolved and validated exactly as api/handlers/gate.go's RequireSession validates the X-Krill-Session-Id header on this tool's HTTP twin."`
}

// requireKrillSession resolves raw against sessions, mirroring
// api/handlers/gate.go's RequireSession and krill/importer's own
// requireSession for a caller that cannot wrap itself in that HTTP
// middleware either (see ../server/registry.go's RegisterWrite doc
// comment). A missing or malformed id, or one sessions.GetSession does not
// recognize, is returned as a clean tool error -- every caller below
// checks this error before running any store mutation, so a bad or
// unknown session id never reaches a write.
func requireKrillSession(ctx context.Context, sessions store.SessionStore, raw string) (store.Session, error) {
	if raw == "" {
		return store.Session{}, fmt.Errorf("krill_session_id: required")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return store.Session{}, fmt.Errorf("krill_session_id: invalid or missing UUID")
	}
	sess, err := sessions.GetSession(ctx, store.SessionID(id))
	if errors.Is(err, store.ErrSessionNotFound) {
		return store.Session{}, fmt.Errorf("unknown krill session %s", id)
	}
	if err != nil {
		return store.Session{}, fmt.Errorf("resolve krill session: %w", err)
	}
	return sess, nil
}

// ── open_design_session (write, FR1/FR8) ────────────────────────────────────

// openDesignSessionInput is open_design_session's argument schema --
// mirrors api/handlers/design_session.go's openDesignSessionRequest, plus
// the krill session id that request's HTTP twin takes via a header instead.
type openDesignSessionInput struct {
	krillSessionInput
	ProductID         string `json:"product_id" jsonschema:"The Product surrogate id (LB2) this session opens against, as a UUID string."`
	OpeningSubmission string `json:"opening_submission" jsonschema:"A Requirement Contributor's plain-language submission -- no entity reference is required or accepted (FR8)."`
}

// RegisterOpenDesignSession registers open_design_session (FR1, FR8): opens
// a new DesignSession, attributing scope_id and opened_by_krill_session_id
// to the resolved krill session only (provenance, never a caller-supplied
// field -- mirrors OpenDesignSessionHandler exactly).
func RegisterOpenDesignSession(reg *server.Registry, sessions store.SessionStore, designSessions store.DesignSessionStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "open_design_session",
		Description: "Open a new design session against a Product (FR1). Accepts a plain-language opening_submission with " +
			"no entity reference (FR8) -- a Requirement Contributor contributes without knowing krill's entity model.",
	}, nil, func(ctx context.Context, _ *mcp.CallToolRequest, in openDesignSessionInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}
		if in.OpeningSubmission == "" {
			return nil, zero, fmt.Errorf("opening_submission: required")
		}

		ds, err := designSessions.Open(ctx, sess.ScopeID, productID, in.OpeningSubmission, sess.ID)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: ds.ID.String()}, nil
	})
}

// ── append_revision_event (write, FR2-FR4/FR7) ──────────────────────────────

// entityDeltaInput mirrors store.EntityDelta field-for-field, exactly like
// api/handlers/revision_event.go's entityDeltaRequest.
type entityDeltaInput struct {
	EntityID    string `json:"entity_id"`
	Change      string `json:"change"`
	SummaryLine string `json:"summary_line"`
}

// openQuestionOpenedInput mirrors store.OpenQuestionOpened field-for-field
// (FR6), exactly like api/handlers/revision_event.go's
// openQuestionOpenedRequest.
type openQuestionOpenedInput struct {
	QuestionID string `json:"question_id"`
	Blocking   bool   `json:"blocking"`
	Text       string `json:"text"`
}

// openQuestionsDeltaInput mirrors store.OpenQuestionsDelta field-for-field.
type openQuestionsDeltaInput struct {
	Opened   []openQuestionOpenedInput `json:"opened"`
	Resolved []string                  `json:"resolved"`
}

// appendRevisionEventInput is append_revision_event's argument schema --
// mirrors api/handlers/revision_event.go's appendRevisionEventRequest, plus
// design_session_id (a path value on that request's HTTP twin) and the
// krill session id (a header there).
type appendRevisionEventInput struct {
	krillSessionInput
	DesignSessionID    string                  `json:"design_session_id" jsonschema:"The DesignSession surrogate id this round belongs to, as a UUID string."`
	EventType          string                  `json:"event_type" jsonschema:"One of: draft, reconciliation, answer, signoff, ruling (FR2)."`
	EntityDeltas       []entityDeltaInput      `json:"entity_deltas"`
	OpenQuestionsDelta openQuestionsDeltaInput `json:"open_questions_delta"`
	VerifiedAgainst    *string                 `json:"verified_against,omitempty" jsonschema:"Required for event_type draft/reconciliation, must be absent otherwise (FR3)."`
	SignoffStatus      *string                 `json:"signoff_status,omitempty" jsonschema:"approved or changes_requested; required for event_type signoff, must be absent otherwise (FR4)."`
}

// RegisterAppendRevisionEvent registers append_revision_event (FR2-FR4,
// FR7's write half): appends one round to an existing DesignSession,
// attributing acting/on_behalf_of/scope_id to the resolved krill session
// only -- mirrors AppendRevisionEventHandler exactly, including reusing
// handlers.ParseEventType/handlers.ParseUUIDField so an unrecognized
// event_type or a malformed entity_deltas[].entity_id fails with the same
// named message this tool's HTTP twin produces (this task's Testing
// criterion 6: "no divergent validation").
func RegisterAppendRevisionEvent(reg *server.Registry, sessions store.SessionStore, events store.RevisionEventStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "append_revision_event",
		Description: "Append one round (FR2) to an existing design session: draft, reconciliation, answer, signoff, or ruling.",
	}, nil, func(ctx context.Context, _ *mcp.CallToolRequest, in appendRevisionEventInput) (*mcp.CallToolResult, handlers.RevisionEventCreatedResponse, error) {
		var zero handlers.RevisionEventCreatedResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		designSessionID, err := uuid.Parse(in.DesignSessionID)
		if err != nil {
			return nil, zero, fmt.Errorf("design_session_id: invalid or missing UUID")
		}

		eventType, err := handlers.ParseEventType(in.EventType)
		if err != nil {
			return nil, zero, err
		}

		entityDeltas := make([]store.EntityDelta, len(in.EntityDeltas))
		for i, d := range in.EntityDeltas {
			entityID, err := handlers.ParseUUIDField(fmt.Sprintf("entity_deltas[%d].entity_id", i), d.EntityID)
			if err != nil {
				return nil, zero, err
			}
			entityDeltas[i] = store.EntityDelta{
				EntityID:    entityID,
				Change:      store.EntityDeltaChange(d.Change),
				SummaryLine: d.SummaryLine,
			}
		}

		var signoffStatus *store.SignoffStatus
		if in.SignoffStatus != nil {
			st := store.SignoffStatus(*in.SignoffStatus)
			signoffStatus = &st
		}

		opened := make([]store.OpenQuestionOpened, len(in.OpenQuestionsDelta.Opened))
		for i, o := range in.OpenQuestionsDelta.Opened {
			opened[i] = store.OpenQuestionOpened{QuestionID: o.QuestionID, Blocking: o.Blocking, Text: o.Text}
		}

		newEvent := store.NewRevisionEvent{
			ScopeID:      sess.ScopeID,
			SessionID:    designSessionID,
			Acting:       sess.Acting,
			OnBehalfOf:   sess.OnBehalfOf,
			EventType:    eventType,
			EntityDeltas: entityDeltas,
			OpenQuestionsDelta: store.OpenQuestionsDelta{
				Opened:   opened,
				Resolved: in.OpenQuestionsDelta.Resolved,
			},
			VerifiedAgainst: in.VerifiedAgainst,
			SignoffStatus:   signoffStatus,
		}

		ev, err := events.Append(ctx, newEvent)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.RevisionEventCreatedResponse{ID: ev.ID.String(), SeqNo: ev.SeqNo}, nil
	})
}

// ── propose_entities (write, FR9/FR10/NFR2 -- Agent-only) ───────────────────

// mediatedProposalInput mirrors store.MediatedEntityProposal field-for-
// field, exactly like api/handlers/mediated.go's mediatedProposalRequest.
type mediatedProposalInput struct {
	Kind                string  `json:"kind" jsonschema:"feature or requirement."`
	ParentID            *string `json:"parent_id,omitempty" jsonschema:"An existing FeatureSet (for a feature proposal) or Feature (for a requirement proposal) surrogate id. Exactly one of parent_id/parent_proposal_index must be set."`
	ParentProposalIndex *int    `json:"parent_proposal_index,omitempty" jsonschema:"The 0-based index of an earlier feature proposal in this same call's proposals list. Only a requirement proposal may set this."`
	Name                string  `json:"name"`
	Body                *string `json:"body,omitempty"`
	Position            int     `json:"position"`
	RequirementKind     string  `json:"requirement_kind,omitempty" jsonschema:"FR or NFR; required when kind is requirement, must be empty otherwise."`
	SummaryLine         string  `json:"summary_line"`
}

// proposeEntitiesInput is propose_entities' argument schema -- mirrors
// api/handlers/mediated.go's proposeEntitiesRequest, plus design_session_id
// (a path value on that request's HTTP twin) and the krill session id (a
// header there).
type proposeEntitiesInput struct {
	krillSessionInput
	DesignSessionID string                  `json:"design_session_id" jsonschema:"The DesignSession surrogate id this mediated write's revision_event belongs to, as a UUID string."`
	VerifiedAgainst *string                 `json:"verified_against" jsonschema:"Required -- the mediated path always appends a draft round (FR3)."`
	Proposals       []mediatedProposalInput `json:"proposals" jsonschema:"Must not be empty."`
}

// RegisterProposeEntities registers propose_entities (FR9, FR10, NFR2):
// turns a Requirement Contributor's plain-language submission into
// Feature/Requirement rows, in one transaction with the describing
// revision_event -- mirrors ProposeEntitiesHandler, delegating every
// per-proposal structural rule (kind, parent shape, requirement_kind) to
// store.MediatedWriteStore.ProposeEntities itself rather than
// re-validating it here, since that store method already rejects a
// malformed batch before ever opening a transaction (mediated.go's
// validateMediatedProposals) and is the one place this rule can never be
// bypassed by a future caller (../../ARCHITECTURE.md).
//
// This is the one write tool in this file gated to PersonaAgent only (this
// task's Scope section, item 3): FR9/FR10 require a producer-role Agent to
// be the caller of a mediated write, since FR10's "acting must differ from
// on-behalf-of" can never be satisfied by a human acting for itself. Every
// other write tool in this file accepts any resolved persona.
func RegisterProposeEntities(reg *server.Registry, sessions store.SessionStore, mediated store.MediatedWriteStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "propose_entities",
		Description: "Turn a Requirement Contributor's plain-language submission into Feature/Requirement rows (FR9), as " +
			"a producer-role Agent acting on that Contributor's behalf (FR10). Callable only by the Agent persona.",
	}, []server.Persona{server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in proposeEntitiesInput) (*mcp.CallToolResult, handlers.ProposeEntitiesResponse, error) {
		var zero handlers.ProposeEntitiesResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		designSessionID, err := uuid.Parse(in.DesignSessionID)
		if err != nil {
			return nil, zero, fmt.Errorf("design_session_id: invalid or missing UUID")
		}
		if len(in.Proposals) == 0 {
			return nil, zero, fmt.Errorf("proposals: must not be empty")
		}

		proposals := make([]store.MediatedEntityProposal, len(in.Proposals))
		for i, p := range in.Proposals {
			mp := store.MediatedEntityProposal{
				Kind:            store.MediatedEntityKind(p.Kind),
				Name:            p.Name,
				Body:            p.Body,
				Position:        p.Position,
				RequirementKind: store.RequirementKind(p.RequirementKind),
				SummaryLine:     p.SummaryLine,
			}
			if p.ParentID != nil {
				parentID, err := handlers.ParseUUIDField(fmt.Sprintf("proposals[%d].parent_id", i), *p.ParentID)
				if err != nil {
					return nil, zero, err
				}
				mp.ParentID = &parentID
			}
			if p.ParentProposalIndex != nil {
				idx := *p.ParentProposalIndex
				mp.ParentProposalIndex = &idx
			}
			proposals[i] = mp
		}

		ev, entities, err := mediated.ProposeEntities(ctx, store.MediatedProposal{
			SessionID:       designSessionID,
			ScopeID:         sess.ScopeID,
			Acting:          sess.Acting,
			OnBehalfOf:      sess.OnBehalfOf,
			EventType:       store.EventTypeDraft,
			VerifiedAgainst: in.VerifiedAgainst,
			Proposals:       proposals,
		})
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewProposeEntitiesResponse(ev, entities), nil
	})
}

// ── get_design_session (read, FR2) ──────────────────────────────────────────

// designSessionIDInput is the argument schema shared by every read tool in
// this file: a single DesignSession surrogate id. Kept distinct from
// slice.go's sliceInput (structurally identical) since the two name
// different kinds of id in their jsonschema description -- a spec entity
// there, a DesignSession here.
type designSessionIDInput struct {
	ID string `json:"id" jsonschema:"The DesignSession surrogate id, as a UUID string."`
}

// RegisterGetDesignSession registers get_design_session (FR2): the
// design_session row plus its ordered revision_event log -- mirrors
// GetDesignSessionHandler exactly, via the same handlers.
// NewDesignSessionResponse constructor.
func RegisterGetDesignSession(reg *server.Registry, designSessions store.DesignSessionStore, events store.RevisionEventStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "get_design_session",
		Description: "Return a design session's row plus its ordered revision_event log (FR2).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in designSessionIDInput) (*mcp.CallToolResult, handlers.DesignSessionResponse, error) {
		var zero handlers.DesignSessionResponse

		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, zero, fmt.Errorf("id: invalid or missing UUID")
		}

		ds, err := designSessions.GetByID(ctx, id)
		if err != nil {
			return nil, zero, err
		}
		revisionEvents, err := events.ListBySession(ctx, id)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewDesignSessionResponse(ds, revisionEvents), nil
	})
}

// ── get_design_session_slice (read, FR5) ────────────────────────────────────

// RegisterGetDesignSessionSlice registers get_design_session_slice (FR5):
// the "current draft" reconstruction for one design session -- the union of
// every entity_id any of its revision_events touched, resolved through
// querier.GetEntitySetSlice. Returns slice.Document UNCHANGED (LB7),
// reusing sliceDocumentOutputSchema (slice.go) and
// handlers.UnionEntityDeltaIDs so this tool's response is byte-identical to
// GET /design-sessions/{id}/slice's for the same session (this task's
// Testing criterion 5) -- mirrors api/handlers/session_slice.go's handle
// exactly.
func RegisterGetDesignSessionSlice(reg *server.Registry, designSessions store.DesignSessionStore, events store.RevisionEventStore, querier *slice.Querier) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:         "get_design_session_slice",
		Description:  "Return the FR5 scoped slice for a design session: the union of every entity its revision_events touched, as currently written.",
		OutputSchema: sliceDocumentOutputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in designSessionIDInput) (*mcp.CallToolResult, slice.Document, error) {
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, slice.Document{}, fmt.Errorf("id: invalid or missing UUID")
		}

		if _, err := designSessions.GetByID(ctx, id); err != nil {
			return nil, slice.Document{}, err
		}
		revisionEvents, err := events.ListBySession(ctx, id)
		if err != nil {
			return nil, slice.Document{}, err
		}
		doc, err := querier.GetEntitySetSlice(ctx, handlers.UnionEntityDeltaIDs(revisionEvents))
		if err != nil {
			return nil, slice.Document{}, err
		}
		return nil, doc, nil
	})
}

// ── list_open_questions (read, FR6) ─────────────────────────────────────────

// listOpenQuestionsInput is list_open_questions' argument schema -- mirrors
// ListOpenQuestionsHandler's `?blocking=true` query parameter as an
// ordinary bool field, since an MCP tool call has no query string.
type listOpenQuestionsInput struct {
	ID       string `json:"id" jsonschema:"The DesignSession surrogate id, as a UUID string."`
	Blocking bool   `json:"blocking,omitempty" jsonschema:"If true, return only blocking open questions (FR6)."`
}

// RegisterListOpenQuestions registers list_open_questions (FR6): the
// derived, last-event-wins open-question view over a design session's
// revision_event log -- mirrors ListOpenQuestionsHandler exactly, via the
// same handlers.NewListOpenQuestionsResponse constructor.
func RegisterListOpenQuestions(reg *server.Registry, designSessions store.DesignSessionStore, events store.RevisionEventStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_open_questions",
		Description: "Return a design session's currently open questions (FR6), each tagged blocking or non-blocking.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listOpenQuestionsInput) (*mcp.CallToolResult, handlers.ListOpenQuestionsResponse, error) {
		var zero handlers.ListOpenQuestionsResponse

		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, zero, fmt.Errorf("id: invalid or missing UUID")
		}

		if _, err := designSessions.GetByID(ctx, id); err != nil {
			return nil, zero, err
		}
		questions, err := events.ListOpenQuestions(ctx, id)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewListOpenQuestionsResponse(questions, in.Blocking), nil
	})
}

// ── entrypoint ───────────────────────────────────────────────────────────────

// RegisterDesignAll registers every FR1-FR10 design-session tool this
// milestone exposes against reg, backed by entities/sessions/querier. The
// caller (../main.go) must register reg's underlying *mcp.Server at
// server's designMountPath (../server/transport.go), never at specMountPath
// -- see this file's package doc comment.
func RegisterDesignAll(reg *server.Registry, entities *store.Store, sessions store.SessionStore, querier *slice.Querier) {
	RegisterOpenDesignSession(reg, sessions, entities.DesignSessions())
	RegisterAppendRevisionEvent(reg, sessions, entities.RevisionEvents())
	RegisterProposeEntities(reg, sessions, entities.MediatedWrites())
	RegisterGetDesignSession(reg, entities.DesignSessions(), entities.RevisionEvents())
	RegisterGetDesignSessionSlice(reg, entities.DesignSessions(), entities.RevisionEvents(), querier)
	RegisterListOpenQuestions(reg, entities.DesignSessions(), entities.RevisionEvents())
}
