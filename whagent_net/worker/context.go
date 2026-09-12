package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// BuildContextInput is BuildContext's activity input.
type BuildContextInput struct {
	SessionID uuid.UUID
	Turn      int
	// Definition is the agent definition ResolveAgentDefinition (activities.go)
	// resolved for this turn -- passed through rather than re-resolved, so
	// BuildContext and CallModel (activities.go) agree on exactly the same
	// definition a turn used even if it drifts again before the turn ends.
	Definition session.AgentDefinition
	// Input is this turn's new user input (SendTurnSignal.Input,
	// workflow.go), the one piece of this turn's context BuildContext did
	// not already have sitting in the transcript before this call.
	Input string
}

// BuildContextResult is BuildContext's activity result: only the ordered
// event-ID list the context projection was built from -- never the
// assembled message bodies themselves. This is what ARCHITECTURE.md
// "Three nouns: session, transcript, context" means by "context is
// derived, ephemeral, rebuilt every turn... never stored; each turn
// records the event-ID list it was built from" and what "Activity payload
// discipline" means by "activities pass event IDs, not transcript
// bodies" -- CallModel (activities.go) receives this same EventIDs slice
// and re-reads the rows itself rather than receiving bodies over the
// activity boundary a second time.
type BuildContextResult struct {
	EventIDs []uuid.UUID
}

// maxContextEvents bounds how many of a session's transcript events
// BuildContext selects for a turn's context: a placeholder budgeting
// strategy for M1 (ARCHITECTURE.md "Open items" -- "Context budgeting
// strategy (summarization vs. truncation, when to write summary events):
// worker-internal, defer to the milestone that first hits the budget").
// Bounded ~100-turn sessions producing a handful of events each stay
// comfortably under this ceiling in practice, so plain truncation to the
// most recent maxContextEvents (oldest events dropped first) is a safe
// placeholder rather than a real token-budget/summarization algorithm.
const maxContextEvents = 400

// transcriptReadPageSize bounds each Read call BuildContext issues while
// paging through the whole transcript before truncating to
// maxContextEvents.
const transcriptReadPageSize = 200

// BuildContext is per-turn activity #2 (ARCHITECTURE.md "Session
// workflow"): appends the turn's new user-input event (the "turn's
// new-input event append" -- the one piece of this turn's context not
// already sitting in the transcript), then selects a budgeted projection
// over the transcript -- recent events plus the agent definition, fitted
// to maxContextEvents -- and persists the exact ordered event-ID list that
// projection was built from into `turn_context` (LB1). The projection
// itself (the assembled llm.Message list) is derived and ephemeral and
// never enters workflow history or the `turn_context` row -- what
// BuildContextResult carries back to the workflow, and what gets
// persisted, is only the event-ID list.
//
// Retry-safety: the user-input append goes through
// TranscriptStore.AppendIfAbsent (not Append), so a Temporal retry of this
// activity after a prior attempt's append already committed does not
// produce a duplicate transcript event; SaveTurnContext is an idempotent
// upsert for the same reason.
func (a *Activities) BuildContext(ctx context.Context, in BuildContextInput) (BuildContextResult, error) {
	if a.Store == nil {
		return BuildContextResult{}, fmt.Errorf("worker: Activities.Store is nil")
	}

	userPayload, err := marshalMessagePayload(llm.Message{Role: llm.RoleUser, Content: in.Input})
	if err != nil {
		return BuildContextResult{}, fmt.Errorf("build context: marshal user message: %w", err)
	}
	if _, err := a.Store.Transcript().AppendIfAbsent(ctx, in.SessionID, in.Turn, events.EventTypeUserMessage, userPayload); err != nil {
		return BuildContextResult{}, fmt.Errorf("build context: append user turn: %w", err)
	}

	all, err := readWholeTranscript(ctx, a.Store, in.SessionID)
	if err != nil {
		return BuildContextResult{}, fmt.Errorf("build context: read transcript: %w", err)
	}
	if len(all) > maxContextEvents {
		all = all[len(all)-maxContextEvents:]
	}

	eventIDs := make([]uuid.UUID, len(all))
	for i, ev := range all {
		eventIDs[i] = ev.EventID
	}

	if err := a.Store.Transcript().SaveTurnContext(ctx, session.TurnContext{
		SessionID: in.SessionID,
		Turn:      in.Turn,
		EventIDs:  eventIDs,
	}); err != nil {
		return BuildContextResult{}, fmt.Errorf("build context: save turn context: %w", err)
	}

	return BuildContextResult{EventIDs: eventIDs}, nil
}

