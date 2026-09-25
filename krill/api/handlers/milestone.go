// This file (issue #2683, FR1/FR2, C13) is the milestone authoring HTTP
// surface: six endpoints over store.MilestoneAuthoringStore
// (krill/store/milestone_authoring.go) -- create a milestone, revise its
// FR budget, attach a Delivers/Must-not-foreclose association, record a
// deferral, and read a milestone back with its authoring fields and
// association/deferral lists. Every POST endpoint below must be mounted
// behind RequireSession (gate.go), same as every other M1/M2 write
// endpoint -- the LB4 subject pair always comes from the caller's
// session, never the request body. GET /milestones/{id} is ungated, same
// as every other read endpoint in this package.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// createMilestoneRequest is CreateMilestoneHandler's request body (FR1).
type createMilestoneRequest struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name"`
	Outcome   string `json:"outcome"`
	FRBudget  *int   `json:"fr_budget"`
}

// setFRBudgetRequest is SetFRBudgetHandler's request body (FR2's revise
// path).
type setFRBudgetRequest struct {
	FRBudget int `json:"fr_budget"`
}

// addDeliversRequest is AddDeliversHandler's request body (LB6).
type addDeliversRequest struct {
	EntityID string `json:"entity_id"`
}

// addMustNotForecloseRequest is AddMustNotForecloseHandler's request body
// (LB6).
type addMustNotForecloseRequest struct {
	EntityID string `json:"entity_id"`
}

// addDeferralRequest is AddDeferralHandler's request body (FR1). Destination
// must be non-empty -- every deferred entry cites where it went.
type addDeferralRequest struct {
	Body        string `json:"body"`
	Destination string `json:"destination"`
}

// MilestoneResponse is GetMilestoneHandler's response body -- the
// milestone's authoring fields plus its Delivers/Must-not-foreclose
// entity id lists and its deferrals.
type MilestoneResponse struct {
	ID               string                  `json:"id"`
	ProductID        string                  `json:"product_id"`
	Name             string                  `json:"name"`
	Outcome          *string                 `json:"outcome"`
	FRBudget         *int                    `json:"fr_budget"`
	Delivers         []string                `json:"delivers"`
	MustNotForeclose []string                `json:"must_not_foreclose"`
	Deferrals        []MilestoneDeferralWire `json:"deferrals"`
}

// MilestoneDeferralWire is one entry of MilestoneResponse.Deferrals.
type MilestoneDeferralWire struct {
	Body        string `json:"body"`
	Destination string `json:"destination"`
}

// NewMilestoneResponse builds a MilestoneResponse from a
// store.MilestoneRef plus its two entity_milestone association lists and
// its deferrals -- exported so krill/mcp/tools' get_milestone tool builds
// its response the same way this handler does, never a second conversion
// (LB7).
func NewMilestoneResponse(ref store.MilestoneRef, delivers, mustNotForeclose []store.EntityMilestone, deferrals []store.MilestoneDeferral) MilestoneResponse {
	deliversIDs := make([]string, len(delivers))
	for i, m := range delivers {
		deliversIDs[i] = m.EntityID.String()
	}
	mustNotForecloseIDs := make([]string, len(mustNotForeclose))
	for i, m := range mustNotForeclose {
		mustNotForecloseIDs[i] = m.EntityID.String()
	}
	deferralWires := make([]MilestoneDeferralWire, len(deferrals))
	for i, d := range deferrals {
		deferralWires[i] = MilestoneDeferralWire{Body: d.Body, Destination: d.Destination}
	}

	return MilestoneResponse{
		ID:               ref.ID.String(),
		ProductID:        ref.ProductID.String(),
		Name:             ref.Name,
		Outcome:          ref.Outcome,
		FRBudget:         ref.FRBudget,
		Delivers:         deliversIDs,
		MustNotForeclose: mustNotForecloseIDs,
		Deferrals:        deferralWires,
	}
}

// CreateMilestoneHandler returns the milestone-create endpoint (FR1):
// POST /milestones. Must be mounted behind RequireSession (gate.go).
func CreateMilestoneHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req createMilestoneRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		productID, err := ParseUUIDField("product_id", req.ProductID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("outcome", req.Outcome); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		milestone, err := milestones.CreateMilestone(r.Context(), sess.ScopeID, productID, req.Name, req.Outcome, req.FRBudget, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: milestone.ID.String()})
	}
}

// SetFRBudgetHandler returns the FR-budget revise endpoint (FR2):
// POST /milestones/{id}/fr-budget. Must be mounted behind RequireSession.
func SetFRBudgetHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req setFRBudgetRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		if err := milestones.SetFRBudget(r.Context(), id, req.FRBudget, sess.Acting, sess.OnBehalfOf); err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, IDResponse{ID: id.String()})
	}
}

// AddDeliversHandler returns the Delivers-attach endpoint (LB6): POST
// /milestones/{id}/delivers. Must be mounted behind RequireSession.
func AddDeliversHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req addDeliversRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		entityID, err := ParseUUIDField("entity_id", req.EntityID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := milestones.AddDelivers(r.Context(), sess.ScopeID, id, entityID, sess.Acting, sess.OnBehalfOf); err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, IDResponse{ID: id.String()})
	}
}

