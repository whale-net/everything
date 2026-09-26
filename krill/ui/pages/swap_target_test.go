package pages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// An htmx fragment that is swapped into a target MUST carry that target's
// id.
//
// htmx's swapOuterHTML inserts the response before the target and then
// removes the target. A fragment without the id therefore deletes the
// element it was supposed to replace, and htmx's issueAjaxRequest
// returns early when it cannot resolve a target -- before issuing the
// request. So the operator clicks Refresh, no request reaches the server,
// and the page is frozen with no evidence anything was attempted.
//
// Every error-path fragment has to honour this, not just the happy path
// that refresh_test.go covers.

// TestOpsInlineErrorCarriesItsSwapTarget is the guard on the ops console
// error fragment: it is swapped into the results block, so it must carry
// the results id or the console becomes permanently unrefreshable.
func TestOpsInlineErrorCarriesItsSwapTarget(t *testing.T) {
	out := renderBody(t, OpsInlineError("Failed to load console data. Try again."))

	assert.Contains(t, out, `id="`+opsResultsID+`"`,
		"the error fragment is swapped into the results block; without the id htmx deletes the target and every later Refresh is a no-op that never reaches the server")
	assert.Contains(t, out, "Failed to load console data")
	assert.Equal(t, 1, strings.Count(out, "alert-error"), "exactly one message, not a duplicate")
}

// TestSpecInlineErrorCarriesTheRequestedAnchor is the same guard across
// all six spec/delivery pages. One component serves them all, so the
// anchor is a parameter -- a hardcoded or omitted id would leave five of
// the six pages permanently unrefreshable after one failed read.
func TestSpecInlineErrorCarriesTheRequestedAnchor(t *testing.T) {
	for _, anchor := range []string{
		ProductsAnchor,
		CapabilityMapAnchor,
		DecisionsAnchor,
		PersonasAnchor,
		NonGoalsAnchor,
		DeliveryAnchor,
	} {
		t.Run(anchor, func(t *testing.T) {
			out := renderBody(t, SpecInlineError(anchor, "Could not load the spec."))

			assert.Contains(t, out, `id="`+anchor+`"`,
				"the error fragment must land on the id the requesting page's Refresh button targets")
			top := topLevelElements(out)
			assert.Len(t, top, 1,
				"a single top-level element, or a swap splices the extras in on every click")
			assert.Equal(t, "section", top[0])
		})
	}
}
