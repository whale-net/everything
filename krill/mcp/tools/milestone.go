// This file (issue #2683, FR1/FR2, C13) is krill's milestone authoring MCP
// tool group: thin wrappers over store.MilestoneAuthoringStore --
// create_milestone, set_fr_budget, add_delivers, add_must_not_foreclose,
// and add_deferral (write, Requirement Contributor/Agent/Swarm Operator
// personas -- issue #2926) and get_milestone (read) -- mirroring
// krill/api/handlers/milestone.go's HTTP surface for the same capability,
// via the same handlers.IDResponse/handlers.NewMilestoneResponse this
// package's other write/read tools reuse (LB7). Registered from
// RegisterMilestoneAll (below) onto the design mount
// (../main.go's designReg) -- these write tools attribute the LB4
// subject pair from a resolved krill session (krillSessionInput,
// requireKrillSession -- design.go), the same shape every other write
// tool on that mount already uses, never from a caller-supplied field.
package tools

import (
	"context"
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// createMilestoneInput is create_milestone's argument schema (FR1).
type createMilestoneInput struct {
	krillSessionInput
	ProductID string `json:"product_id" jsonschema:"The Product surrogate id this milestone belongs to, as a UUID string."`
	Name      string `json:"name" jsonschema:"The bare milestone identifier, e.g. \"M3\"."`
	Outcome   string `json:"outcome" jsonschema:"The milestone's outcome sentence (FR1)."`
	FRBudget  *int   `json:"fr_budget,omitempty" jsonschema:"Optional FR budget (FR2) -- may be set later via set_fr_budget. No default cap: the 12-FR default applies per milepebble."`
}

// RegisterCreateMilestone registers create_milestone (FR1): mints a new
// milestone_ref row via store.MilestoneAuthoringStore.CreateMilestone.
func RegisterCreateMilestone(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_milestone",
		Description: "Create a milestone with an outcome sentence and optional FR budget (FR1, FR2).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in createMilestoneInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}
		if in.Name == "" {
			return nil, zero, fmt.Errorf("name: required")
		}
		if in.Outcome == "" {
			return nil, zero, fmt.Errorf("outcome: required")
		}

		milestone, err := milestones.CreateMilestone(ctx, sess.ScopeID, productID, in.Name, in.Outcome, in.FRBudget, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milestone.ID.String()}, nil
	})
}

// setFRBudgetInput is set_fr_budget's argument schema (FR2's revise path).
type setFRBudgetInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone or milepebble surrogate id, as a UUID string."`
	FRBudget    int    `json:"fr_budget" jsonschema:"The milestone's or milepebble's revised FR budget -- the current value after two calls is always the latest."`
}

// RegisterSetFRBudget registers set_fr_budget (FR2): revises a milestone's
// FR budget via store.MilestoneAuthoringStore.SetFRBudget.
func RegisterSetFRBudget(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "set_fr_budget",
		Description: "Revise a milestone's or milepebble's FR budget (FR2).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in setFRBudgetInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}

		if err := milestones.SetFRBudget(ctx, milestoneID, in.FRBudget, sess.Acting, sess.OnBehalfOf); err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milestoneID.String()}, nil
	})
}

// addDeliversInput is add_delivers's argument schema (LB6).
type addDeliversInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone surrogate id, as a UUID string."`
	EntityID    string `json:"entity_id" jsonschema:"The delivered entity's surrogate id (feature or FR), as a UUID string."`
}

// RegisterAddDelivers registers add_delivers (LB6): attaches a
// Delivers association between a milestone and a feature/FR via
// store.MilestoneAuthoringStore.AddDelivers.
func RegisterAddDelivers(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "add_delivers",
		Description: "Record that a milestone delivers a given feature or FR (LB6).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in addDeliversInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}
		entityID, err := uuid.Parse(in.EntityID)
		if err != nil {
			return nil, zero, fmt.Errorf("entity_id: invalid or missing UUID")
		}

		if err := milestones.AddDelivers(ctx, sess.ScopeID, milestoneID, entityID, sess.Acting, sess.OnBehalfOf); err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milestoneID.String()}, nil
	})
}

