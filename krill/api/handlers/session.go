// Package handlers is krill api's HTTP handler layer. This file holds the
// `init` endpoint (POST /sessions/init) and GET /scope. init derives the
// session's identity from the api auth front door's verified caller and its
// scope from the deployment; krill/mcp/tools' init_session shares
// InitSessionResponse and ParseSubject.
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/whale-net/everything/krill/caller"
	"github.com/whale-net/everything/krill/store"
)

// SubjectRequest is the wire shape of a Subject in an initSessionRequest --
// mirrors store.Subject field-for-field (LB4). Exported (issue #2827) so
// krill/mcp/tools' init_session tool can reuse it, and ParseSubject below,
// rather than a second MCP-local copy of the same validation (LB7).
type SubjectRequest struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	// The description is the only place a caller reading the published
	// init_session tool schema learns agent is accepted, so it names all
	// three kinds ParseSubject below validates.
	Kind string `json:"kind" jsonschema:"Either human, service or agent."`
}

// InitSessionResponse is InitSessionHandler's response body. Exported
// (issue #2827) so krill/mcp/tools' init_session tool returns this exact
// value rather than an MCP-local mirror (LB7, the same rule IDResponse's
// doc comment states for open_design_session).
//
// ScopeID is the scope the session was minted under -- the value
// list_products, list_tasks, and the ops console tools take as input.
type InitSessionResponse struct {
	SessionID string `json:"session_id"`
	ScopeID   string `json:"scope_id"`
}

// IdentityFunc returns the verified caller of a request (from the api's auth
// front door), or false when there is none.
type IdentityFunc func(*http.Request) (caller.Identity, bool)

// InitSessionHandler returns POST /sessions/init. The caller's identity comes
// from the verified credential via identity and the scope from the
// deployment's sole scope; the body carries no identity or scope fields and
// is ignored. It is not wrapped by RequireSession -- init mints the session.
func InitSessionHandler(sessions store.SessionStore, scopes store.ScopeStore, identity IdentityFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		who, ok := identity(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		scope, err := scopes.GetSole(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("resolve scope: %v", err))
			return
		}
		var wsid *string
		if who.WhagentSessionID != "" {
			wsid = &who.WhagentSessionID
		}
		id, err := sessions.InitSession(r.Context(), scope.ID, toStoreSubject(who.Acting), toStoreSubject(who.OnBehalfOf), wsid)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to init session")
			return
		}
		writeJSON(w, http.StatusCreated, InitSessionResponse{SessionID: id.String(), ScopeID: scope.ID.String()})
	}
}

func toStoreSubject(s caller.Subject) store.Subject {
	return store.Subject{Iss: s.Iss, Sub: s.Sub, Kind: store.SubjectKind(s.Kind)}
}

// ParseSubject validates and converts a SubjectRequest into a store.Subject.
// iss and sub must both be non-empty (LB4: both real columns, never
// defaulted); kind must be one of store.SubjectKindHuman,
// store.SubjectKindService or store.SubjectKindAgent.
// Exported (issue #2827) so krill/mcp/tools' init_session tool validates a
// caller-supplied Subject identically to this handler (LB7).
func ParseSubject(s SubjectRequest) (store.Subject, error) {
	if s.Iss == "" {
		return store.Subject{}, fmt.Errorf("iss is required")
	}
	if s.Sub == "" {
		return store.Subject{}, fmt.Errorf("sub is required")
	}
	switch store.SubjectKind(s.Kind) {
	case store.SubjectKindHuman, store.SubjectKindService, store.SubjectKindAgent:
	default:
		return store.Subject{}, fmt.Errorf("kind must be %q, %q or %q, got %q", store.SubjectKindHuman, store.SubjectKindService, store.SubjectKindAgent, s.Kind)
	}
	return store.Subject{Iss: s.Iss, Sub: s.Sub, Kind: store.SubjectKind(s.Kind)}, nil
}

// writeJSON encodes v as the JSON response body with status code and the
// standard Content-Type header -- mirrors main.go's handleHealthz shape,
// the one existing precedent for a JSON response in this binary.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// jsonError is the one error response shape every handler in this package
// returns -- a single "error" field, never a shape that varies by endpoint.
type jsonError struct {
	Error string `json:"error"`
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, jsonError{Error: msg})
}

// ScopeResponse is GET /scope's body.
type ScopeResponse struct {
	ScopeID string `json:"scope_id"`
}

// GetScopeHandler returns GET /scope: the deployment's sole scope_id,
// resolved by the same store.ScopeStore.GetSole the MCP get_scope tool uses.
func GetScopeHandler(scopes store.ScopeStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, err := scopes.GetSole(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusConflict, fmt.Sprintf("resolve scope: %v", err))
			return
		}
		writeJSON(w, http.StatusOK, ScopeResponse{ScopeID: scope.ID.String()})
	}
}
