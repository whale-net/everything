package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxui"
)

// TestBuildHead_ThemesCSSLoadsAfterDaisyUI guards the load-order trap
// documented in htmxui ARCHITECTURE §10: ThemesCSS maps daisyUI's CSS
// variables to krill's palette, so it must load *after* the daisyUI
// stylesheet. Loading it first makes the override silently lose to
// daisyUI's defaults -- no error, just wrong colours.
func TestBuildHead_ThemesCSSLoadsAfterDaisyUI(t *testing.T) {
	head := buildHead()

	daisyUIIdx := strings.Index(head, `<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui`)
	themesIdx := strings.Index(head, "<style>"+htmxui.ThemesCSS+"</style>")
	if daisyUIIdx < 0 {
		t.Fatalf("expected the pinned daisyUI stylesheet in head, got: %s", head)
	}
	if themesIdx < 0 {
		t.Fatalf("expected the htmxui ThemesCSS in head, got: %s", head)
	}
	if daisyUIIdx > themesIdx {
		t.Errorf("ThemesCSS (index %d) must load after the daisyUI stylesheet (index %d), or the palette override silently loses",
			themesIdx, daisyUIIdx)
	}
}

// TestBuildHead_PinsTheSameVersionsAsTheRestOfTheRepo pins the CDN
// versions so one app drifting off them is visible here rather than as a
// visual difference nobody attributes to a version bump.
func TestBuildHead_PinsTheSameVersionsAsTheRestOfTheRepo(t *testing.T) {
	head := buildHead()
	for _, want := range []string{
		"@tailwindcss/browser@4.3.3",
		"daisyui@5.6.18",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("expected pinned %s in head, got: %s", want, head)
		}
	}
}

// TestBuildHead_ThemeBootstrapReadsTheSharedStorageKey guards that the
// no-FOUC bootstrap reads the same localStorage key htmxui's
// ThemeSwitcher writes, or the operator's chosen theme is overwritten on
// the next page load.
func TestBuildHead_ThemeBootstrapReadsTheSharedStorageKey(t *testing.T) {
	head := buildHead()
	if !strings.Contains(head, `"`+htmxui.ThemeSwitcherStorageKey+`"`) {
		t.Errorf("expected the bootstrap to read htmxui.ThemeSwitcherStorageKey (%q), got: %s",
			htmxui.ThemeSwitcherStorageKey, head)
	}
	// The regression htmxui guards against in its own test: a theme
	// switch must not need a full reload to take effect.
	if strings.Contains(head, "location.reload") {
		t.Errorf("the theme bootstrap must not force a reload, got: %s", head)
	}
}

// TestBuildHead_RelativeAgeBindsItsListenerWhereItRuns guards the load-time
// trap in relativeAgeScript: it is emitted into the HEAD, where <body> does
// not exist yet, so a document.body guard evaluates false and binds nothing.
//
// The consequence is silent and specific: the DOMContentLoaded upgrade runs,
// so the first paint says "Updated 3 seconds ago"; but the after:swap listener
// never binds, so every later in-place swap -- a filter change on the Tasks
// page, a Refresh -- leaves the element showing the absolute instant the
// server rendered. Nothing anywhere reports an error.
//
// Asserting the binding is on `document` is what makes this a test rather
// than a substring check: the line could be deleted outright and a naive
// "does the script mention afterSwap" assertion would stay green.
func TestBuildHead_RelativeAgeBindsItsListenerWhereItRuns(t *testing.T) {
	head := buildHead()

	const listen = "addEventListener('htmx:after:swap'"
	if !strings.Contains(head, listen) {
		t.Fatalf("the relative-age script no longer listens for htmx:after:swap at all, got: %s", head)
	}
	if strings.Contains(head, "document.body&&document.body."+listen) {
		t.Errorf("the after:swap listener is guarded on document.body, which does not exist "+
			"when a head script runs -- it can never bind, and every in-place swap falls back "+
			"to the absolute instant: %s", head)
	}
}

