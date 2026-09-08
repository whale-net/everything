package main

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/whagent_net/llm"
)

// disallowedBodyTypes are field types an activity's INPUT must never
// carry (ARCHITECTURE.md "Activity payload discipline": "activities pass
// event IDs, not transcript bodies, so Temporal history stays small").
// []llm.Message is the assembled-context shape BuildContext computes and
// CallModel would otherwise need to receive verbatim if this discipline
// were dropped; llm.Message itself (a single message body) covers the
// same mistake made one level down (e.g. a field that smuggles one
// message's body through instead of a whole slice).
var disallowedBodyTypes = []reflect.Type{
	reflect.TypeOf([]llm.Message{}),
	reflect.TypeOf(llm.Message{}),
}

// assertNoTranscriptBodyFields fails t if any field of v's type (a struct
// value, typically a zero-valued activity input) is, or contains as a
// slice element, one of disallowedBodyTypes.
func assertNoTranscriptBodyFields(t *testing.T, v interface{}) {
	t.Helper()
	typ := reflect.TypeOf(v)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		for _, bad := range disallowedBodyTypes {
			assert.NotEqual(t, bad, field.Type,
				"%s.%s carries a transcript body (%s) across the activity boundary -- activities must pass event IDs, not bodies",
				typ.Name(), field.Name, bad)
		}
	}
}

// TestCallModelInput_CarriesEventIDsNotTranscriptBodies proves
// CallModelInput's shape (issue #2114's Testing phase: "Activities
// receive event IDs, not transcript bodies -- assert the activity input
// type carries no event payloads"): CallModel receives BuildContext's
// selected EventIDs and re-reads the rows itself (activities.go's doc
// comment), never the assembled llm.Message list over the activity
// boundary.
func TestCallModelInput_CarriesEventIDsNotTranscriptBodies(t *testing.T) {
	assertNoTranscriptBodyFields(t, CallModelInput{})

	field, ok := reflect.TypeOf(CallModelInput{}).FieldByName("EventIDs")
	if assert.True(t, ok, "CallModelInput must carry the context's event-ID list") {
		assert.Equal(t, reflect.TypeOf([]uuid.UUID{}), field.Type, "EventIDs must be a []uuid.UUID, not a slice of bodies")
	}
}

// TestCommitTurnInput_CarriesEventIDsNotTranscriptBodies is the same
// assertion for CommitTurnInput: it carries BuildContext's EventIDs list
// through informationally (activities.go's doc comment) but never a
// transcript body -- Response is the turn's own freshly-produced model
// output, not a re-transmission of prior transcript events.
func TestCommitTurnInput_CarriesEventIDsNotTranscriptBodies(t *testing.T) {
	typ := reflect.TypeOf(CommitTurnInput{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Name == "Response" {
			// The turn's own freshly-generated model response (a single
			// llm.Response) is not a "transcript body" in the sense this
			// discipline guards against -- it is the new content CommitTurn
			// exists to persist, not a re-crossing of prior events already
			// committed. Skip it explicitly rather than widen
			// disallowedBodyTypes to also flag llm.Response.
			continue
		}
		for _, bad := range disallowedBodyTypes {
			assert.NotEqual(t, bad, field.Type, "CommitTurnInput.%s carries a transcript body (%s)", field.Name, bad)
		}
	}
}

// TestBuildContextInput_CarriesNoTranscriptBodies is the same assertion
// for BuildContextInput -- it carries the turn's one new piece of input
// (a plain string) plus the resolved Definition, never an assembled
// message list.
func TestBuildContextInput_CarriesNoTranscriptBodies(t *testing.T) {
	assertNoTranscriptBodyFields(t, BuildContextInput{})
}