// addMustNotForecloseInput is add_must_not_foreclose's argument schema (LB6).
type addMustNotForecloseInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone surrogate id, as a UUID string."`
	EntityID    string `json:"entity_id" jsonschema:"The entity's surrogate id this milestone must not foreclose, as a UUID string."`
}

// RegisterAddMustNotForeclose registers add_must_not_foreclose (LB6):
// attaches a Must-not-foreclose association via
// store.MilestoneAuthoringStore.AddMustNotForeclose.
func RegisterAddMustNotForeclose(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "add_must_not_foreclose",
		Description: "Record a decision or entity a milestone must not foreclose (LB6).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in addMustNotForecloseInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}
		entityID, err := uuid.Parse(in.EntityID)
		if err != nil {
			return nil, zero, fmt.Errorf("entity_id: invalid or missing UUID")
		}

		if err := milestones.AddMustNotForeclose(ctx, sess.ScopeID, milestoneID, entityID, sess.Acting, sess.OnBehalfOf); err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milestoneID.String()}, nil
	})
}

// addDeferralInput is add_deferral's argument schema (FR1). Destination
// must be non-empty -- every deferred entry cites where it went.
type addDeferralInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone surrogate id, as a UUID string."`
	Body        string `json:"body" jsonschema:"What was deferred."`
	Destination string `json:"destination" jsonschema:"Where the deferred item went, e.g. a future milestone or issue -- required for every deferral."`
}

// RegisterAddDeferral registers add_deferral (FR1): records a deferral
// via store.MilestoneAuthoringStore.AddDeferral.
func RegisterAddDeferral(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "add_deferral",
		Description: "Record a deferred item and the destination it was pushed to (FR1).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in addDeferralInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}
		if in.Destination == "" {
			return nil, zero, fmt.Errorf("destination: required")
		}

		deferral, err := milestones.AddDeferral(ctx, sess.ScopeID, milestoneID, in.Body, in.Destination, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: deferral.ID.String()}, nil
	})
}

// milestoneIDInput is get_milestone's argument schema: a single
// MilestoneRef surrogate id.
type milestoneIDInput struct {
	ID string `json:"id" jsonschema:"The milestone surrogate id, as a UUID string."`
}

// RegisterGetMilestone registers get_milestone: a milestone's authoring
// fields plus its Delivers/Must-not-foreclose association lists and its
// deferrals -- mirrors GetMilestoneHandler, via the same
// handlers.NewMilestoneResponse constructor (LB7).
func RegisterGetMilestone(reg *server.Registry, milestones store.MilestoneAuthoringStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "get_milestone",
		Description: "Return a milestone's authoring fields plus its Delivers/Must-not-foreclose lists and deferrals.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in milestoneIDInput) (*mcp.CallToolResult, handlers.MilestoneResponse, error) {
		var zero handlers.MilestoneResponse

		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, zero, fmt.Errorf("id: invalid or missing UUID")
		}

		ref, delivers, mustNotForeclose, deferrals, err := milestones.GetMilestone(ctx, id)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewMilestoneResponse(ref, delivers, mustNotForeclose, deferrals), nil
	})
}

// Milepebble tools below (migration 011, issue #2684, FR3/FR4) wrap the
// equally-named store.MilestoneAuthoringStore methods, mirroring
// krill/api/handlers/milestone.go's HTTP surface for the same capability
// (LB7).

// createMilepebbleInput is create_milepebble's argument schema (FR3).
type createMilepebbleInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The parent milestone's surrogate id, as a UUID string."`
	Name        string `json:"name" jsonschema:"The milepebble's short name, unique among its parent milestone's own milepebbles."`
	Outcome     string `json:"outcome" jsonschema:"The milepebble's outcome sentence."`
	FRBudget    *int   `json:"fr_budget,omitempty" jsonschema:"Optional FR budget for this milepebble (default 12 by convention) -- may be set later via set_fr_budget."`
}

