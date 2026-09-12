//go:build integration

// This file proves the data half of FR21 (root plan issue #2485, task
// issue #2500): `/project-manager:design --milestone`'s krill-domain
// read branches to a live call to krill's own `get_product_slice` (FR8)
// instead of reading `krill/product/03-roadmap.md`. It shares
// roundtrip_integration_test.go's testEnv/krillDocsRoot helpers (same
// package, same //krill/conformance:roundtrip_integration_test go_test
// target -- see BUILD.bazel) since both prove properties of the same
// self-hosting loop: that file proves the import/render round trip
// (FR18/FR19, issue #2497); this one proves the live-consumer half
// (FR21) that root plan issue #2485 calls out as the other thing that
// makes M1's self-hosting loop actually exercised, not merely proven by
// round-trip fidelity.
//
// Per LB7 ("M1's MCP tool is a thin wrapper over [slice.Querier], not the
// thing itself" -- krill/mcp/tools/slice.go), this tests slice.Querier
// directly rather than standing up the MCP wire protocol: the MCP tool's
// own correctness (auth, byte-identical JSON shape) is already covered by
// issue #2494's own tests, and slice.Querier.GetProductSlice is the exact
// code every one of that surface's calls executes. The domain-branch
// decision itself -- krill's own domain calls this live, every other
// domain still reads the file -- lives in
// tools/project-manager/skills/design/SKILL.md and
// tools/project-manager/agents/producer.md, which ship no Bazel test
// target to exercise automatically; root plan issue #2485's own
// acceptance criteria calls for verifying that one by diff review
// instead.
//
// Run explicitly (requires a working Docker daemon), same as the
// round-trip suite this file shares a target with:
//
//	bazel test //krill/conformance:roundtrip_integration_test --test_output=all
package conformance

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/importer"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// TestDesignMilestoneQuery_LiveProductSlice_CarriesMilestonesCitedContent
// is FR21's positive case (root plan issue #2485's Testing bullet:
// "designing against krill's own M2 entry returns the same milestone
// content the committed krill/product/03-roadmap.md carries"): after
// importing krill's own committed brief, the exact FR8 call the design
// skill's krill-domain branch makes (slice.Querier.GetProductSlice)
// surfaces, live, the same capability descriptions that milestone's own
// `Delivers:` line in the committed roadmap file names -- by content, the
// thing the design skill actually needs to draft FRs against, not by a
// milestone-scoped id filter the FR5-FR9 surface does not yet expose (see
// krill/ARCHITECTURE.md "The design skill's live milestone read (FR21)"
// for that documented gap, M3's C13/C28).
func TestDesignMilestoneQuery_LiveProductSlice_CarriesMilestonesCitedContent(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	root := krillDocsRoot(t)

	report, err := importer.Import(ctx, env.store, env.sessions, env.sessionID, root)
	require.NoError(t, err, "importing krill's own committed brief must succeed")

	// Independently re-parse the same committed doc set the importer just
	// wrote, purely to learn M2's own Delivers list and each cited
	// capability's description -- this is the "committed
	// krill/product/03-roadmap.md" half of the Testing bullet's
	// comparison, read directly rather than trusted from the importer's
	// own report.
	parsed, err := importer.Parse(root)
	require.NoError(t, err)

	var m2 *importer.ParsedMilestone
	for i := range parsed.Milestones {
		if parsed.Milestones[i].ID == "M2" {
			m2 = &parsed.Milestones[i]
			break
		}
	}
	require.NotNil(t, m2, "krill's own committed roadmap must define an M2 entry for this test to compare against")
	require.NotEmpty(t, m2.Delivers, "M2 must cite at least one capability in its Delivers line")

	wantDescriptions := make(map[string]string, len(m2.Delivers))
	for _, bucket := range parsed.Buckets {
		for _, cap := range bucket.Capabilities {
			wantDescriptions[cap.ID] = cap.Description
		}
	}

	// Now make the exact live call FR21 wires the design skill's
	// krill-domain branch to (FR8, whole-product granularity) -- no file
	// read from here on.
	doc, err := slice.NewQuerier(env.store).GetProductSlice(ctx, report.ProductID)
	require.NoError(t, err, "the live FR8 call must succeed against krill's own just-imported Product")

	liveDescriptions := make(map[string]bool, len(doc.Features))
	for _, f := range doc.Features {
		liveDescriptions[f.Name] = true
	}

	for _, capID := range m2.Delivers {
		wantDesc, ok := wantDescriptions[capID]
		require.True(t, ok, "M2's Delivers line cites %s, which the capability map must define", capID)
		assert.True(t, liveDescriptions[wantDesc],
			"M2 (Delivers: %s) cites %s = %q in the committed roadmap; the live get_product_slice call must surface that exact capability content, since the design skill's krill-domain branch reads it from there and not from the file",
			capID, capID, wantDesc)
	}
}

// TestDesignMilestoneQuery_UnreachableProduct_FailsLoudNotSilentlyEmpty is
// FR21's negative case (root plan issue #2485's Testing bullet:
// "krill unreachable produces the documented explicit failure, not a
// file fallback"). The design skill's own unreachable-server case (an
// HTTP-level failure calling the krill MCP server) is outside what a Go
// test in this repo can exercise, but the design skill's failure
// instruction is "stop and say so by name" whenever the live call does
// not come back clean -- this proves the underlying call it depends on
// actually behaves that way: querying a Product id krill does not (or no
// longer) hold returns a clear, wrapped, named error, never a quietly
// empty slice.Document a caller could mistake for "no milestone content"
// and silently paper over by falling back to the file.
func TestDesignMilestoneQuery_UnreachableProduct_FailsLoudNotSilentlyEmpty(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	unknownID := uuid.New()
	doc, err := slice.NewQuerier(env.store).GetProductSlice(ctx, unknownID)

	require.Error(t, err, "querying a Product krill does not hold must fail loudly, not return a zero-value Document as if it were an empty-but-valid milestone read")
	assert.ErrorIs(t, err, store.ErrNotFound, "the failure must be the store's own named not-found error, not an opaque wrapped-away one the caller cannot distinguish from an actual transport failure")
	assert.Contains(t, err.Error(), unknownID.String(), "the error must name which id was unreachable, so a caller (or the design skill relaying it to the user) can say so by name rather than a generic 'krill unreachable'")
	assert.Empty(t, doc.Features, "a failed call must not also hand back partially-populated content the caller could mistake for a successful (if thin) milestone read")
}
