// Package tools holds krill's MCP tool groups (issue #2494, FR10/NFR1).
// This file is the one group M1 ships: four thin wrappers over
// //krill/slice's scoped-slice query (FR5-FR8), following
// audience_score_system/mcp/tools' one-file-per-group convention and
// mirroring krill/api/handlers/slice.go's own shape -- the HTTP surface
// over the exact same //krill/slice.Querier methods.
//
// Every tool here returns slice.Document UNCHANGED (LB7: "M1's MCP tool
// is a thin wrapper over it, not the thing itself" -- see
// //krill/slice/query.go's Querier doc comment). None reshapes the
// document into a bespoke per-tool output type; doing so is precisely
// the second projection LB7 forbids. This is also what makes the MCP tool
// response byte-identical to the corresponding HTTP slice response
// payload (issue #2494's acceptance criteria) -- both serialize the same
// slice.Document value with encoding/json's default struct tags, nothing
// tool- or handler-specific in between.
package tools

import (
	"context"
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/slice"
)

// sliceInput is the argument schema shared by all four granularities: a
// single surrogate id (LB2) naming the FeatureSet/Feature/Requirement/
// Product to slice from. One shared type (rather than a per-tool type
// that would only differ in its doc comment) mirrors
// krill/api/handlers/slice.go's own sliceQueryFunc-keyed handle() --
// there is exactly one shape to parse here, four times.
type sliceInput struct {
	ID string `json:"id" jsonschema:"The surrogate id (LB2) of the entity to slice from, as a UUID string"`
}

// sliceQueryFunc is the signature every slice.Querier granularity method
// shares -- what lets one registerSliceTool implementation back all four
// tools (FR9: one document type, four granularities) instead of a
// hand-written handler per tool that would only re-prove the same
// parse/call sequence four times.
type sliceQueryFunc func(ctx context.Context, id uuid.UUID) (slice.Document, error)

// sliceDocumentOutputSchema is the MCP output schema every FR5-FR8 tool
// advertises for slice.Document, computed once and shared since all four
// granularities return the same document type (FR9). It must be set
// explicitly rather than left to mcp.AddTool's default
// reflection-based inference: jsonschema-go's jsonschema.ForType walks
// uuid.UUID's underlying Go kind ([16]byte) and infers JSON schema type
// "array", but encoding/json's actual marshaling of a uuid.UUID (via its
// MarshalText method) produces a JSON string. The SDK validates every
// tool call's real output against whatever schema it advertises, so
// left to the default, every one of these tools fails its own
// output-schema validation for any populated document -- issue #2494's
// Testing-phase defect. This is the same jsonschema-go/uuid.UUID gotcha
// audience_score_system/mcp/tools/research.go documents on the input
// side (hence that package declares UUID-carrying input fields as
// string, not uuid.UUID) -- but LB7 forbids reshaping slice.Document
// into a bespoke output DTO the way that package reshapes its outputs,
// so the fix here corrects the advertised schema instead, via
// jsonschema.ForOptions.TypeSchemas overriding just the uuid.UUID leaf
// to match its real wire shape.
var sliceDocumentOutputSchema = mustSliceDocumentOutputSchema()

func mustSliceDocumentOutputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[slice.Document](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[uuid.UUID](): {Type: "string"},
		},
	})
	if err != nil {
		panic(fmt.Errorf("krill/mcp/tools: building slice.Document output schema: %w", err))
	}
	return s
}

// registerSliceTool registers one FR5-FR8 granularity as a read-only MCP
// tool: parse the caller's id, call query, return the resulting
// slice.Document unchanged (LB7).
func registerSliceTool(reg *server.Registry, name, description string, query sliceQueryFunc) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:         name,
		Description:  description,
		OutputSchema: sliceDocumentOutputSchema,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in sliceInput) (*mcp.CallToolResult, slice.Document, error) {
		id, err := uuid.Parse(in.ID)
		if err != nil {
			return nil, slice.Document{}, err
		}
		doc, err := query(ctx, id)
		if err != nil {
			return nil, slice.Document{}, err
		}
		return nil, doc, nil
	})
}

// RegisterFeatureSetSlice registers get_feature_set_slice (FR5): a
// FeatureSet, its Features, their FRs/NFRs, and only the
// LoadBearingDecisions attached to that FeatureSet.
func RegisterFeatureSetSlice(reg *server.Registry, querier *slice.Querier) {
	registerSliceTool(reg, "get_feature_set_slice",
		"Return the FR5 scoped slice for a FeatureSet: the FeatureSet itself, its Features, their FRs/NFRs, "+
			"and only the LoadBearingDecisions attached to that FeatureSet -- never the product-wide decision list. "+
			"Takes a single FeatureSet surrogate id.",
		querier.GetFeatureSetSlice)
}

// RegisterFeatureSlice registers get_feature_slice (FR6): a Feature and
// its FRs/NFRs, nothing else.
func RegisterFeatureSlice(reg *server.Registry, querier *slice.Querier) {
	registerSliceTool(reg, "get_feature_slice",
		"Return the FR6 scoped slice for a Feature: the Feature itself and its FRs/NFRs -- no FeatureSet, no "+
			"LoadBearingDecisions. Takes a single Feature surrogate id.",
		querier.GetFeatureSlice)
}

// RegisterRequirementSlice registers get_requirement_slice (FR7): a
// single FR or NFR by surrogate id alone.
func RegisterRequirementSlice(reg *server.Registry, querier *slice.Querier) {
	registerSliceTool(reg, "get_requirement_slice",
		"Return the FR7 scoped slice for a single Requirement (FR or NFR), by surrogate id alone -- no parent "+
			"Feature, FeatureSet, or Product context.",
		querier.GetRequirementSlice)
}

// RegisterProductSlice registers get_product_slice (FR8): every
// FeatureSet, Feature, FR, NFR, and LoadBearingDecision beneath a
// Product, in one call.
func RegisterProductSlice(reg *server.Registry, querier *slice.Querier) {
	registerSliceTool(reg, "get_product_slice",
		"Return the FR8 scoped slice for an entire Product: every FeatureSet, Feature, FR, NFR, and "+
			"LoadBearingDecision beneath it, in one call. Takes a single Product surrogate id.",
		querier.GetProductSlice)
}

// RegisterAll registers every FR5-FR8 granularity this milestone exposes
// against reg, backed by querier.
func RegisterAll(reg *server.Registry, querier *slice.Querier) {
	RegisterFeatureSetSlice(reg, querier)
	RegisterFeatureSlice(reg, querier)
	RegisterRequirementSlice(reg, querier)
	RegisterProductSlice(reg, querier)
}