// RegisterCreateMilepebble registers create_milepebble (FR3): cuts a
// milestone into a new milepebble via
// store.MilestoneAuthoringStore.CreateMilepebble.
func RegisterCreateMilepebble(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "create_milepebble",
		Description: "Cut a milestone into a new sub-milestone container (milepebble) with an optional FR budget (FR3).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in createMilepebbleInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}
		if in.Name == "" {
			return nil, zero, fmt.Errorf("name: required")
		}
		if in.Outcome == "" {
			return nil, zero, fmt.Errorf("outcome: required")
		}

		milepebble, err := milestones.CreateMilepebble(ctx, sess.ScopeID, milestoneID, in.Name, in.Outcome, in.FRBudget, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milepebble.ID.String()}, nil
	})
}

// addMilepebbleScopeInput is add_milepebble_scope's argument schema (FR3).
type addMilepebbleScopeInput struct {
	krillSessionInput
	MilepebbleID string `json:"milepebble_id" jsonschema:"The milepebble surrogate id, as a UUID string."`
	EntityID     string `json:"entity_id" jsonschema:"The delivered entity's surrogate id (feature or FR) -- must already be in the parent milestone's own Delivers set (FR3)."`
}

// RegisterAddMilepebbleScope registers add_milepebble_scope (FR3):
// attaches a Delivers association between a milepebble and an entity
// already delivered by its parent milestone, via
// store.MilestoneAuthoringStore.AddMilepebbleDelivers.
func RegisterAddMilepebbleScope(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "add_milepebble_scope",
		Description: "Attach an entity already delivered by the parent milestone to one of its milepebbles (FR3).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in addMilepebbleScopeInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milepebbleID, err := uuid.Parse(in.MilepebbleID)
		if err != nil {
			return nil, zero, fmt.Errorf("milepebble_id: invalid or missing UUID")
		}
		entityID, err := uuid.Parse(in.EntityID)
		if err != nil {
			return nil, zero, fmt.Errorf("entity_id: invalid or missing UUID")
		}

		if err := milestones.AddMilepebbleDelivers(ctx, sess.ScopeID, milepebbleID, entityID, sess.Acting, sess.OnBehalfOf); err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: milepebbleID.String()}, nil
	})
}

// addDiscoveredScopeInput is add_discovered_scope's argument schema (FR4).
// Exactly one of FeatureSetID (creating a Feature) or FeatureID (creating
// a Requirement) must be set, mirroring store.DiscoveredScopeInput's own
// shape.
type addDiscoveredScopeInput struct {
	krillSessionInput
	MilepebbleID    string  `json:"milepebble_id" jsonschema:"The milepebble surrogate id, as a UUID string."`
	FeatureSetID    *string `json:"feature_set_id" jsonschema:"Set to create a discovered Feature under this FeatureSet id (UUID string) -- mutually exclusive with feature_id."`
	FeatureID       *string `json:"feature_id" jsonschema:"Set to create a discovered Requirement under this Feature id (UUID string) -- mutually exclusive with feature_set_id."`
	RequirementKind string  `json:"requirement_kind" jsonschema:"\"FR\" or \"NFR\" -- required when feature_id is set, ignored otherwise."`
	Name            string  `json:"name" jsonschema:"The new Feature's or Requirement's name."`
	Description     *string `json:"description" jsonschema:"The new Feature's description -- applies only when feature_set_id is set."`
	Body            *string `json:"body" jsonschema:"The new Requirement's body -- applies only when feature_id is set."`
}

// discoveredScopeResponse is add_discovered_scope's response body,
// mirroring handlers.DiscoveredScopeResponse (LB7).
type discoveredScopeResponse struct {
	EntityID string `json:"entity_id"`
	Kind     string `json:"kind"`
}

