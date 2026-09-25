package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/libs/go/logging"
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

// maxContextEvents bounds how many of a session's transcript events a
// BULK-mode turn's context selects, applied before the character budget
// below. It is a ceiling on the number of event IDs a turn records in its
// turn_context row -- a per-turn resource that does not shrink just
// because every event is tiny -- and not on the size of the request the
// provider receives; bulkModeContextBudget is what bounds that.
//
// A count is the wrong unit for request size, and was originally the only
// bound: a single tool_result event can carry tens of kilobytes, so a
// count ceiling never fires for exactly the sessions that need one. A dev
// session (2026-09-20, audience-score-system-research) accumulated 48
// events totalling 191KB inside a single turn -- far under this ceiling --
// and re-sent that whole transcript on every one of the turn's ~10 model
// calls, each of which then ran past CallModel's activity timeout and was
// retried, failing the session outright. Hence both bounds, count then
// characters.
//
// Search-mode turns are bounded by searchModeContextBudget alone and are
// deliberately NOT subject to this ceiling: that budget, not a count, is
// FR10's whole contract for them, and a session whose events are small
// enough to fit the budget must not be truncated anyway.
const maxContextEvents = 400

// bulkModeContextBudget is the per-turn character budget BuildContext
// charges a bulk-mode turn's transcript content against -- the bulk-mode
// half of the context-budgeting open item (ARCHITECTURE.md "Open items",
// "Context budgeting strategy"), closed the same way M4 (issue #2673,
// FR10) closed the search-mode half: with fitToBudget, in the same
// character unit as searchModeContextBudget, and for the same reason a
// count was insufficient (see maxContextEvents above).
//
// Value: the same 120,000 characters search mode uses -- ~30K tokens at a
// ~4-chars/token heuristic. Bulk mode's un-charged tool definitions (below)
// are what it has instead, so the two budgets are deliberately equal
// rather than tuned apart.
//
// Bulk mode is budgeted against transcript content only, not its tool
// definitions, because processTurn deliberately runs
// ActivityListToolDefinitions AFTER ActivityBuildContext for a bulk-mode
// turn (workflow.go) -- so no turn_tool_defs row exists yet when
// BuildContext runs, and reordering the two would need its own
// workflow.GetVersion change ID. The un-charged tool definitions are a
// stable per-agent cost rather than a per-turn one, so leaving them out
// costs a bounded, predictable slice of headroom. Summarization (rather
// than truncation) remains the larger, still-open half of the strategy.
const bulkModeContextBudget = 120_000

// transcriptReadPageSize bounds each Read call BuildContext issues while
// paging through the whole transcript before budgeting it down to the
// selected projection.
const transcriptReadPageSize = 200