// AddMustNotForecloseHandler returns the Must-not-foreclose-attach
// endpoint (LB6): POST /milestones/{id}/must-not-foreclose. Must be
// mounted behind RequireSession.
func AddMustNotForecloseHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req addMustNotForecloseRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		entityID, err := ParseUUIDField("entity_id", req.EntityID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := milestones.AddMustNotForeclose(r.Context(), sess.ScopeID, id, entityID, sess.Acting, sess.OnBehalfOf); err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, IDResponse{ID: id.String()})
	}
}

// AddDeferralHandler returns the deferral-record endpoint (FR1): POST
// /milestones/{id}/deferrals. Must be mounted behind RequireSession.
func AddDeferralHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req addDeferralRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if err := RequireNonEmpty("destination", req.Destination); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		deferral, err := milestones.AddDeferral(r.Context(), sess.ScopeID, id, req.Body, req.Destination, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: deferral.ID.String()})
	}
}

// Milepebble handlers below (migration 011, issue #2684, FR3/FR4) wrap
// the equally-named store.MilestoneAuthoringStore methods -- see that
// file's doc comments for the FR3 subset rule and FR4 atomicity this
// layer relies on but does not itself enforce.

// createMilepebbleRequest is CreateMilepebbleHandler's request body (FR3).
type createMilepebbleRequest struct {
	Name     string `json:"name"`
	Outcome  string `json:"outcome"`
	FRBudget *int   `json:"fr_budget"`
}

// addMilepebbleDeliversRequest is AddMilepebbleDeliversHandler's request
// body (FR3).
type addMilepebbleDeliversRequest struct {
	EntityID string `json:"entity_id"`
}

// addDiscoveredScopeRequest is AddDiscoveredScopeHandler's request body
// (FR4). Exactly one of FeatureSetID (creating a Feature) or FeatureID
// (creating a Requirement) must be set, mirroring
// store.DiscoveredScopeInput's own shape.
type addDiscoveredScopeRequest struct {
	FeatureSetID    *string `json:"feature_set_id"`
	FeatureID       *string `json:"feature_id"`
	RequirementKind string  `json:"requirement_kind"`
	Name            string  `json:"name"`
	Description     *string `json:"description"`
	Body            *string `json:"body"`
}

// DiscoveredScopeResponse is AddDiscoveredScopeHandler's response body --
// the newly created entity's id and which table it landed in ("feature"
// or "requirement").
type DiscoveredScopeResponse struct {
	EntityID string `json:"entity_id"`
	Kind     string `json:"kind"`
}

// MilepebbleSummary is one entry of ListMilepebblesResponse.Milepebbles.
type MilepebbleSummary struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Outcome  *string `json:"outcome"`
	FRBudget *int    `json:"fr_budget"`
}

// ListMilepebblesResponse is ListMilepebblesHandler's response body.
type ListMilepebblesResponse struct {
	Milepebbles []MilepebbleSummary `json:"milepebbles"`
}

// CreateMilepebbleHandler returns the milepebble-create endpoint (FR3):
// POST /milestones/{id}/milepebbles. Must be mounted behind
// RequireSession.
func CreateMilepebbleHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		parentMilestoneID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req createMilepebbleRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if err := RequireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("outcome", req.Outcome); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		milepebble, err := milestones.CreateMilepebble(r.Context(), sess.ScopeID, parentMilestoneID, req.Name, req.Outcome, req.FRBudget, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: milepebble.ID.String()})
	}
}

// AddMilepebbleDeliversHandler returns the milepebble Delivers-attach
// endpoint (FR3): POST /milepebbles/{id}/delivers. Must be mounted behind
// RequireSession.
func AddMilepebbleDeliversHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		milepebbleID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req addMilepebbleDeliversRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		entityID, err := ParseUUIDField("entity_id", req.EntityID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := milestones.AddMilepebbleDelivers(r.Context(), sess.ScopeID, milepebbleID, entityID, sess.Acting, sess.OnBehalfOf); err != nil {
			if errors.Is(err, store.ErrMilepebbleDeliversNotSubset) {
				writeJSONError(w, http.StatusConflict, err.Error())
				return
			}
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, IDResponse{ID: milepebbleID.String()})
	}
}