// TestHeadScripts_ListensForHtmx4SwapEvents pins the event NAME, which the
// attachment checks above cannot reach: a listener bound to an event htmx
// never dispatches is attached, unguarded, on document -- every invariant
// they assert holds, and the upgrade still never runs. htmx 4.0.0 (the build
// htmxbase serves) renamed its lifecycle events to the colon form, so
// `htmx:afterSwap` binds nothing at all and the first in-place swap reverts
// every lease to its RFC3339 instant and "Updated N ago" to the same.
//
// The shape follows libs/go/htmxsse/liveindicator's
// TestLiveIndicator_ListensForHtmx4SSEEvents: the good name present AND the
// 1.x name absent. The negative half is the load-bearing one -- presence
// alone would still pass against a script carrying both spellings.
func TestHeadScripts_ListensForHtmx4SwapEvents(t *testing.T) {
	const htmx4Event = "addEventListener('htmx:after:swap'"
	const htmx1Event = "addEventListener('htmx:afterSwap'"

	for name, script := range map[string]string{
		"copyTaskIdScript":     copyTaskIdScript,
		"leaseCountdownScript": leaseCountdownScript,
		"relativeAgeScript":    relativeAgeScript,
	} {
		if !strings.Contains(script, htmx4Event) {
			t.Errorf("%s no longer listens for htmx:after:swap, so a swap re-runs no upgrade, got: %s",
				name, script)
		}
		if strings.Contains(script, htmx1Event) {
			t.Errorf("%s still binds htmx:afterSwap: htmx 1.x event names no longer fire, got: %s",
				name, script)
		}
	}

	// Head-wide, so a camelCase binding added by a third head script is
	// caught here rather than only by whichever test owns that script.
	head := buildHead()
	if strings.Contains(head, "htmx:afterSwap") {
		t.Errorf("the head binds a 1.x camelCase htmx event: htmx 1.x event names no longer fire, got: %s", head)
	}
	if !strings.Contains(head, htmx4Event) {
		t.Errorf("the head never listens for htmx:after:swap, so no swap re-runs a head upgrade, got: %s", head)
	}
}

// TestCopyTaskIdScript_IsEmittedInCustomHead pins where the copy behaviour
// lives. buildHead's return value is CustomHead and nothing else, so
// containment in it is the claim: the script is emitted in the document head
// rather than inline in the rail.
//
// That placement is the whole reason it survives a tab swap. The rail is a
// fragment the tab strip and the Refresh button re-request, so a script
// inside it is thrown away with the swap and the chip goes back to dead
// until the next full page load.
func TestCopyTaskIdScript_IsEmittedInCustomHead(t *testing.T) {
	head := buildHead()
	if !strings.Contains(head, copyTaskIdScript) {
		t.Errorf("copyTaskIdScript is no longer emitted in CustomHead, so the chip never gets its "+
			"behaviour -- or loses it on the first swap. head: %s", head)
	}
}

// TestCopyTaskIdScript_BindsItsListenersWhereItRuns is the copy script's
// version of the document-body trap guarded above for relativeAgeScript. The
// script is emitted from CustomHead, so a classic inline script there runs
// while the parser is still inside <head> and document.body is still null: a
// document.body guard evaluates false, binds nothing, and the symptom is
// that DOMContentLoaded fires with no handlers at all.
func TestCopyTaskIdScript_BindsItsListenersWhereItRuns(t *testing.T) {
	for _, guard := range []string{
		"document.body.addEventListener",
		"document.body&&",
	} {
		if strings.Contains(copyTaskIdScript, guard) {
			t.Errorf("the copy script binds through %q, which does not exist when a head script "+
				"runs -- it can never bind, and the chip stays disabled. script: %s",
				guard, copyTaskIdScript)
		}
	}
	if n := strings.Count(copyTaskIdScript, "document.addEventListener"); n != 2 {
		t.Errorf("the copy script binds %d listeners on document; it needs both DOMContentLoaded "+
			"and htmx:after:swap so a swapped-in chip is wired too. script: %s", n, copyTaskIdScript)
	}
}