// BuildContext is per-turn activity #2 (ARCHITECTURE.md "Session
// workflow"): appends the turn's new user-input event (the "turn's
// new-input event append" -- the one piece of this turn's context not
// already sitting in the transcript), then selects a budgeted projection
// over the transcript -- recent events plus the agent definition, fitted
// to bulkModeContextBudget (bulk mode) or searchModeContextBudget (search
// mode, FR10), both via fitToBudget -- and persists the exact ordered
// event-ID list that projection was built from into `turn_context` (LB1).
// The projection itself (the assembled llm.Message list) is derived and
// ephemeral and never enters workflow history or the `turn_context` row
// -- what BuildContextResult carries back to the workflow, and what gets
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
	if in.Definition.ToolLoadingMode == session.ToolLoadingModeSearch {
		// FR10 (issue #2673): a shared budget spanning this turn's tool
		// definitions and transcript content, and -- unlike the bulk-mode
		// branch below -- with no event-count ceiling layered on top.
		// toolDefs is re-read from the turn_tool_defs row
		// ActivityListToolDefinitions persisted earlier this turn (search
		// mode always resolves tools before BuildContext runs, workflow.go's
		// processTurn) rather than received as a BuildContextInput field --
		// see ListToolDefinitionsResult's doc comment (activities.go) for
		// why.
		toolDefs, err := readTurnToolDefs(ctx, a.Store, in.SessionID, in.Turn)
		if err != nil {
			return BuildContextResult{}, fmt.Errorf("build context: %w", err)
		}
		if overage := toolDefsCharge(toolDefs) - searchModeContextBudget; overage > 0 {
			logging.Get("worker").WarnContext(ctx, "search-mode tool definitions alone exceed the context budget; keeping a minimal event floor instead of the full budgeted projection",
				"session_id", in.SessionID, "turn", in.Turn, "overage_chars", overage)
		}
		all = fitToBudget(toolDefs, all, searchModeContextBudget)
	} else {
		// Bulk mode, two bounds. The count ceiling is applied first and
		// unchanged: it bounds how many event IDs land in the turn_context
		// row (a per-turn resource independent of content size), not how
		// big the request is. The character budget is applied second and
		// is what actually bounds the request. Order matters -- the
		// count truncation can itself cut between an assistant message and
		// its tool results, so it has to run before fitToBudget, whose
		// trimOrphanedToolResults is the last thing to touch the window.
		if len(all) > maxContextEvents {
			all = all[len(all)-maxContextEvents:]
		}
		// Tool definitions are deliberately not charged here -- this
		// branch runs before ActivityListToolDefinitions for a bulk-mode
		// turn, so no turn_tool_defs row exists yet (see
		// bulkModeContextBudget).
		all = fitToBudget(nil, all, bulkModeContextBudget)
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

// readTurnToolDefs re-reads the tool list ActivityListToolDefinitions
// persisted for (sessionID, turn) via SaveTurnToolDefs (session/
// transcript.go) -- the activity-internal read half of "Activity payload
// discipline" (ARCHITECTURE.md): CallModel and, for a search-mode turn,
// BuildContext both call this instead of receiving the tool list as their
// own activity input, so the full catalog crosses the Temporal workflow
// boundary once per turn (as ListToolDefinitions' own input/nothing-result)
// rather than once per turn PLUS once per caller PLUS once per tool-loop
// iteration. No row (no tool_set configured, or a session predating
// ListToolDefinitions) returns a nil slice, not an error -- "no tools
// attached" is an ordinary state, not a fault.
func readTurnToolDefs(ctx context.Context, store *session.Store, sessionID uuid.UUID, turn int) ([]llm.ToolDefinition, error) {
	encoded, ok, err := store.Transcript().ReadTurnToolDefs(ctx, sessionID, turn)
	if err != nil {
		return nil, fmt.Errorf("read turn tool defs: %w", err)
	}
	if !ok {
		return nil, nil
	}
	var toolDefs []llm.ToolDefinition
	if err := json.Unmarshal(encoded, &toolDefs); err != nil {
		return nil, fmt.Errorf("decode turn tool defs: %w", err)
	}
	return toolDefs, nil
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
// ToolCalls field. A tool_unlock event (toolUnlockEventType's
// "tool_unlock:<call_index>" prefix, FR5/FR6) is likewise intentionally
// not turned into a message here: it is whagent-net's own bookkeeping for
// UnlockedTools (activities.go) to read back, not part of the model's view
// of a search_tools call -- that view is exactly the tool_call/tool_result
// pair above. Both fall through the switch's default case below. Any
// other/future event type is skipped rather than failing the whole turn.
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
				Role: llm.RoleTool,
				// A domain MCP server's result is whatever it chose to
				// return, and one list-style call can run to tens of
				// kilobytes -- the dev transcript's largest was 84KB.
				// Clamp it here, at the one place the content becomes a
				// message, so a single fat result degrades one message
				// instead of crowding every other message out of the
				// budget. The transcript event itself keeps its full
				// body (LB1); only the model's view of it is clamped.
				Content:    clampToolResultContent(payload.Content),
				ToolCallID: payload.ToolCallID,
			})
		default:
			continue
		}
	}
	return hoistAssistantToolCalls(messages), nil
}

// withSystemPrompt prepends prompt to msgs as a RoleSystem message when set
// (AgentDefinition.SystemPrompt, session/agentdef.go), and returns msgs
// unchanged otherwise -- CallModel's (activities.go) one call site, kept
// separate from eventsToMessages since the system prompt is a property of
// the agent definition, not of the transcript projection.
func withSystemPrompt(msgs []llm.Message, prompt *string) []llm.Message {
	if prompt == nil || *prompt == "" {
		return msgs
	}
	return append([]llm.Message{{Role: llm.RoleSystem, Content: *prompt}}, msgs...)
}