// AddDiscoveredScopeHandler returns the mid-milestone discovered-scope
// endpoint (FR4): POST /milepebbles/{id}/discovered-scope. Must be
// mounted behind RequireSession.
func AddDiscoveredScopeHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		milepebbleID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req addDiscoveredScopeRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if err := RequireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if (req.FeatureSetID == nil) == (req.FeatureID == nil) {
			writeJSONError(w, http.StatusBadRequest, "exactly one of feature_set_id or feature_id must be set")
			return
		}

		input := store.DiscoveredScopeInput{
			Name:        req.Name,
			Description: req.Description,
			Body:        req.Body,
		}
		if req.FeatureSetID != nil {
			featureSetID, err := ParseUUIDField("feature_set_id", *req.FeatureSetID)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			input.FeatureSetID = &featureSetID
		}
		if req.FeatureID != nil {
			featureID, err := ParseUUIDField("feature_id", *req.FeatureID)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			input.FeatureID = &featureID
			input.RequirementKind = store.RequirementKind(req.RequirementKind)
		}

		result, err := milestones.AddDiscoveredScope(r.Context(), sess.ScopeID, milepebbleID, input, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, DiscoveredScopeResponse{EntityID: result.EntityID.String(), Kind: string(result.Kind)})
	}
}

// GetMilepebbleHandler returns the milepebble read endpoint: GET
// /milepebbles/{id}, ungated like every other read endpoint in this
// package. A milepebble is a `milestone_ref` row like any other (FR3), so
// this reuses store.MilestoneAuthoringStore.GetMilestone and
// NewMilestoneResponse exactly as GetMilestoneHandler does (LB7) -- a
// milepebble simply has no Must-not-foreclose associations or deferrals
// of its own, so those lists come back empty.
func GetMilepebbleHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return GetMilestoneHandler(milestones)
}

// ListMilepebblesHandler returns the milepebble list endpoint: GET
// /milestones/{id}/milepebbles, ungated like every other read endpoint in
// this package.
func ListMilepebblesHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		milestoneID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		milepebbles, err := milestones.ListMilepebblesByMilestone(r.Context(), milestoneID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		summaries := make([]MilepebbleSummary, len(milepebbles))
		for i, m := range milepebbles {
			summaries[i] = MilepebbleSummary{ID: m.ID.String(), Name: m.Name, Outcome: m.Outcome, FRBudget: m.FRBudget}
		}

		writeJSON(w, http.StatusOK, ListMilepebblesResponse{Milepebbles: summaries})
	}
}

// Product-wide delivery listing below (issue #2689, FR11, C28) is a
// scaffold-stage skeleton -- routes.go wiring, repeated `status=` query
// param parsing, the unknown-status 400, and the response shape land in
// the Implementation phase, matching //krill/slice.Querier.
// ListProductDelivery's own skeleton this will call.

// productDeliveryQuerier is the one method of *slice.Querier
// GetProductDeliveryHandler calls, narrowed to an interface for
// testability without Postgres -- mirrors deliveryBreakdownQuerier's own
// precedent above (delivery_shipment.go).
type productDeliveryQuerier interface {
	ListProductDelivery(ctx context.Context, scopeID, productID uuid.UUID, statuses []store.MilestoneStatus) (slice.DeliveryListing, error)
}

var _ productDeliveryQuerier = (*slice.Querier)(nil)

// GetProductDeliveryHandler returns FR11's product-wide delivery listing
// endpoint: GET /products/{id}/delivery?status=planned&status=in+progress,
// ungated like every other read endpoint in this package. A repeated
// `status` query parameter narrows the listing to those statuses; absent
// entirely, it means "all" (slice.Querier.ListProductDelivery's own
// contract). An unrecognized status value is a 400 naming the seven valid
// values (ValidMilestoneStatuses, milestone_status.go) rather than a
// silent empty result.
func GetProductDeliveryHandler(products store.ProductStore, querier productDeliveryQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		productID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		statuses, ok := parseStatusFilter(w, r)
		if !ok {
			return
		}

		// milestone_ref rows are keyed (scope_id, product_id, ...), so
		// this listing needs productID's own scope_id even though the
		// caller supplied no session -- resolved from the product row
		// itself, the same LB2 parentage every scoped store method
		// relies on.
		product, err := products.GetCurrentByID(r.Context(), productID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "product not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		listing, err := querier.ListProductDelivery(r.Context(), product.ScopeID, productID, statuses)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, listing)
	}
}

// parseStatusFilter parses r's repeated `status` query parameter into a
// []store.MilestoneStatus, writing a 400 naming the seven valid values
// (ValidMilestoneStatuses, milestone_status.go) and returning ok=false on
// the first unrecognized value.
func parseStatusFilter(w http.ResponseWriter, r *http.Request) (statuses []store.MilestoneStatus, ok bool) {
	values := r.URL.Query()["status"]
	statuses = make([]store.MilestoneStatus, 0, len(values))
	for _, v := range values {
		status := store.MilestoneStatus(v)
		if _, valid := ValidMilestoneStatuses[status]; !valid {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf(
				"status: must be one of the fixed FR8 values (%s), got %q",
				validMilestoneStatusesJoined(), v))
			return nil, false
		}
		statuses = append(statuses, status)
	}
	return statuses, true
}

// GetMilestoneHandler returns the milestone read endpoint: GET
// /milestones/{id}, ungated like every other read endpoint in this
// package.
func GetMilestoneHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		ref, delivers, mustNotForeclose, deferrals, err := milestones.GetMilestone(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "milestone not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, NewMilestoneResponse(ref, delivers, mustNotForeclose, deferrals))
	}
}