// TestCopyTaskIdScript_SelectsTheChipByItsShippedHook keeps the script and
// the markup in step. The rail test asserts the hook is on the button; this
// asserts the script looks for it, so renaming one side without the other
// is a failure rather than a control that silently stops copying.
func TestCopyTaskIdScript_SelectsTheChipByItsShippedHook(t *testing.T) {
	const hook = `[data-krill="copy-task-id"]`
	if !strings.Contains(copyTaskIdScript, hook) {
		t.Errorf("the copy script no longer selects %s, so it cannot find the chip. script: %s",
			hook, copyTaskIdScript)
	}
	if !strings.Contains(copyTaskIdScript, `data-krill="copy-task-id-status"`) {
		t.Errorf("the copy script no longer looks for the status element it renders its "+
			"confirmation into, so a click would copy with nothing to confirm it. script: %s",
			copyTaskIdScript)
	}
}

// TestCopyTaskIdScript_DegradesLoudlyWithoutAClipboard is the load-bearing
// half of this task's brief: navigator.clipboard is undefined on an insecure
// origin and writeText rejects when the clipboard permission is denied, so
// the unguarded form -- the two-line pattern used elsewhere in this repo --
// either throws or resolves into silence. Silence is the one unacceptable
// outcome, because an operator who clicks a copy button and sees nothing has
// been told the id is gone when it is not.
func TestCopyTaskIdScript_DegradesLoudlyWithoutAClipboard(t *testing.T) {
	// The capability check, so an insecure origin is a branch rather than a
	// TypeError thrown out of the click handler.
	for _, want := range []string{
		"navigator.clipboard",                // the guarded read
		"typeof clip.writeText!=='function'", // absent API -> same path as a denial
	} {
		if !strings.Contains(copyTaskIdScript, want) {
			t.Errorf("the copy script lost %q: without it an insecure origin throws instead of "+
				"taking the failure path. script: %s", want, copyTaskIdScript)
		}
	}

	// Both the missing-API branch and the rejected-promise branch have to say
	// something. The rejection handler is the one that matters most: a denied
	// clipboard permission is a promise that rejects, which code that only
	// writes a .then(success) silently swallows.
	if !strings.Contains(copyTaskIdScript, `},function(){`) {
		t.Errorf("the copy script no longer passes a rejection handler to writeText, so a denied "+
			"clipboard permission resolves into silence. script: %s", copyTaskIdScript)
	}
	// And the failure has to leave the operator able to get the id anyway,
	// rather than only telling them it went wrong.
	if !strings.Contains(copyTaskIdScript, "selectNodeContents") {
		t.Errorf("the copy script no longer selects the chip's own text on failure, so the "+
			"operator is told the copy failed with no route to the id. script: %s", copyTaskIdScript)
	}
}

// TestCopyTaskIdScript_AnnouncesWithoutStealingFocus guards the confirmation
// against the two ways it goes wrong: an alert or a toast that grabs focus
// mid-task, and a state that lingers over the id it is confirming.
func TestCopyTaskIdScript_AnnouncesWithoutStealingFocus(t *testing.T) {
	for _, forbidden := range []string{
		".focus(",
		"alert(",
		"confirm(",
		"showToast",
	} {
		if strings.Contains(copyTaskIdScript, forbidden) {
			t.Errorf("the copy script calls %s; the confirmation must be a polite live region "+
				"that neither steals focus nor interrupts. script: %s", forbidden, copyTaskIdScript)
		}
	}
	// The confirmation is a word, not a colour: the announced text names the
	// outcome, so the message survives a monochrome or a colour-blind read.
	if !strings.Contains(copyTaskIdScript, `'Copied'`) {
		t.Errorf("the success confirmation carries no word, leaving the announcement to a "+
			"colour alone. script: %s", copyTaskIdScript)
	}
	// Brief, not permanent: a confirmation that never clears is a row that
	// stops being about the id.
	if !strings.Contains(copyTaskIdScript, "clearTimeout") {
		t.Errorf("the copy script never clears its confirmation, so the state lingers. script: %s",
			copyTaskIdScript)
	}
	// And it must not survive the node being swapped out from under it.
	if !strings.Contains(copyTaskIdScript, "btn.isConnected") {
		t.Errorf("the copy script's reset does not check the chip is still in the document, so a "+
			"pending timer can write into a detached node. script: %s", copyTaskIdScript)
	}
}

