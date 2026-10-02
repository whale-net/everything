// The lease countdown's re-upgrade on an htmx swap, checked as an
// attachment rather than as text. NFR 7b497d92's client-side rewrite is
// worth nothing if it only ever runs on the first paint: the board's own
// Refresh button and the scope control both swap fresh cards in over the
// page, and a countdown that does not re-run there leaves every lease
// showing a raw RFC3339 instant until the operator forces a reload.
//
// The earlier assertions on this script were substring checks, which
// cannot tell an attached listener from an unattached one -- the script
// carried its htmx:afterSwap registration verbatim while the && guard in
// front of it short-circuited every execution, and the suite stayed green.
// So this file resolves where each addEventListener is actually attached
// and whether anything conditional stands in front of it.

package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addListenerRE finds one addEventListener call and the event name it is
// given. The leading dot is part of the match so the receiver is whatever
// immediately precedes the dot.
var addListenerRE = regexp.MustCompile(`\.addEventListener\(\s*'([^']*)'`)

// listenerCall is one resolved registration: which event, on which
// receiver, and behind what guard.
type listenerCall struct {
	event    string
	receiver string
	guard    string
}

// headParseReachable reports whether the call attaches during the
// execution of a classic inline script sitting in <head>, before
// <body> exists. Two things decide it, and both were real defects once:
//
//   - the receiver has to exist that early. `document` and `window` do;
//     anything reached through `document.body` does not, and referencing it
//     there is the classic TypeError this script originally shipped with
//     the guard added over it.
//   - nothing conditional may stand in front of the call, or the
//     registration never happens. `document.body && document.body.addEventListener(...)`
//     is silent where the unguarded form throws, which is what made the
//     first version of this bug invisible rather than loud.
func (c listenerCall) headParseReachable() bool {
	if c.guard != "" {
		return false
	}
	return c.receiver == "document" || c.receiver == "window"
}

// listenerCalls resolves every addEventListener in a script.
func listenerCalls(script string) []listenerCall {
	matches := addListenerRE.FindAllStringSubmatchIndex(script, -1)
	calls := make([]listenerCall, 0, len(matches))
	for _, m := range matches {
		// The match starts at the dot before addEventListener, so the
		// receiver is the member chain ending there.
		receiver, recvStart := receiverBefore(script, m[0])
		calls = append(calls, listenerCall{
			event:    script[m[2]:m[3]],
			receiver: receiver,
			guard:    guardBefore(script, recvStart),
		})
	}
	return calls
}

