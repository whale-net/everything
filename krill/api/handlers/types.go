// This file (issue #2490, FR1/FR2/FR4) is the write API's shared request
// decoding and error-mapping surface: entity create handlers (product.go,
// featureset.go, feature.go, requirement.go, decision.go) all decode a
// strict (unknown-field-rejecting) JSON body via decodeStrict and map their
// store call's error via writeStoreError, so the 4xx/409 shape below is
// identical across every M1 create endpoint rather than reinvented per
// handler.
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/whale-net/everything/krill/store"
)

// idResponse is every create handler's response body (LB2): the entity's
// immutable surrogate id, and nothing else -- no response field here ever
// carries a stored display number ("FR7", "LB3", etc.).
type idResponse struct {
	ID string `json:"id"`
}

// decodeStrict decodes r's JSON body into dst, rejecting unknown fields --
// this is what turns a multi-parent payload (an extra parent-shaped field
// no request struct below declares) into a 400 rather than the decoder
// silently ignoring it. Mirrors session.go's InitSessionHandler decode.
func decodeStrict(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// parseUUIDField parses value as a UUID, returning a field-scoped error
// (never a bare uuid.Parse error) so a handler's 400 body names which field
// was missing or malformed -- covers both "missing parent" (empty string)
// and "malformed parent" (not a UUID) in the one call.
func parseUUIDField(field, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%s: invalid or missing UUID", field)
	}
	return id, nil
}

// requireNonEmpty rejects a blank required string field with a
// field-scoped error, mirroring parseUUIDField's shape.
func requireNonEmpty(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s: required", field)
	}
	return nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505), via errors.As against *pgconn.PgError rather
// than string matching (mirrors audience_score_system/store's
// person_identity.go precedent). Every scope-qualified uniqueness index
// migration 002 defines (e.g. product_scope_name_current_idx) surfaces
// here so a duplicate-name create is a 409, never a 500.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// writeStoreError maps a krill/store Create* error onto this package's one
// JSON error shape (jsonError, session.go):
//   - store.ErrNotFound (missing or cross-scope parent, LB2 parentage) -> 400
//   - a scope-qualified unique-constraint violation                   -> 409
//   - anything else (a genuine store failure)                         -> 500
//
// Every create handler below funnels its store call's error through this
// single switch so the mapping cannot drift handler-to-handler.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case isUniqueViolation(err):
		writeJSONError(w, http.StatusConflict, "an entity with this name already exists in this scope")
	default:
		writeJSONError(w, http.StatusInternalServerError, "failed to write entity")
	}
}

// requireSessionOrInternalError resolves the GatedSession RequireSession
// placed on r's context. Every create handler below is only ever mounted
// behind RequireSession (routes.go), so a missing GatedSession here is a
// wiring bug, not a caller error -- reported as 500 rather than silently
// proceeding with a zero-value session (which would write a zero-UUID
// scope_id, violating LB1's "never optional" requirement).
func requireSessionOrInternalError(w http.ResponseWriter, r *http.Request) (GatedSession, bool) {
	sess, ok := SessionFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "no session on request context (handler not mounted behind RequireSession)")
		return GatedSession{}, false
	}
	return sess, true
}