// TestCopyTaskIdScript_WritesOnlyToTheOperatorsClipboard pins the read-only
// page. The copy is a client-side clipboard write; nothing here may talk to
// krill, and a htmx verb or a fetch added later would turn a read-only detail
// page into a writer without anybody deciding that it should.
func TestCopyTaskIdScript_WritesOnlyToTheOperatorsClipboard(t *testing.T) {
	for _, forbidden := range []string{
		"fetch(",
		"XMLHttpRequest",
		"hx-post",
		"hx-put",
		"hx-delete",
	} {
		if strings.Contains(copyTaskIdScript, forbidden) {
			t.Errorf("the copy script reaches for %s; this page is read-only and the copy targets "+
				"the operator's clipboard, not krill. script: %s", forbidden, copyTaskIdScript)
		}
	}
}

// TestCopyTaskIdScript_BindsEachChipOnce guards the double-binding. The
// upgrade runs on every htmx swap, and the after:swap listener walks the
// swapped subtree -- so a chip reachable from two nested swapped fragments
// gets bound twice and announces twice per click, which reads as the copy
// failing.
func TestCopyTaskIdScript_BindsEachChipOnce(t *testing.T) {
	// The guard expression, not just the hook name: a script that still
	// mentioned data-krill-bound while never testing it would pass a
	// presence check and rebind on every swap.
	const guard = "getAttribute('data-krill-bound')==='1'"
	if !strings.Contains(copyTaskIdScript, guard) {
		t.Errorf("the copy script has no bind-once guard (%s), so a chip reachable from two swapped "+
			"fragments collects two click listeners and announces twice per click. script: %s",
			guard, copyTaskIdScript)
	}
	// And the marker is actually written, so the guard is never vacuously
	// true-and-never-set.
	if !strings.Contains(copyTaskIdScript, "setAttribute('data-krill-bound','1')") {
		t.Errorf("the copy script tests its bind-once marker but never sets it, so the guard can "+
			"never be true and every chip rebinds on every swap. script: %s", copyTaskIdScript)
	}
}

// copyScriptIndex returns the offset of needle in the copy script, failing
// the test when it is absent. Every structural assertion below is about
// ORDER or SHAPE rather than mere presence, and an order assertion needs a
// definite offset on both sides: an absent needle makes the comparison
// vacuous, which is how "the guard runs before the bind" passes against a
// script that has no guard at all.
func copyScriptIndex(t *testing.T, needle string) int {
	t.Helper()
	i := strings.Index(copyTaskIdScript, needle)
	if i < 0 {
		t.Fatalf("the copy script no longer contains %q, so this test's ordering assertion has "+
			"nothing to compare and would pass vacuously. script: %s", needle, copyTaskIdScript)
	}
	return i
}

// TestCopyTaskIdScript_GuardsBeforeItBinds is the bind-once rule as an
// ORDER rather than a presence. The presence test above proves the guard
// exists and that the marker is written; neither says the guard runs FIRST.
//
// The order is the whole mechanism. Checked after the listener is attached,
// a chip reachable from two nested swapped fragments would still collect two
// listeners on the first pass -- the guard would only bite on the second --
// and both would announce, which reads to the operator as a copy that half
// worked.
func TestCopyTaskIdScript_GuardsBeforeItBinds(t *testing.T) {
	guard := copyScriptIndex(t, "getAttribute('data-krill-bound')==='1'")
	mark := copyScriptIndex(t, "setAttribute('data-krill-bound','1')")
	listen := copyScriptIndex(t, "addEventListener('click'")

	if guard > mark {
		t.Errorf("the bind-once guard (offset %d) runs AFTER the marker is written (offset %d), so "+
			"the check can never be true and every chip rebinds on every swap. script: %s",
			guard, mark, copyTaskIdScript)
	}
	if mark > listen {
		t.Errorf("the bind-once marker (offset %d) is written AFTER the click listener is attached "+
			"(offset %d), so a chip reachable from two nested swapped fragments collects two "+
			"listeners and announces twice per click. script: %s",
			mark, listen, copyTaskIdScript)
	}
	// The guard has to be an early return, not a condition that merely skips
	// the marker: a chip that tested the attribute and then fell through would
	// re-attach its listener on every swap while still looking guarded.
	if !strings.Contains(copyTaskIdScript, "==='1'){return;}") {
		t.Errorf("the bind-once guard is not an early return, so a chip that already tested bound "+
			"still falls through and attaches a second listener. script: %s", copyTaskIdScript)
	}
}