// hoistAssistantToolCalls moves an assistant message that carries tool
// calls to immediately BEFORE the run of tool results answering it, when
// the transcript committed them in the other order.
//
// processTurn dispatches a response's tool calls and only afterwards
// commits the assistant_message carrying them (workflow.go) -- the order
// ARCHITECTURE.md's "Session workflow" step 4 describes. So one tool-loop
// iteration's transcript segment is seq-ordered
// [tool_result:N ... assistant_message:I(tool_calls=[N...])], and rendered
// in that order a `tool` message PRECEDES the assistant message it
// answers. The provider requires the opposite: a tool message must respond
// to a tool call in a preceding assistant message. Left alone this is a
// hard 400 on every model call after a turn's first tool use, and the
// malformed events stay in the transcript, so it poisons the next turn too
// -- one tool call and the session can never complete another model call.
// It has only gone unnoticed because the model used in dev tolerated it.
//
// Context is a projection over the transcript ("Three nouns": derived,
// rebuilt every turn, never stored), so the projection is the right place
// to repair this: the transcript keeps the order it actually recorded, and
// sessions already stored in the broken order are repaired too rather than
// only new ones.
//
// Deliberately narrow, and deterministic: the walk back stops at the first
// result this message does NOT answer -- that one belongs to an earlier
// assistant message and stays put -- so a message only ever moves earlier
// past results it itself answers, and never past anything else. Reordering
// is therefore a pure function of the transcript, which matters for prompt
// caching: a turn's already-rendered messages keep their exact relative
// order as the session grows, so the prefix does not churn between turns.
func hoistAssistantToolCalls(msgs []llm.Message) []llm.Message {
	out := append([]llm.Message(nil), msgs...)
	for j := range out {
		if out[j].Role != llm.RoleAssistant || len(out[j].ToolCalls) == 0 {
			continue
		}
		answered := make(map[string]bool, len(out[j].ToolCalls))
		for _, tc := range out[j].ToolCalls {
			answered[tc.ID] = true
		}
		// Walk back over the contiguous run of results this message
		// answers. Consecutive iterations' results abut in the transcript,
		// so requiring the whole preceding run to match would refuse to
		// repair the second of two iterations once the first was repaired;
		// stopping at the first foreign result is what makes one pass over
		// the list enough.
		i := j
		for i > 0 && out[i-1].Role == llm.RoleTool && answered[out[i-1].ToolCallID] {
			i--
		}
		if i == j {
			continue // already in protocol order
		}
		// Shift the run one slot right (copy is memmove, so the overlap is
		// safe) and drop the assistant message into the vacated slot.
		assistant := out[j]
		copy(out[i+1:j+1], out[i:j])
		out[i] = assistant
	}
	return out
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

// toolUnlockEventType derives the `type` column a search_tools call's
// tool_unlock event is committed under (events.go's EventTypeToolUnlock doc
// comment) -- the same reasoning as toolCallEventType/toolResultEventType
// above applies: AppendIfAbsent's idempotency key is (session_id, turn,
// type) only, and one turn may carry more than one search_tools call.
// callIndex must be the same turn-scoped call index used for that call's
// paired tool_call/tool_result events, so the three events of one
// search_tools call are trivially correlatable by index.
func toolUnlockEventType(callIndex int) string {
	return fmt.Sprintf("%s:%d", events.EventTypeToolUnlock, callIndex)
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

// toolUnlockEventPayload is a tool_unlock transcript event's JSON payload
// (FR5, FR6): the tool names a search_tools call sticky-unlocked for the
// rest of the session, plus the query that produced them.
type toolUnlockEventPayload struct {
	ToolNames []string `json:"tool_names"`
	Query     string   `json:"query"`
}

// marshalToolUnlockPayload converts a search_tools call's unlocked tool
// names and query into the JSON payload committed for its tool_unlock
// transcript event.
func marshalToolUnlockPayload(toolNames []string, query string) (json.RawMessage, error) {
	raw, err := json.Marshal(toolUnlockEventPayload{ToolNames: toolNames, Query: query})
	if err != nil {
		return nil, fmt.Errorf("marshal tool unlock payload: %w", err)
	}
	return raw, nil
}
