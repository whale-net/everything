// Package handlers is krill api's HTTP handler layer. This file
// (issue #2489, FR3) is the `init` endpoint: it decodes a caller's acting
// and on-behalf-of subject assertions, mints a krill-native session id via
// krill/store's InitSession, and returns it. No authentication front door
// is mounted on krill's `api` binary in M1 (ARCHITECTURE.md "Open items" --
// that is NFR1's two-front-door pattern, scoped to the separate `krill/mcp`
// binary in issue #2494) -- `init` therefore trusts the caller's asserted
// identity fields rather than verifying a bearer credential itself. See
// gate.go for the write gate every mutating endpoint after this one passes
// through.
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// subjectRequest is the wire shape of a Subject in an initSessionRequest --
// mirrors store.Subject field-for-field (LB4).
type subjectRequest struct {
	Iss  string `json:"iss"`
	Sub  string `json:"sub"`
	Kind string `json:"kind"`
}

// initSessionRequest is InitSessionHandler's request body. Acting and
// OnBehalfOf are both required and are never inferred from one another --
// a caller acting for itself must send identical triples for both (FR3's
// doc comment on store.SessionStore.InitSession).
type initSessionRequest struct {
	ScopeID          string         `json:"scope_id"`
	Acting           subjectRequest `json:"acting"`
	OnBehalfOf       subjectRequest `json:"on_behalf_of"`
	WhagentSessionID *string        `json:"whagent_session_id,omitempty"`
}

// initSessionResponse is InitSessionHandler's response body.
type initSessionResponse struct {
	SessionID string `json:"session_id"`
}

// InitSessionHandler returns the `init` endpoint (FR3): POST /sessions/init.
// It is deliberately not wrapped by RequireSession (gate.go) -- init is how
// a caller obtains a session id in the first place, so it cannot itself
// require one.
func InitSessionHandler(sessions store.SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var req initSessionRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		scopeID, err := uuid.Parse(req.ScopeID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "scope_id: invalid or missing UUID")
			return
		}

		acting, err := parseSubject(req.Acting)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("acting: %v", err))
			return
		}

		onBehalfOf, err := parseSubject(req.OnBehalfOf)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("on_behalf_of: %v", err))
			return
		}

		id, err := sessions.InitSession(r.Context(), scopeID, acting, onBehalfOf, req.WhagentSessionID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to init session")
			return
		}

		writeJSON(w, http.StatusCreated, initSessionResponse{SessionID: id.String()})
	}
}

// parseSubject validates and converts a subjectRequest into a store.Subject.
// iss and sub must both be non-empty (LB4: both real columns, never
// defaulted); kind must be one of store.SubjectKindHuman/SubjectKindService
// -- there is no third kind in M1 (see 003_session.up.sql's CHECK comment).
func parseSubject(s subjectRequest) (store.Subject, error) {
	if s.Iss == "" {
		return store.Subject{}, fmt.Errorf("iss is required")
	}
	if s.Sub == "" {
		return store.Subject{}, fmt.Errorf("sub is required")
	}
	switch store.SubjectKind(s.Kind) {
	case store.SubjectKindHuman, store.SubjectKindService:
	default:
		return store.Subject{}, fmt.Errorf("kind must be %q or %q, got %q", store.SubjectKindHuman, store.SubjectKindService, s.Kind)
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