// RegisterAddDiscoveredScope registers add_discovered_scope (FR4):
// creates a real Feature/Requirement row and associates it to a
// milepebble and its parent milestone, via
// store.MilestoneAuthoringStore.AddDiscoveredScope.
func RegisterAddDiscoveredScope(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "add_discovered_scope",
		Description: "Add scope discovered mid-milestone (a one-off Feature or Requirement) to a milepebble, without re-drafting the parent milestone (FR4).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in addDiscoveredScopeInput) (*mcp.CallToolResult, discoveredScopeResponse, error) {
		var zero discoveredScopeResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milepebbleID, err := uuid.Parse(in.MilepebbleID)
		if err != nil {
			return nil, zero, fmt.Errorf("milepebble_id: invalid or missing UUID")
		}
		if in.Name == "" {
			return nil, zero, fmt.Errorf("name: required")
		}
		if (in.FeatureSetID == nil) == (in.FeatureID == nil) {
			return nil, zero, fmt.Errorf("exactly one of feature_set_id or feature_id must be set")
		}

		input := store.DiscoveredScopeInput{
			Name:        in.Name,
			Description: in.Description,
			Body:        in.Body,
		}
		if in.FeatureSetID != nil {
			featureSetID, err := uuid.Parse(*in.FeatureSetID)
			if err != nil {
				return nil, zero, fmt.Errorf("feature_set_id: invalid UUID")
			}
			input.FeatureSetID = &featureSetID
		}
		if in.FeatureID != nil {
			featureID, err := uuid.Parse(*in.FeatureID)
			if err != nil {
				return nil, zero, fmt.Errorf("feature_id: invalid UUID")
			}
			input.FeatureID = &featureID
			input.RequirementKind = store.RequirementKind(in.RequirementKind)
		}

		result, err := milestones.AddDiscoveredScope(ctx, sess.ScopeID, milepebbleID, input, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			return nil, zero, err
		}
		return nil, discoveredScopeResponse{EntityID: result.EntityID.String(), Kind: string(result.Kind)}, nil
	})
}

// listMilepebblesInput is list_milepebbles' argument schema: a single
// parent milestone surrogate id.
type listMilepebblesInput struct {
	MilestoneID string `json:"milestone_id" jsonschema:"The parent milestone's surrogate id, as a UUID string."`
}

// milepebbleSummary is one entry of listMilepebblesResponse.Milepebbles,
// mirroring handlers.MilepebbleSummary (LB7).
type milepebbleSummary struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Outcome  *string `json:"outcome"`
	FRBudget *int    `json:"fr_budget" jsonschema:"The milepebble's FR budget, or null if none is set."`
}

// listMilepebblesResponse is list_milepebbles' response body.
type listMilepebblesResponse struct {
	Milepebbles []milepebbleSummary `json:"milepebbles"`
}

// RegisterListMilepebbles registers list_milepebbles: every milepebble
// cut from a milestone, in Position order, via
// store.MilestoneAuthoringStore.ListMilepebblesByMilestone.
func RegisterListMilepebbles(reg *server.Registry, milestones store.MilestoneAuthoringStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_milepebbles",
		Description: "List every milepebble cut from a milestone, in position order (FR3, FR7).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listMilepebblesInput) (*mcp.CallToolResult, listMilepebblesResponse, error) {
		var zero listMilepebblesResponse

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}

		milepebbles, err := milestones.ListMilepebblesByMilestone(ctx, milestoneID)
		if err != nil {
			return nil, zero, err
		}

		summaries := make([]milepebbleSummary, len(milepebbles))
		for i, m := range milepebbles {
			summaries[i] = milepebbleSummary{ID: m.ID.String(), Name: m.Name, Outcome: m.Outcome, FRBudget: m.FRBudget}
		}
		return nil, listMilepebblesResponse{Milepebbles: summaries}, nil
	})
}

// productDeliveryQuerier is the one method of *slice.Querier
// list_product_delivery will call, narrowed to an interface mirroring
// delivery_shipment.go's own deliveryBreakdownQuerier precedent, so this
// file's tests can supply a fake without a real *store.Store.
type productDeliveryQuerier interface {
	ListProductDelivery(ctx context.Context, scopeID, productID uuid.UUID, statuses []store.MilestoneStatus) (slice.DeliveryListing, error)
}

var _ productDeliveryQuerier = (*slice.Querier)(nil)

// listProductDeliveryInput is list_product_delivery's argument schema
// (FR11): a Product surrogate id plus an optional repeated status filter.
// An empty/omitted Statuses means "all", mirroring
// slice.Querier.ListProductDelivery's own contract.
type listProductDeliveryInput struct {
	ProductID string   `json:"product_id" jsonschema:"The Product surrogate id to list delivery for, as a UUID string."`
	Statuses  []string `json:"statuses,omitempty" jsonschema:"Optional list of MilestoneStatus values to filter to (see get_milestone_status for the fixed seven-value set) -- omitted or empty means all statuses."`
}