// TestCopyTaskIdScript_MarksTheChipBoundBeforeAnythingElse pins the other
// half of the swap story: the chip arrives from every swap as FRESH markup --
// server-rendered, disabled, unbound -- and only the after:swap upgrade can
// revive it. If the upgrade ran but never removed `disabled`, the chip would
// sit there looking correct in the DOM and refusing every click, with nothing
// on the page to say why.
func TestCopyTaskIdScript_EnablesTheChipInsideTheBindItGuards(t *testing.T) {
	guard := copyScriptIndex(t, "getAttribute('data-krill-bound')==='1'")
	enable := copyScriptIndex(t, "btn.removeAttribute('disabled')")
	listen := copyScriptIndex(t, "addEventListener('click'")

	// After the guard: a chip already bound must not be re-enabled either --
	// that is harmless, but it means the enable sits inside the guarded
	// region rather than outside it.
	if enable < guard {
		t.Errorf("the chip is enabled (offset %d) BEFORE the bind-once guard (offset %d), so the "+
			"enable is outside the guarded region. script: %s", enable, guard, copyTaskIdScript)
	}
	// And before the listener: enabling after attaching would leave a window
	// in which a click on a still-disabled button does nothing.
	if enable > listen {
		t.Errorf("the chip is enabled (offset %d) only AFTER its click listener is attached (offset "+
			"%d). script: %s", enable, listen, copyTaskIdScript)
	}
	// The title is swapped too, and on the same pass: a chip whose title still
	// says "needs JavaScript" while it works is telling the operator the
	// opposite of the truth.
	if strings.Contains(copyTaskIdScript, "needs JavaScript") {
		t.Errorf("the copy script still carries the disabled chip's \"needs JavaScript\" title text; "+
			"the script must replace it, not ship it. script: %s", copyTaskIdScript)
	}
	if !strings.Contains(copyTaskIdScript, "btn.setAttribute('title','Copy the task id to your clipboard')") {
		t.Errorf("the script enables the chip but never retitles it, so it keeps promising it needs "+
			"JavaScript while working. script: %s", copyTaskIdScript)
	}
}

// TestCopyTaskIdScript_ResetsOnlyWhileTheChipIsStillOnThePage is the
// detached-node case, as an ORDER. The reset writes into the status element
// and removes the chip's state attribute; both are writes to nodes that a
// swap may have taken away.
//
// The guard is present (asserted elsewhere), but a guard that runs AFTER the
// first write has already written into a detached node -- the exception is
// swallowed and, worse, a browser that keeps the detached subtree alive lets
// the stale text sit in a node an operator may still be reading through a
// cached back-navigation.
func TestCopyTaskIdScript_ResetsOnlyWhileTheChipIsStillOnThePage(t *testing.T) {
	timer := copyScriptIndex(t, "setTimeout(function()")
	guard := copyScriptIndex(t, "btn.isConnected")
	write := copyScriptIndex(t, "s.textContent=''")

	if guard < timer {
		t.Errorf("the isConnected guard (offset %d) runs BEFORE the timer (offset %d), so it checks "+
			"the node once at schedule time and a swap after that is unguarded. script: %s",
			guard, timer, copyTaskIdScript)
	}
	if guard > write {
		t.Errorf("the reset writes into the status element (offset %d) BEFORE checking the chip is "+
			"still connected (offset %d), so a swap mid-flight takes the write. script: %s",
			write, guard, copyTaskIdScript)
	}
	// And a second click must not leave the first timer running to clear the
	// second confirmation early: the operator clicks twice and the "Copied"
	// vanishes while they are still looking at it.
	clear := strings.Index(copyTaskIdScript, "clearTimeout")
	set := strings.Index(copyTaskIdScript, "setTimeout(")
	if clear < 0 {
		t.Errorf("a second click leaves the first reset timer armed, so it clears the second "+
			"confirmation early. script: %s", copyTaskIdScript)
	} else if set < 0 || clear > set {
		t.Errorf("the pending reset (offset %d) is not cleared before the next one is armed "+
			"(offset %d), so a second click's confirmation is cleared by the first click's timer. "+
			"script: %s", clear, set, copyTaskIdScript)
	}
}

