package main

import (
	"encoding/json"
	"fmt"
	htmlpkg "html"
	"io"
	"strings"
)

// esc HTML-escapes text pulled from agent output before it is spliced into a
// hand-built HTML fragment.
func esc(s string) string { return htmlpkg.EscapeString(s) }

// sanitizeID turns an arbitrary pi tool-call id into something safe to use
// as an HTML id / CSS selector.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

// transcriptState tracks just enough per-SSE-connection state to turn a
// stream of pi RPC events into incremental HTML fragments.
type transcriptState struct {
	assistantSeq int
	currentMsgID string
	toolNames    map[string]string
}

func newTranscriptState() *transcriptState {
	return &transcriptState{toolNames: map[string]string{}}
}

type rpcEvent struct {
	Type    string `json:"type"`
	Message *struct {
		Role string `json:"role"`
	} `json:"message"`
	AssistantMessageEvent *struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	IsError    bool   `json:"isError"`
	Success    *bool  `json:"success"`
	Error      string `json:"error"`
	Command    string `json:"command"`
}

// renderEvent turns one raw pi RPC event line into an HTML fragment made
// entirely of hx-swap-oob elements, or "" if the event isn't rendered.
// See https://pi.dev/docs/latest/rpc for the event shapes handled here.
func renderEvent(state *transcriptState, payload string) string {
	var e rpcEvent
	if err := json.Unmarshal([]byte(payload), &e); err != nil {
		return ""
	}

	switch e.Type {
	case "agent_start":
		return `<span id="status" hx-swap-oob="innerHTML">working…</span>`

	case "agent_settled":
		return `<span id="status" hx-swap-oob="innerHTML">idle</span>`

	case "message_start":
		if e.Message == nil || e.Message.Role != "assistant" {
			return ""
		}
		state.assistantSeq++
		state.currentMsgID = fmt.Sprintf("msg-%d", state.assistantSeq)
		return fmt.Sprintf(`<div id="%s" class="bubble assistant" hx-swap-oob="beforeend:#transcript"></div>`, state.currentMsgID)

	case "message_update":
		if e.AssistantMessageEvent == nil || e.AssistantMessageEvent.Type != "text_delta" || state.currentMsgID == "" {
			return ""
		}
		return fmt.Sprintf(`<span hx-swap-oob="beforeend:#%s">%s</span>`, state.currentMsgID, esc(e.AssistantMessageEvent.Delta))

	case "tool_execution_start":
		id := "tool-" + sanitizeID(e.ToolCallID)
		state.toolNames[e.ToolCallID] = e.ToolName
		return fmt.Sprintf(`<div id="%s" class="bubble tool" hx-swap-oob="beforeend:#transcript">&#9656; running %s&hellip;</div>`, id, esc(e.ToolName))

	case "tool_execution_end":
		id := "tool-" + sanitizeID(e.ToolCallID)
		name := state.toolNames[e.ToolCallID]
		class, mark := "bubble tool", "&#10003;"
		if e.IsError {
			class, mark = "bubble tool error", "&#10007;"
		}
		return fmt.Sprintf(`<div id="%s" class="%s" hx-swap-oob="outerHTML:#%s">%s %s finished</div>`, id, class, id, mark, esc(name))

	case "response":
		if e.Success != nil && !*e.Success {
			return fmt.Sprintf(`<div class="bubble system error" hx-swap-oob="beforeend:#transcript">%s failed: %s</div>`, esc(e.Command), esc(e.Error))
		}
		return ""

	case "extension_error":
		return fmt.Sprintf(`<div class="bubble system error" hx-swap-oob="beforeend:#transcript">extension error: %s</div>`, esc(e.Error))

	default:
		return ""
	}
}

// writeSSE writes one named SSE event whose data may span multiple lines.
func writeSSE(w io.Writer, event, data string) {
	fmt.Fprintf(w, "event: %s\n", event)
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}
