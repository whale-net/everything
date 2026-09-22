// workPayloadOutputSchema (issue #2935) is the MCP output schema every
// work.Payload-returning tool advertises: get_task (task_get.go),
// claim_task, complete_task, abandon_task, cancel_task, release_task,
// escalate_task, and requeue_task (task_claim.go, task_complete.go,
// task_abandon.go, task_cancel.go, task_release.go, task_escalate.go,
// task_requeue.go). It must be set explicitly rather than left to
// mcp.AddTool's default reflection-based inference: jsonschema-go's
// jsonschema.ForType walks uuid.UUID's underlying Go kind ([16]byte) and
// infers JSON schema type "array", but encoding/json's actual marshaling
// of a uuid.UUID (via its MarshalText method) produces a JSON string. The
// SDK validates every tool call's real output against whatever schema it
// advertises, so left to the default, every one of these eight tools
// fails its own output-schema validation for any populated payload --
// this is the same jsonschema-go/uuid.UUID gotcha slice.go's
// sliceDocumentOutputSchema documents, and the fix here is the same:
// jsonschema.ForOptions.TypeSchemas overriding just the uuid.UUID leaf to
// match its real wire shape.
//
// Kept in its own file, apart from any Register* function, rather than
// folded into task_get.go alongside RegisterGetTaskPayload.
package tools

import (
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/work"
)

var workPayloadOutputSchema = mustWorkPayloadOutputSchema()

func mustWorkPayloadOutputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[work.Payload](&jsonschema.ForOptions{
		TypeSchemas: map[reflect.Type]*jsonschema.Schema{
			reflect.TypeFor[uuid.UUID](): {Type: "string"},
		},
	})
	if err != nil {
		panic(fmt.Errorf("krill/mcp/tools: building work.Payload output schema: %w", err))
	}
	return s
}