// TestCopyTaskIdScript_ConfirmsBothOutcomesInWords guards the "not by colour
// alone" clause for BOTH states rather than the success half only. The
// existing presence test proves the word "Copied" exists somewhere; this
// proves each tone is paired with a message, so removing the failure message
// leaves the failure announced by text-error and nothing else -- invisible to
// a monochrome display and silent to a reader who never sees the colour.
func TestCopyTaskIdScript_ConfirmsBothOutcomesInWords(t *testing.T) {
	// Every tone key must reach a message, and every message must reach a
	// textContent write. Pairing them by offset is what makes this an
	// assertion about the pairing rather than two independent presences.
	for state, msg := range map[string]string{
		"copied": "announce(btn,'copied','Copied')",
		"failed": "announce(btn,'failed','Could not copy. The id is selected - press Ctrl+C.')",
	} {
		call := strings.Index(copyTaskIdScript, msg)
		if call < 0 {
			t.Errorf("the %s state has no message: %q. Without one the outcome is conveyed by the "+
				"%s colour alone. script: %s", state, msg, state, copyTaskIdScript)
			continue
		}
		// The announce helper is what puts text in the live region; a state
		// whose announce call sits outside the helper cannot be read out.
		if helper := strings.Index(copyTaskIdScript, "function announce("); helper < 0 || helper > call {
			t.Errorf("the %s message is announced from outside announce(): %s", state, copyTaskIdScript)
		}
	}
	// And the tone map must key both states, or TONE[state] is undefined and
	// the className write lands as the string "undefined".
	for _, state := range []string{"copied", "failed"} {
		key := state + ":'"
		if !strings.Contains(copyTaskIdScript, key) {
			t.Errorf("the tone map has no %q entry, so announcing the %s state writes the literal "+
				"string 'undefined' into the confirmation's class. script: %s",
				state, state, copyTaskIdScript)
		}
	}
}

// TestCopyTaskIdScript_PrefersTheIdAttributeOverTheButtonText is the id
// source. The attribute is what the script reads because the button's own
// text is the id the operator sees -- but the button's text also carries
// whatever the status write or a daisyUI badge put there, so a chip that read
// its text would copy a string that is not an id.
//
// The order is the claim: the attribute first, the text only as the fallback
// for a chip that somehow lost it, and the fallback trimmed (the template
// renders the id on its own line, so the raw text carries whitespace).
func TestCopyTaskIdScript_PrefersTheIdAttributeOverTheButtonText(t *testing.T) {
	read := copyScriptIndex(t, "var id=btn.getAttribute('data-task-id')||(btn.textContent||'').trim();")
	write := copyScriptIndex(t, "var p=writeId(btn);")
	clip := copyScriptIndex(t, "clip.writeText(id)")

	// Inside writeId the id is read before it is written: a helper that
	// passed something else to writeText would resolve and announce "Copied"
	// having copied nothing.
	if read > clip {
		t.Errorf("the chip's id is read (offset %d) only after writeText (offset %d) is handed "+
			"something, so what lands on the clipboard is not the id. script: %s",
			read, clip, copyTaskIdScript)
	}
	// And the handler reads it through that helper rather than reaching for
	// the clipboard itself, so the "which id" rule lives in one place.
	if read > write {
		t.Errorf("the chip's id is read (offset %d) only after the click handler (offset %d) has "+
			"already used it. script: %s", read, write, copyTaskIdScript)
	}
	// The fallback is trimmed. Untrimmed, the operator pastes a value with a
	// newline and a leading tab into whatever they were about to use it in.
	if !strings.Contains(copyTaskIdScript, ".trim()") {
		t.Errorf("the id fallback is not trimmed, so a chip whose attribute is missing copies the "+
			"template's surrounding whitespace with the id. script: %s", copyTaskIdScript)
	}
}