// readWholeTranscript pages through every committed event for sessionID in
// seq order via TranscriptStore.Read, which only supports forward
// pagination from a seq -- there is no "read the last N" query, so the
// whole (bounded, ~100-turn) transcript is read and BuildContext truncates
// the tail itself.
func readWholeTranscript(ctx context.Context, store *session.Store, sessionID uuid.UUID) ([]events.Event, error) {
	var all []events.Event
	fromSeq := int64(0)
	for {
		page, err := store.Transcript().Read(ctx, sessionID, fromSeq, transcriptReadPageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < transcriptReadPageSize {
			return all, nil
		}
		fromSeq = page[len(page)-1].Seq + 1
	}
}

// transcriptMessagePayload is the JSON payload shape BuildContext and
// CommitTurn (activities.go) write into transcript_event.payload for an
// events.EventTypeUserMessage / events.EventTypeAssistantMessage event,
// and the shape eventsToMessages parses back out for CallModel
// (activities.go) -- the one schema every producer/consumer of a message
// transcript event in this package agrees on. Round-trips onto
// llm.Message (whagent_net/llm/client.go): Role/Content/ToolCallID/
// ToolCalls.
type transcriptMessagePayload struct {
	Role       string               `json:"role"`
	Content    string               `json:"content,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	ToolCalls  []transcriptToolCall `json:"tool_calls,omitempty"`
}

// transcriptToolCall mirrors llm.ToolCall inside a transcriptMessagePayload.
type transcriptToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// marshalMessagePayload converts an llm.Message into the JSON payload
// AppendIfAbsent/CommitTurn commit for a user_message/assistant_message
// transcript event.
func marshalMessagePayload(m llm.Message) (json.RawMessage, error) {
	payload := transcriptMessagePayload{
		Role:       string(m.Role),
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		payload.ToolCalls = make([]transcriptToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			payload.ToolCalls[i] = transcriptToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal message payload: %w", err)
	}
	return raw, nil
}

// eventsToMessages decodes evs (already in seq/chronological order, see
// TranscriptStore.Read/ReadByIDs) back into the llm.Message list CallModel
// (activities.go) sends as a Request. A user_message/assistant_message
// event decodes into its role's message verbatim (an assistant_message
// already carries any ToolCalls the model requested that turn,
// transcriptMessagePayload.ToolCalls, so there is nothing further to fold
// in from a separate tool_call event -- see below). An intermediate loop
// iteration's assistant message (assistantMessageEventType's
// "assistant_message:<iteration>" prefix -- "add the inner tool loop")
// decodes identically: the final iteration's response is the only one ever
// committed under the bare EventTypeAssistantMessage constant (CommitTurn,
// activities.go), so the two cases never collide within one turn, and both
// must be visible to the next CallModel call the same way. A tool_result
// event (toolResultEventType's "tool_result:<call_index>" prefix, issue
// #2121) decodes into an llm.RoleTool message bound back to its call via
// ToolCallID -- the OpenAI wire protocol CallModel speaks (llm/client.go)
// requires exactly this reply-message shape following an assistant
// message that requested tool calls, or the provider rejects the request.
// A tool_call event itself (informational -- FR2's transcript visibility)
// is intentionally not turned into a message here: the assistant_message
// event it accompanies already carries the identical call in its
// ToolCalls field. Any other/future event type is skipped rather than
// failing the whole turn.
func eventsToMessages(evs []events.Event) ([]llm.Message, error) {
	messages := make([]llm.Message, 0, len(evs))
	for _, ev := range evs {
		switch {
		case ev.Type == events.EventTypeUserMessage || ev.Type == events.EventTypeAssistantMessage ||
			strings.HasPrefix(ev.Type, events.EventTypeAssistantMessage+":"):
			var payload transcriptMessagePayload
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				return nil, fmt.Errorf("unmarshal message payload for event %s: %w", ev.EventID, err)
			}
			msg := llm.Message{
				Role:       llm.Role(payload.Role),
				Content:    payload.Content,
				ToolCallID: payload.ToolCallID,
			}
			if len(payload.ToolCalls) > 0 {
				msg.ToolCalls = make([]llm.ToolCall, len(payload.ToolCalls))
				for i, tc := range payload.ToolCalls {
					msg.ToolCalls[i] = llm.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
				}
			}
			messages = append(messages, msg)
		case strings.HasPrefix(ev.Type, events.EventTypeToolResult+":"):
			var payload toolResultEventPayload
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				return nil, fmt.Errorf("unmarshal tool result payload for event %s: %w", ev.EventID, err)
			}
			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				Content:    payload.Content,
				ToolCallID: payload.ToolCallID,
			})
		default:
			continue
		}
	}
	return messages, nil
}

// toolCallEventType/toolResultEventType derive the `type` column
// DispatchTool (activities.go) commits for one tool call's before/after
// transcript events -- see that method's doc comment for why callIndex
// must be folded in rather than using events.EventTypeToolCall/
// EventTypeToolResult verbatim: AppendIfAbsent's idempotency key is
// (session_id, turn, type) only, and a turn may carry more than one tool
// call. callIndex is a running count across the WHOLE external turn
// (workflow.go's processTurn), not reset per model response -- "add the
// inner tool loop" made a turn capable of more than one model response, so
// keeping callIndex turn-scoped rather than response-scoped is what keeps
// every dispatched call's pair of events distinct across iterations, with
// no format change needed here: a turn that never loops still produces the
// exact same "tool_call:0", "tool_call:1", ... sequence it always has,
// since callIndex and response-local position are identical when there is
// only one response.
func toolCallEventType(callIndex int) string {
	return fmt.Sprintf("%s:%d", events.EventTypeToolCall, callIndex)
}

func toolResultEventType(callIndex int) string {
	return fmt.Sprintf("%s:%d", events.EventTypeToolResult, callIndex)
}

// assistantMessageEventType derives the `type` column
// CommitToolLoopIteration (activities.go) commits for one non-final loop
// iteration's assistant-message event ("add the inner tool loop"): the
// final iteration of a turn (the response with no more tool calls) still
// commits under the bare EventTypeAssistantMessage constant via CommitTurn,
// unchanged from before this feature, so a turn that never loops produces
// the exact same single "assistant_message" event it always has. iteration
// is 0-based and counts only non-final loop iterations within the turn
// (workflow.go's processTurn), disjoint from that bare type -- the two
// never collide under AppendIfAbsent's (session_id, turn, type) key.
func assistantMessageEventType(iteration int) string {
	return fmt.Sprintf("%s:%d", events.EventTypeAssistantMessage, iteration)
}

// toolCallEventPayload is a tool_call transcript event's JSON payload
// (FR2): the model-requested call exactly as it arrived, before dispatch.
type toolCallEventPayload struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
}

// toolResultEventPayload is a tool_result transcript event's JSON payload
// (FR2): tools.Dispatcher.Dispatch's outcome verbatim, including the
// domain server's own IsError flag (dispatch.go's package doc comment,
// "isError is not a whagent-net failure") -- never reinterpreted here.
type toolResultEventPayload struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	IsError    bool   `json:"is_error"`
}

// marshalToolCallPayload converts an llm.ToolCall into the JSON payload
// DispatchTool commits for its tool_call transcript event.
func marshalToolCallPayload(call llm.ToolCall) (json.RawMessage, error) {
	raw, err := json.Marshal(toolCallEventPayload{
		ToolCallID: call.ID,
		Name:       call.Name,
		Arguments:  call.Arguments,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tool call payload: %w", err)
	}
	return raw, nil
}

// marshalToolResultPayload converts a tools.Result into the JSON payload
// DispatchTool commits for its tool_result transcript event.
func marshalToolResultPayload(result tools.Result) (json.RawMessage, error) {
	raw, err := json.Marshal(toolResultEventPayload{
		ToolCallID: result.ToolCallID,
		Name:       result.Name,
		Content:    result.Content,
		IsError:    result.IsError,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal tool result payload: %w", err)
	}
	return raw, nil
}
