package main

// The page-wide half of FR ede7cee6: one Overview region whose read fails
// renders an inline error, and every other region renders normally.
//
// The per-region seams were built by the three tasks that shipped the tiles,
// the Needs-attention panel and the in-flight panel, and each proves its own
// region's failure in isolation. What this file adds is the guarantee across
// regions: that a failed read in one of them costs the operator nothing
// belonging to another, and that the failed region itself never answers with
// a reassuring number it did not read.
//
// The rule the whole page turns on: a failed read must never look like a
// successful read of zero. "0 escalated tasks" and "nothing is in flight" are
// both claims an operator acts on; a read failure is neither, so it renders
// the alert and nothing else.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// regionBoom is the store failure every region is driven with. It is
// deliberately verbose and internal-looking, because half of what these
// cases assert is that none of it reaches the operator: a store error can
// carry a connection string or an internal address (NFR cfadc9c7).
var regionBoom = errors.New("pq: connection refused dsn=postgres://krill:hunter2@10.4.0.9:5432/krill")

// overviewFailureDesignSessions is the design read behind the Blocking
// questions tile, with a knob the other tiles' cases never touch.
type overviewFailureDesignSessions struct {
	navStubDesignSessions
	err error
}

func (f overviewFailureDesignSessions) SummarizeByProduct(context.Context, uuid.UUID) (store.ProductDesignSessionsSummary, error) {
	if f.err != nil {
		return store.ProductDesignSessionsSummary{}, f.err
	}
	return store.ProductDesignSessionsSummary{}, nil
}

