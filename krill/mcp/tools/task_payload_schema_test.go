// No-database unit test (issue #2935) proving get_task's declared output
// schema -- the same workPayloadOutputSchema get_task, claim_task,
// complete_task, abandon_task, cancel_task, release_task, escalate_task,
// and requeue_task (task_get.go, task_claim.go, task_complete.go,
// task_abandon.go, task_cancel.go, task_release.go, task_escalate.go,
// task_requeue.go) all advertise -- actually validates a real work.Payload
// value whose Slice.Features is populated: the exact shape that tripped
// go-sdk's own output-schema validation before this fix. jsonschema-go's
// reflection-based default infers slice.features[].id/.revision_id
// (uuid.UUID's underlying Go kind, [16]byte) as JSON schema type "array",
// when encoding/json actually marshals a uuid.UUID as a string (via its
// MarshalText method).
//
// Fetches the schema the way a real MCP client would -- over a real
// in-memory transport, from RegisterGetTaskPayload's own registration --
// rather than reaching into the tools package's unexported
// workPayloadOutputSchema var directly, so this file stays an ordinary
// external tools_test package like every other file in this directory
// (store.New(nil)/mcp.NewInMemoryTransports, no Postgres needed --
// registration never queries, mirroring task_registration_test.go's own
// reasoning).
package tools_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/mcp/tools"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// TestGetTaskOutputSchema_ValidatesPopulatedFeatures reproduces issue
// #2935's exact failure mode: a Payload whose Slice.Features has one
// entry (both blocked tasks in the issue belonged to a FeatureSet with a
// single Feature) must validate clean against get_task's declared output
// schema, not fail with "has type \"string\", want \"array\"" on
// features[].id/.revision_id.
func TestGetTaskOutputSchema_ValidatesPopulatedFeatures(t *testing.T) {
	ctx := context.Background()
	tasks := store.New(nil).Tasks()

	srv := mcp.NewServer(server.Implementation, nil)
	reg := server.NewRegistry(srv)
	tools.RegisterGetTaskPayload(reg, tasks, nil)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := srv.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	var outputSchema any
	for tool, err := range cs.Tools(ctx, nil) {
		require.NoError(t, err)
		if tool.Name == "get_task" {
			outputSchema = tool.OutputSchema
		}
	}
	require.NotNil(t, outputSchema, "get_task must advertise an output schema")

	raw, err := json.Marshal(outputSchema)
	require.NoError(t, err)
	var schema jsonschema.Schema
	require.NoError(t, json.Unmarshal(raw, &schema))
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	require.NoError(t, err)

	escalationID := uuid.New()
	payload := work.Payload{
		Slice: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Features: []slice.FeatureEntity{
				{
					EntityRef:    slice.EntityRef{ID: uuid.New(), RevisionID: uuid.New()},
					FeatureSetID: uuid.New(),
					Name:         "Toggle Page",
					Position:     0,
				},
			},
		},
		Task: work.TaskView{
			ID:            uuid.New(),
			MilestoneID:   uuid.New(),
			Title:         "implement toggle page",
			Body:          "ship it",
			CurrentLane:   "implementation",
			LaneSequence:  []string{"scaffold", "implementation", "testing", "done"},
			AttemptNumber: 1,
			CurrentClaim: &work.ClaimView{
				ClaimID:        uuid.New(),
				SessionID:      uuid.New(),
				ClaimedAt:      time.Now(),
				LeaseExpiresAt: time.Now().Add(time.Hour),
			},
			Notes: []work.NoteView{
				{ID: uuid.New(), Kind: "comment", Body: "note body", Status: "noted"},
			},
			State:               "escalated",
			CurrentEscalationID: &escalationID,
		},
	}

	payloadRaw, err := json.Marshal(payload)
	require.NoError(t, err)
	var instance any
	require.NoError(t, json.Unmarshal(payloadRaw, &instance))

	require.NoError(t, resolved.Validate(instance),
		"get_task's declared output schema must accept its own real wire shape, including a populated Slice.Features")
}