// TestCopyTaskIdScript_FindsTheLiveRegionThroughTheChipOwnRow keeps the
// script's lookup and the markup's shape in step.
//
// The script walks up to the chip's <dd> and then finds the status span
// inside it. That is the right scope -- a single confirmation on the page,
// beside its own chip -- but it is a coupling to the markup's structure, and
// a rail that moved the span out of the <dd> would leave the script writing
// into null and the click confirming nothing at all, with the copy having
// actually worked. So the <dd> is asserted to be what the chip and its
// status share.
func TestCopyTaskIdScript_FindsTheLiveRegionThroughTheChipOwnRow(t *testing.T) {
	closest := copyScriptIndex(t, "btn.closest('dd')")
	hook := copyScriptIndex(t, `cell.querySelector('[data-krill="copy-task-id-status"]')`)

	if closest > hook {
		t.Errorf("the status lookup (offset %d) precedes the <dd> walk (offset %d) it is scoped by. "+
			"script: %s", hook, closest, copyTaskIdScript)
	}
	// A bare document-wide querySelector would announce beside whatever row
	// happened to be first -- beside the wrong value, on a page with two.
	if strings.Contains(copyTaskIdScript, "document.querySelector('[data-krill=\"copy-task-id-status\"]')") {
		t.Errorf("the copy script looks the live region up document-wide; a second chip's "+
			"confirmation would land in the first one's row. script: %s", copyTaskIdScript)
	}
	// And a null status must be a no-op rather than a throw: the copy has
	// already happened by then, and an exception in the announce would leave
	// the operator with a clipboard that filled and a page that said nothing.
	if !strings.Contains(copyTaskIdScript, "var s=statusOf(btn);if(!s){return;}") {
		t.Errorf("the announce helper does not bail on a missing status element, so a chip whose "+
			"row lost the live region throws after the copy succeeded. script: %s", copyTaskIdScript)
	}
}

// TestCopyTaskIdScript_LeavesNoOtherValueInTheMarkup is the leakage check.
//
// The chip carries one value -- the task id -- because that is the whole
// point. But the same head script runs on EVERY krill page, and the hook it
// keys on is a generic one: a chip marked data-krill="copy-task-id" anywhere
// on any page gets bound, and whatever its data-task-id says is written to
// that operator's clipboard and read into a live region. So the script must
// put nothing else anywhere: no other attribute write, no other value
// surfaced, nothing echoed into the DOM that the operator did not type.
//
// The claim id, the lease instant and the session id all sit in data
// attributes on the detail section. None of them may appear in the script.
func TestCopyTaskIdScript_LeavesNoOtherValueInTheMarkup(t *testing.T) {
	// Every attribute the script writes must be one of the three it owns.
	// A fourth write is a new value leaving the page.
	writes := regexp.MustCompile(`\.setAttribute\('([^']+)'`)
	allowed := map[string]bool{
		"data-krill-bound": true, // the bind-once marker
		"title":            true, // the retitle
		"data-copy-state":  true, // the outcome, for styling and for tests
	}
	for _, m := range writes.FindAllStringSubmatch(copyTaskIdScript, -1) {
		if !allowed[m[1]] {
			t.Errorf("the copy script writes the %q attribute, which is a value leaving the page "+
				"that this task never asked for. script: %s", m[1], copyTaskIdScript)
		}
	}
	// The live region only ever carries a fixed phrase. Anything built by
	// concatenation -- the id, a claim id, an error string -- would put a
	// value into an announced element, which is read aloud.
	for _, built := range []string{"s.textContent=id", "s.textContent=sess", "s.textContent=claim"} {
		if strings.Contains(copyTaskIdScript, built) {
			t.Errorf("the live region is filled with a derived value (%q); it must carry only the "+
				"fixed phrases, since a live region is announced aloud. script: %s",
				built, copyTaskIdScript)
		}
	}
	// And nothing from the surrounding page is read into the copy. The only
	// read off the chip is its own id.
	if strings.Contains(copyTaskIdScript, "data-krill-claim-id") ||
		strings.Contains(copyTaskIdScript, "data-krill-lease-expires-at") {
		t.Errorf("the copy script reads the region's claim or lease attributes, which would put a "+
			"credential-adjacent value in the operator's clipboard. script: %s", copyTaskIdScript)
	}
}