// failureMux serves the Overview with every read healthy except the one the
// case breaks. Each read goes through its own knob -- the header's listing,
// the escalated count, the console read behind two tiles, the design read
// behind the fourth, the progress read, the panel's own read -- so a case
// names the region it is failing and nothing else moves with it.
func failureMux(t *testing.T, counter *overviewCounter, listingErr, designErr error) *http.ServeMux {
	t.Helper()
	app := newTestApp(t)
	app.spec = &fakeSpecReader{
		products:   []store.Product{{ID: overviewProduct, Name: "krill", Vision: "the spec substrate"}},
		listing:    overviewListing(),
		listingErr: listingErr,
	}
	app.tasks = counter
	app.credentials = &fakeCredentials{}
	app.designSessions = overviewFailureDesignSessions{err: designErr}
	app.revisionEvents = navStubRevisionEvents{}

	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// healthyCounter is the fixture every failure case starts from: the header
// has two containers in flight, the panel has its rows, the attention panel
// has escalations and every tile reads zero. A case breaks exactly one read.
func healthyCounter() *overviewCounter {
	c := withProgress(panelContainers()...)
	c.count = len(overviewEscalationTitles)
	c.readable = true
	c.escalated = escalatedFixtures(len(overviewEscalationTitles))
	return c
}

// regionCase is one failure case: what was broken, the hook its region must
// render its error at, and the store text that must not appear anywhere.
type regionCase struct {
	name string

	// breakIt wires the one failing read onto a healthy fixture.
	breakIt func(*overviewCounter) *overviewCounter

	// errorHook is the region that must render the inline alert.
	errorHook string

	// healthyHooks are the regions that must still render their own data,
	// each paired with the text proving they did.
	healthy []struct{ hook, want string }
}

// The four failure cases the FR names, plus the escalated count the header's
// primary action is built from -- which is its own region, and the one whose
// silence would read as "nothing needs you".
var overviewRegionFailures = []regionCase{
	{
		name:       "the escalated count behind the header's action",
		breakIt:    func(c *overviewCounter) *overviewCounter { c.readable = false; return c },
		errorHook:  "overview-escalated-error",
		healthy:    []struct{ hook, want string }{{"overview-in-flight-row", "Executing milestone"}, {"needs-attention-list", "Escalate newest"}},
	},
	{
		name:       "a tile's console count",
		breakIt:    func(c *overviewCounter) *overviewCounter { c.overviewErr = regionBoom; return c },
		errorHook:  "overview-stat-error",
		healthy:    []struct{ hook, want string }{{"overview-in-flight-row", "Executing milestone"}, {"needs-attention-list", "Escalate newest"}},
	},
	{
		name:       "the header's in-flight listing",
		breakIt:    func(c *overviewCounter) *overviewCounter { return c },
		errorHook:  "overview-in-flight-error",
		healthy:    []struct{ hook, want string }{{"overview-in-flight-panel-rows", "Executing milestone"}, {"needs-attention-list", "Escalate newest"}},
	},
	{
		name:       "the attention panel's escalated read",
		breakIt:    func(c *overviewCounter) *overviewCounter { c.escalatedErr = regionBoom; return c },
		errorHook:  "needs-attention-error",
		healthy:    []struct{ hook, want string }{{"overview-in-flight-row", "Executing milestone"}, {"overview-in-flight-panel-rows", "Executing milestone"}},
	},
	{
		name:       "the in-flight panel's progress read",
		breakIt:    func(c *overviewCounter) *overviewCounter { c.progressErr = regionBoom; return c },
		errorHook:  "overview-in-flight-panel-error",
		// The header's badges come from the delivery read, which is a
		// different one from the progress read this case breaks -- so they
		// must still be listed, under the listing's own names.
		healthy: []struct{ hook, want string }{{"overview-in-flight", "Build the thing"}, {"needs-attention-list", "Escalate newest"}},
	},
}

// TestOverviewOneFailedRegionCostsNoOtherRegion is the page-wide guarantee
// (FR ede7cee6). For each case: the status is 200, the failing region shows
// an inline alert, every other region renders its real data, and none of the
// store's own text appears anywhere on the page.
func TestOverviewOneFailedRegionCostsNoOtherRegion(t *testing.T) {
	for _, tc := range overviewRegionFailures {
		t.Run(tc.name, func(t *testing.T) {
			var listingErr, designErr error
			if tc.errorHook == "overview-in-flight-error" {
				listingErr = regionBoom
			}
			mux := failureMux(t, tc.breakIt(healthyCounter()), listingErr, designErr)

			for _, path := range overviewPaths() {
				rec := fetch(t, mux, path)

				if rec.Code != http.StatusOK {
					t.Errorf("GET %s: status %d, want 200 -- htmx does not swap on a non-2xx, "+
						"so an error status leaves the operator with an unchanged page and no explanation", path, rec.Code)
				}
				body := rec.Body.String()

				if region := overviewRegion(body, tc.errorHook); region == "" {
					t.Errorf("GET %s: the failing region rendered no inline error at %q:\n%s",
						path, tc.errorHook, collapsed(body))
				} else if !strings.Contains(region, "alert-error") {
					t.Errorf("GET %s: the failing region did not render an htmxui.Alert:\n%s", path, collapsed(region))
				}

				for _, h := range tc.healthy {
					region := overviewRegion(body, h.hook)
					if region == "" {
						t.Errorf("GET %s: region %q rendered nothing, though its own read succeeded:\n%s",
							path, h.hook, collapsed(body))
						continue
					}
					if !strings.Contains(collapsed(region), h.want) {
						t.Errorf("GET %s: region %q lost its data when an unrelated read failed, "+
							"want it to still contain %q:\n%s", path, h.hook, h.want, collapsed(region))
					}
				}

				// No store text on any failure path: the message names the
				// missing fact and points at the logs, which carry the cause.
				if strings.Contains(body, "hunter2") || strings.Contains(body, "10.4.0.9") ||
					strings.Contains(body, "connection refused") {
					t.Errorf("GET %s: the page leaked the store's own error text to the operator:\n%s",
						path, collapsed(body))
				}
			}
		})
	}
}

// TestOverviewAFailedRegionNeverAnswersWithZero is the half of the FR the
// other half cannot see: the failed region must not render a figure or an
// empty-list sentence it did not read. A `0` and "Nothing is in flight" are
// both reassuring, and an operator who believes either has been told
// something the page does not know.
//
// Each case names its own forbidden claims rather than looking them up, so a
// region that renders no alert at all fails here too rather than passing
// vacuously.
func TestOverviewAFailedRegionNeverAnswersWithZero(t *testing.T) {
	cases := []struct {
		name    string
		breakIt func(*overviewCounter) *overviewCounter
		listing error

		// errHook is the region that must report the failure, and forbidden
		// are the claims that region would make if the failure leaked
		// through as a successful read of nothing.
		errHook   string
		forbidden []string
	}{
		{
			name:    "the escalated count",
			breakIt: func(c *overviewCounter) *overviewCounter { c.readable = false; return c },
			errHook: "overview-escalated-error",
			// The count is 7 in this fixture, so an action or a
			// "nothing is escalated" sentence here is a number the read
			// never returned -- and the second is a reassurance it cannot
			// support.
			forbidden: []string{"overview-primary-action", "overview-nothing-escalated"},
		},
		{
			name:     "the header's in-flight listing",
			breakIt:  func(c *overviewCounter) *overviewCounter { return c },
			listing:  regionBoom,
			errHook:  "overview-in-flight-error",
			forbidden: []string{"overview-no-in-flight", "overview-in-flight"},
		},
		{
			name:     "the attention panel",
			breakIt:  func(c *overviewCounter) *overviewCounter { c.escalatedErr = regionBoom; return c },
			errHook:  "needs-attention-error",
			forbidden: []string{"needs-attention-empty", "needs-attention-list"},
		},
		{
			name:     "the in-flight panel",
			breakIt:  func(c *overviewCounter) *overviewCounter { c.progressErr = regionBoom; return c },
			errHook:  "overview-in-flight-panel-error",
			forbidden: []string{"overview-in-flight-panel-empty", "overview-in-flight-panel-rows"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := failureMux(t, tc.breakIt(healthyCounter()), tc.listing, nil)

			for _, path := range overviewPaths() {
				body := fetch(t, mux, path).Body.String()

				if overviewRegion(body, tc.errHook) == "" {
					t.Fatalf("GET %s: the failing region rendered no error, so the rest of this case is vacuous:\n%s",
						path, collapsed(body))
				}
				for _, claim := range tc.forbidden {
					if overviewRegion(body, claim) != "" {
						t.Errorf("GET %s: the failed region still renders %q, a claim its read never made:\n%s",
							path, claim, collapsed(body))
					}
				}
			}
		})
	}
}

// TestOverviewATileFailureDoesNotFabricateAZero is the tile strip's own half
// of the rule, which the case above cannot see because a stat tile and a
// panel share the page rather than a hook. A tile whose read failed must
// render its message and no figure: the figure would sit directly above a
// sub-line derived from the same read and be read as one confident answer.
func TestOverviewATileFailureDoesNotFabricateAZero(t *testing.T) {
	mux := failureMux(t, func(c *overviewCounter) *overviewCounter {
		c.overviewErr = regionBoom
		return c
	}(healthyCounter()), nil, nil)

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		// The two tiles behind the console read carry its error; the other
		// two read elsewhere and must keep their figures.
		for _, label := range []string{"Claimed", "Open notes"} {
			tile := overviewTileSection(t, body, label)
			if !strings.Contains(tile, `data-krill="overview-stat-error"`) {
				t.Errorf("GET %s: tile %q rendered no error though the console read failed: %s", path, label, tile)
			}
			if strings.Contains(tile, `data-krill="overview-stat-count"`) {
				t.Errorf("GET %s: tile %q rendered a figure for a read that failed: %s", path, label, tile)
			}
			if strings.Contains(tile, ">0<") {
				t.Errorf("GET %s: tile %q rendered an unreadable count as a zero: %s", path, label, tile)
			}
		}

		// The Escalated tile's own read (the badge count) still succeeded,
		// so its figure must still be there: the failure belongs to the
		// tiles that share the console read and not to the whole strip.
		if overviewRegion(body, "overview-stat-count") == "" {
			t.Errorf("GET %s: the console read's failure cost the whole strip its figures:\n%s", path, collapsed(body))
		}
	}
}