// identRune is a character that can appear in the member chain ending at a
// call's dot: `document.body`, `window`, and the like.
func identRune(c byte) bool {
	return c == '.' || c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// receiverBefore returns the member chain ending at the dot index at,
// and the index it starts at. It stops at the first character that cannot
// continue the chain, so in `a.b&&a.b.addEventListener` it reads `a.b`
// and leaves the `&&` behind for guardBefore.
func receiverBefore(script string, at int) (string, int) {
	start := at
	for start > 0 && identRune(script[start-1]) {
		start--
	}
	return script[start:at], start
}

// guardBefore returns the conditional expression standing between the
// start of the statement holding the call and the call's receiver, or ""
// when the call is the whole statement. Both spellings of a conditional
// are covered: a short-circuit in the same statement, and an enclosing
// `if (...) {` block.
func guardBefore(script string, recvStart int) string {
	j := recvStart - 1
	for ; j >= 0; j-- {
		switch script[j] {
		case ' ', '\t', '\n', '\r':
			continue
		}
		break
	}
	if j < 0 {
		return ""
	}
	switch script[j] {
	case ';', '}':
		// The call opens its own statement: unconditional.
		return ""
	case '{':
		// The call is the first thing in a block; the block is only a
		// guard if an `if (...)` opened it.
		if cond, ok := ifConditionOpening(script, j); ok {
			return cond
		}
		return ""
	}
	// Anything else sits between the statement boundary and the call, so
	// the call is only reached when that expression evaluates truthy --
	// `a && a.addEventListener(...)` being the shipped one. Report the
	// whole expression, from the boundary up to the call.
	for k := j; k >= 0; k-- {
		if script[k] == ';' || script[k] == '{' || script[k] == '}' {
			return strings.TrimSpace(script[k+1 : recvStart])
		}
	}
	return strings.TrimSpace(script[:recvStart])
}

// ifConditionOpening returns the condition of the `if (...)` whose block
// begins at brace, if there is one.
func ifConditionOpening(script string, brace int) (string, bool) {
	j := brace - 1
	for j >= 0 && (script[j] == ' ' || script[j] == '\t' || script[j] == '\n' || script[j] == '\r') {
		j--
	}
	if j < 0 || script[j] != ')' {
		return "", false
	}
	close := j
	for j >= 0 && script[j] != '(' {
		j--
	}
	if j < 0 {
		return "", false
	}
	cond := script[j+1 : close]
	for k := j - 1; k >= 0; k-- {
		if script[k] == ' ' || script[k] == '\t' || script[k] == '\n' || script[k] == '\r' {
			continue
		}
		// `if` is the two characters ending at k.
		return cond, k >= 1 && script[k] == 'f' && script[k-1] == 'i'
	}
	return "", false
}

// TestLeaseCountdownAttachesItsHtmxListenerWhereItCanRun is the fix's own
// guard. buildHead interpolates leaseCountdownScript into a plain inline
// <script> that htmxbase renders from CustomHead, so it executes while the
// parser is still inside <head> and document.body is still null. A swap
// listener attached to document.body therefore never attaches, and the
// board's Refresh leaves every lease showing its absolute instant.
//
// The assertion resolves the attachment instead of matching its text, so
// reintroducing the guard -- in either spelling -- turns this red.
func TestLeaseCountdownAttachesItsHtmxListenerWhereItCanRun(t *testing.T) {
	f := newCardFixture()
	mux := f.mux(t)

	page := boardBody(t, mux, "")
	head := page[:strings.Index(page, "</head>")]
	require.NotEmpty(t, head, "the board page must render a head")

	// The premise the whole argument rests on: the countdown script runs
	// before <body> exists. If this ever stops holding, the attachment
	// below is no longer constrained and this test should be re-argued
	// rather than quietly deleted.
	scriptAt := strings.Index(head, "task-lease")
	require.NotEqual(t, -1, scriptAt, "the board page must ship the countdown script in its head")
	assert.Less(t, scriptAt, strings.Index(page, "<body"),
		"the countdown script runs inside <head>, so it cannot rely on document.body existing")

	calls := listenerCalls(head)
	require.NotEmpty(t, calls, "the head must register listeners; the analyzer found none to check")

	var afterSwap []listenerCall
	for _, c := range calls {
		if strings.HasPrefix(c.event, "htmx:") {
			afterSwap = append(afterSwap, c)
		}
	}
	require.NotEmpty(t, afterSwap,
		"the countdown must re-run after an htmx swap, or a Refresh silently reverts every lease to its absolute instant")

	for _, c := range afterSwap {
		assert.True(t, c.headParseReachable(),
			"the %q listener is attached to %q behind guard %q, so it never attaches during head parsing",
			c.event, c.receiver, c.guard)
	}

	// The re-upgrade has to look at what the swap brought in, not at the
	// document as a whole, or a partially-swapped page keeps stale text on
	// the part that was not replaced.
	assert.Contains(t, head, "htmx:afterSwap',function(e){upgrade(e.target);}",
		"the swap handler must upgrade the swapped subtree")
}

// TestListenerReachabilityAnalyzerBitesOnTheBugItWasWrittenFor keeps the
// analyzer honest. A reachability check that passes on the defect it was
// added for is worse than the substring check it replaced: it reads as
// coverage while catching nothing. These are the two spellings of the
// shipped bug, asserted to be unreachable, plus the fix asserted reachable.
func TestListenerReachabilityAnalyzerBitesOnTheBugItWasWrittenFor(t *testing.T) {
	unreachable := map[string]string{
		"short-circuit guard": `document.body&&document.body.addEventListener('htmx:afterSwap',up);`,
		"if-block guard":      `if(document.body){document.addEventListener('htmx:afterSwap',up);}`,
		"body receiver":       `document.body.addEventListener('htmx:afterSwap',up);`,
	}
	for name, script := range unreachable {
		t.Run(name, func(t *testing.T) {
			calls := listenerCalls(script)
			require.Len(t, calls, 1, "the analyzer must resolve the one registration")
			assert.False(t, calls[0].headParseReachable(),
				"%s must read as unreachable during head parsing", name)
		})
	}

	t.Run("the fix", func(t *testing.T) {
		calls := listenerCalls(`document.addEventListener('htmx:afterSwap',up);`)
		require.Len(t, calls, 1)
		assert.True(t, calls[0].headParseReachable(),
			"a listener on document is reachable during head parsing")
	})
}