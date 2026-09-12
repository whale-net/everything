// This file (issue #2493, FR11) is the read-only HTTP surface over
// store/history.go's HistoryStore -- as-of reads and version lists for
// Requirement and LoadBearingDecision. Never gated by RequireSession
// (gate.go): FR3's write-only gate applies to no read path in this
// milestone, exactly like krill/slice's four granularities (slice.go).
package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// asOfQueryParam is the query parameter both as-of endpoints below read
// the target timestamp from, RFC3339-encoded (e.g.
// "2026-01-15T00:00:00Z").
const asOfQueryParam = "at"

// requirementVersion is the wire shape of one Requirement revision --
// shared by GetRequirementAsOfHandler (a single revision) and
// ListRequirementVersionsHandler (every revision).
type requirementVersion struct {
	ID         string     `json:"id"`
	RevisionID string     `json:"revision_id"`
	FeatureID  string     `json:"feature_id"`
	Kind       string     `json:"kind"`
	Name       string     `json:"name"`
	Body       *string    `json:"body,omitempty"`
	Position   int        `json:"position"`
	ValidFrom  time.Time  `json:"valid_from"`
	ValidTo    *time.Time `json:"valid_to,omitempty"`
}

func toRequirementVersion(r store.Requirement) requirementVersion {
	return requirementVersion{
		ID:         r.ID.String(),
		RevisionID: r.RevisionID.String(),
		FeatureID:  r.FeatureID.String(),
		Kind:       string(r.Kind),
		Name:       r.Name,
		Body:       r.Body,
		Position:   r.Position,
		ValidFrom:  r.ValidFrom,
		ValidTo:    r.ValidTo,
	}
}

// decisionVersion is the wire shape of one LoadBearingDecision revision --
// shared by GetLoadBearingDecisionAsOfHandler and
// ListLoadBearingDecisionVersionsHandler.
type decisionVersion struct {
	ID           string     `json:"id"`
	RevisionID   string     `json:"revision_id"`
	FeatureSetID string     `json:"feature_set_id"`
	Name         string     `json:"name"`
	Body         *string    `json:"body,omitempty"`
	Position     int        `json:"position"`
	ValidFrom    time.Time  `json:"valid_from"`
	ValidTo      *time.Time `json:"valid_to,omitempty"`
}

func toDecisionVersion(d store.LoadBearingDecision) decisionVersion {
	return decisionVersion{
		ID:           d.ID.String(),
		RevisionID:   d.RevisionID.String(),
		FeatureSetID: d.FeatureSetID.String(),
		Name:         d.Name,
		Body:         d.Body,
		Position:     d.Position,
		ValidFrom:    d.ValidFrom,
		ValidTo:      d.ValidTo,
	}
}

// versionListResponse is the response body of both version-list endpoints
// below -- every revision of one entity, oldest first (HistoryStore's own
// ordering).
type versionListResponse[T any] struct {
	Versions []T `json:"versions"`
}

// parseAsOf parses r's asOfQueryParam as RFC3339, writing a 400 and
// returning false on a missing or malformed value.
func parseAsOf(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	raw := r.URL.Query().Get(asOfQueryParam)
	if raw == "" {
		writeJSONError(w, http.StatusBadRequest, asOfQueryParam+": required RFC3339 timestamp query parameter")
		return time.Time{}, false
	}
	asOf, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("%s: invalid RFC3339 timestamp: %v", asOfQueryParam, err))
		return time.Time{}, false
	}
	return asOf, true
}

// writeHistoryError maps a HistoryStore error onto this package's one JSON
// error shape: store.ErrNotFound (no revision existed at asOf, or id never
// existed) -> 404, anything else -> 500. Mirrors slice.go's handle's
// not-found mapping.
func writeHistoryError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSONError(w, http.StatusInternalServerError, "internal error")
}

// GetRequirementAsOfHandler returns the Requirement as-of-read endpoint
// (FR11): GET /requirements/{id}/as-of?at=<RFC3339>.
func GetRequirementAsOfHandler(history store.HistoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		asOf, ok := parseAsOf(w, r)
		if !ok {
			return
		}

		requirement, err := history.GetRequirementAsOf(r.Context(), id, asOf)
		if err != nil {
			writeHistoryError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, toRequirementVersion(requirement))
	}
}

// ListRequirementVersionsHandler returns the Requirement version-list
// endpoint (FR11): GET /requirements/{id}/versions.
func ListRequirementVersionsHandler(history store.HistoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		versions, err := history.ListRequirementVersions(r.Context(), id)
		if err != nil {
			writeHistoryError(w, err)
			return
		}

		out := make([]requirementVersion, len(versions))
		for i, v := range versions {
			out[i] = toRequirementVersion(v)
		}
		writeJSON(w, http.StatusOK, versionListResponse[requirementVersion]{Versions: out})
	}
}

// GetLoadBearingDecisionAsOfHandler returns the LoadBearingDecision
// as-of-read endpoint (FR11): GET /load-bearing-decisions/{id}/as-of?at=<RFC3339>.
func GetLoadBearingDecisionAsOfHandler(history store.HistoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		asOf, ok := parseAsOf(w, r)
		if !ok {
			return
		}

		decision, err := history.GetLoadBearingDecisionAsOf(r.Context(), id, asOf)
		if err != nil {
			writeHistoryError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, toDecisionVersion(decision))
	}
}

// ListLoadBearingDecisionVersionsHandler returns the LoadBearingDecision
// version-list endpoint (FR11): GET /load-bearing-decisions/{id}/versions.
func ListLoadBearingDecisionVersionsHandler(history store.HistoryStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		versions, err := history.ListLoadBearingDecisionVersions(r.Context(), id)
		if err != nil {
			writeHistoryError(w, err)
			return
		}

		out := make([]decisionVersion, len(versions))
		for i, v := range versions {
			out[i] = toDecisionVersion(v)
		}
		writeJSON(w, http.StatusOK, versionListResponse[decisionVersion]{Versions: out})
	}
}