// TestOverviewFailedRegionsDoNotShareOneCascade is the structural claim
// under all of the above: the regions read different things, so failing one
// does not fail the rest. Each case names one broken read and asserts that
// every *other* region still shows its data -- which is only true if the
// reads are independent, since a shared knob would take them all down
// together and this would pass for the wrong reason.
func TestOverviewFailedRegionsDoNotShareOneCascade(t *testing.T) {
	allBroken := func(c *overviewCounter) *overviewCounter {
		c.readable = false         // the header's escalated count
		c.overviewErr = regionBoom // the console read behind two tiles
		c.escalatedErr = regionBoom
		c.progressErr = regionBoom
		return c
	}
	mux := failureMux(t, allBroken(healthyCounter()), regionBoom, regionBoom)

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		for _, hook := range []string{
			"overview-escalated-error",
			"overview-stat-error",
			"overview-in-flight-error",
			"needs-attention-error",
		} {
			if overviewRegion(body, hook) == "" {
				t.Errorf("GET %s: with every read broken, region %q reported nothing:\n%s",
					path, hook, collapsed(body))
			}
		}
		// Nothing anywhere claims a figure or a list it could not read.
		for _, claim := range []string{
			"overview-primary-action",
			"overview-nothing-escalated",
			"overview-no-in-flight",
			"overview-in-flight-panel-empty",
			"needs-attention-empty",
			"needs-attention-list",
		} {
			if overviewRegion(body, claim) != "" {
				t.Errorf("GET %s: with every read broken, the page still claims %q", path, claim)
			}
		}
	}
}

// TestOverviewFailedInFlightReadStillRendersEveryOtherRegion pins the
// regression this page was written to prevent: an early return in
// buildOverview made a failed in-flight read cost the operator the attention
// panel's own answer, so a product with escalations was told nothing needs
// attention beside an unrelated error. The attention panel's read is
// independent and its rows must survive the header's failure.
func TestOverviewFailedInFlightReadStillRendersEveryOtherRegion(t *testing.T) {
	mux := failureMux(t, healthyCounter(), regionBoom, nil)

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		if overviewRegion(body, "overview-in-flight-error") == "" {
			t.Fatalf("GET %s: the in-flight read failed but the header reported nothing", path)
		}
		rows := attentionRows(body)
		if len(rows) != pages.NeedsAttentionMax {
			t.Errorf("GET %s: the attention panel rendered %d rows after the header's read failed, "+
				"want the %d its own read returned", path, len(rows), pages.NeedsAttentionMax)
		}
		if empty := overviewRegion(body, "needs-attention-empty"); empty != "" {
			t.Errorf("GET %s: the attention panel rendered its empty state after an unrelated read "+
				"failed, claiming nothing is escalated when its own read succeeded", path)
		}
		// The panel the failure belongs to is the header's, not the
		// progress panel's -- a distinct read with a distinct knob.
		if panel := overviewRegion(body, "overview-in-flight-panel-rows"); panel == "" {
			t.Errorf("GET %s: the in-flight progress panel lost its rows to the header's failure:\n%s",
				path, collapsed(body))
		}
	}
}