// listProductDeliveryOutputSchema is list_product_delivery's advertised
// output schema for slice.DeliveryListing, computed once. Required for the
// same reason slice.go's sliceDocumentOutputSchema is (see its own doc
// comment): DeliveryListing nests slice.Document values (Delivers/
// MustNotForeclose), and jsonschema-go's default reflection over
// uuid.UUID (an [16]byte array) does not match encoding/json's real
// string marshaling of it.
var listProductDeliveryOutputSchema = mustListProductDeliveryOutputSchema()

func mustListProductDeliveryOutputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[slice.DeliveryListing](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[uuid.UUID](): {Type: "string"},
		},
	})
	if err != nil {
		panic(fmt.Errorf("krill/mcp/tools: building DeliveryListing output schema: %w", err))
	}
	return s
}

// RegisterListProductDelivery registers list_product_delivery (issue
// #2689, FR11, C28): every milestone and milepebble under a product,
// filterable by status, via //krill/slice.Querier.ListProductDelivery.
// Resolves productID's own scope_id from the product row (the same LB2
// parentage GetProductDeliveryHandler resolves for the HTTP surface),
// since this listing needs a scope-qualified read but this read tool
// takes no krill session.
func RegisterListProductDelivery(reg *server.Registry, products store.ProductStore, querier productDeliveryQuerier) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:         "list_product_delivery",
		Description:  "List every milestone and milepebble under a Product, filterable by status (FR11) -- answers 'what is planned versus what is merely spec'd' for a whole product.",
		OutputSchema: listProductDeliveryOutputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listProductDeliveryInput) (*mcp.CallToolResult, slice.DeliveryListing, error) {
		var zero slice.DeliveryListing

		productID, err := uuid.Parse(in.ProductID)
		if err != nil {
			return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
		}

		statuses := make([]store.MilestoneStatus, 0, len(in.Statuses))
		for _, v := range in.Statuses {
			status := store.MilestoneStatus(v)
			if _, valid := handlers.ValidMilestoneStatuses[status]; !valid {
				return nil, zero, fmt.Errorf("statuses: %q is not one of the fixed FR8 values", v)
			}
			statuses = append(statuses, status)
		}

		product, err := products.GetCurrentByID(ctx, productID)
		if err != nil {
			return nil, zero, err
		}

		listing, err := querier.ListProductDelivery(ctx, product.ScopeID, productID, statuses)
		if err != nil {
			return nil, zero, err
		}
		return nil, listing, nil
	})
}

// RegisterMilestoneAll registers every milestone-authoring tool this
// milestone exposes against reg -- create_milestone/set_fr_budget/
// add_delivers/add_must_not_foreclose/add_deferral/create_milepebble/
// add_milepebble_scope/add_discovered_scope (write) and get_milestone/
// list_milepebbles/list_product_delivery (read). The caller (../main.go)
// mounts reg at the design mount (server.designMountPath via designReg),
// never the read-only spec mount: every write tool here needs a resolved
// krill session for its LB4 subject pair, the same shape every other
// write tool on that mount already requires (see this file's package doc
// comment) -- list_product_delivery is a read tool with no such
// requirement, mounted here anyway (mirroring
// RegisterDeliveryShipmentAll's own read/write mix) since it wraps this
// same file's store.MilestoneAuthoringStore-adjacent surface.
func RegisterMilestoneAll(reg *server.Registry, sessions store.SessionStore, milestones store.MilestoneAuthoringStore, products store.ProductStore, querier productDeliveryQuerier) {
	RegisterCreateMilestone(reg, sessions, milestones)
	RegisterSetFRBudget(reg, sessions, milestones)
	RegisterAddDelivers(reg, sessions, milestones)
	RegisterAddMustNotForeclose(reg, sessions, milestones)
	RegisterAddDeferral(reg, sessions, milestones)
	RegisterGetMilestone(reg, milestones)
	RegisterCreateMilepebble(reg, sessions, milestones)
	RegisterAddMilepebbleScope(reg, sessions, milestones)
	RegisterAddDiscoveredScope(reg, sessions, milestones)
	RegisterListMilepebbles(reg, milestones)
	RegisterListProductDelivery(reg, products, querier)
}
