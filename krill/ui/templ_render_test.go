package main

import (
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