// TestCopyTaskIdScript_AnnouncesTheFailureOnEveryPathThatCanFail walks the
// click handler's own branches rather than checking that a failure message
// exists somewhere in the script.
//
// There are three ways the copy can fail -- the API is absent, writeText
// throws synchronously, writeText rejects -- and each has to reach the same
// outcome. writeId collapses the first two into one null return, so the
// handler has exactly two failure exits, and BOTH must select the id and say
// so. A failure exit that only announced, without selecting, leaves the
// operator told to press Ctrl+C over a selection that was never made.
//
// The counting is what makes it bite: one announce-and-select pair means a
// path fails silently.
func TestCopyTaskIdScript_AnnouncesTheFailureOnEveryPathThatCanFail(t *testing.T) {
	const failMsg = "announce(btn,'failed',"
	if n := strings.Count(copyTaskIdScript, failMsg); n != 2 {
		t.Errorf("the click handler has %d failure announcements; it needs one for the "+
			"missing/throwing-clipboard exit and one for the rejected-promise exit, or one of "+
			"them copies with nothing said and nothing selected. script: %s", n, copyTaskIdScript)
	}
	// Both exits must select, and both must announce -- in that order, so the
	// selection exists by the time the operator is told to use it.
	if n := strings.Count(copyTaskIdScript, "selectId(btn);\n"); n < 2 {
		t.Errorf("selectId(btn) appears %d times; both failure exits must select the id before "+
			"announcing, or the message tells the operator to press Ctrl+C over nothing selected. "+
			"script: %s", n, copyTaskIdScript)
	}
	// The click's own default must be cancelled: the chip is a <button>, and
	// a submit-capable default inside a form would make the copy a write.
	if !strings.Contains(copyTaskIdScript, "ev.preventDefault();") {
		t.Errorf("the click handler does not preventDefault, so the chip's button default runs. "+
			"script: %s", copyTaskIdScript)
	}
	// The selection is over the chip's CONTENTS, not the whole document and
	// not the status span beside it: selecting the status span would put the
	// message on the clipboard instead of the id.
	if !strings.Contains(copyTaskIdScript, "r.selectNodeContents(btn);") {
		t.Errorf("the failure path does not select the chip's own contents, so the operator's next "+
			"Ctrl+C would copy the wrong text. script: %s", copyTaskIdScript)
	}
	// The selection replaces whatever was selected rather than adding to it.
	if !strings.Contains(copyTaskIdScript, "sel.removeAllRanges();sel.addRange(r);") {
		t.Errorf("the failure path does not clear the existing selection before adding the id's, "+
			"so Ctrl+C copies a range spanning both. script: %s", copyTaskIdScript)
	}
	// And the whole thing is best-effort: a browser that refuses the Range
	// still gets the message naming the manual step, so the selection cannot
	// be the thing that throws the failure away.
	if !strings.Contains(copyTaskIdScript, "catch(e){}") {
		t.Errorf("the selection is unguarded, so a browser that refuses the Range throws instead "+
			"of telling the operator to select the id by hand. script: %s", copyTaskIdScript)
	}
}
