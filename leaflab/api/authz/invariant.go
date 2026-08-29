package authz

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/leaflab/api/contract"
)

// LiveRef names one live reference within a write payload that
// AssertSameHousehold must validate, paired with the request field it came
// from so a violation can be reported against that exact field (FR1.3's
// "naming the offending entry and field"). Entity/field naming for the
// failure comes from ref.Kind and Field respectively -- there is no
// separate "entity" string to keep in sync with EntityKind.
type LiveRef struct {
	EntityRef
	// Field is the request field this reference came from, reported
	// verbatim on failure -- e.g. "sensors[3].region_id" for one entry of
	// a PushDeviceConfig payload's sensor list. Callers validating a batch
	// (FR1.3) build one LiveRef per entry with a field string that already
	// encodes the entry's position, so AssertSameHousehold itself never
	// needs to know it's looking at a batch.
	Field string
}

// AssertSameHousehold is FR1.2's global write invariant: "no write through
// any surface may cause an entity's current state to reference a region,
// plant or board belonging to a different household." Every write RPC
// that would leave a live reference in place -- adoption, transfer, claim,
// region assignment, plant placement, subtree relocation -- calls this
// once per reference it is about to write, before performing the write.
// NFR1's conformance test asserts every such write path does.
//
// It resolves each of refs via resolver and fails closed on the first
// reference whose resolved household is not writerHousehold -- an
// Unclaimed resolution (FR1.1's board-with-no-owner exception) never
// satisfies this either, since "belongs to nobody yet" is not "belongs to
// writerHousehold". The returned error is FR59's FailureInvalidArgument,
// carrying the offending ref's EntityKind and Field as the failure's
// entity/field (FR59.1) -- never a generic "forbidden" with no pointer
// back to which entry violated the invariant.
//
// AssertSameHousehold performs no writes and stops at the first violation:
// FR1.3's "no partial application" is the caller's responsibility to
// preserve by calling this -- for every ref in the payload -- before any
// of them are written, never interleaved with writing some and validating
// the rest.
//
// This only ever asserts about a *live* reference a write is about to
// create. A closed history row (valid_to IS NULL is false) is a record of
// what was true then, not a live reference (FR1.2) -- it is never passed
// to this function and never fails it.
//
// The signature takes resolver explicitly rather than being a method on a
// concrete Resolver implementation, matching ResolveInScope's existing
// shape in this package: every call site already holds a Resolver (e.g.
// the authzResolver a handler was constructed with), not a
// resolver-bound receiver.
func AssertSameHousehold(ctx context.Context, resolver Resolver, writerHousehold int64, refs ...LiveRef) error {
	for _, ref := range refs {
		res, err := resolver.Resolve(ctx, ref.EntityRef)
		if err != nil {
			return fmt.Errorf("authz: assert same household: resolve %s %d: %w", ref.Kind, ref.ID, err)
		}
		if res.Unclaimed || res.HouseholdID != writerHousehold {
			return contract.InvalidArgument(string(ref.Kind), ref.Field, foreignReferenceReason)
		}
	}
	return nil
}

// foreignReferenceReason is FR59.2's persona-appropriate sentence for
// every AssertSameHousehold violation -- the same text regardless of which
// entity kind or write path triggered it, so a violation is
// distinguishable from any other invalid_argument failure only by class,
// entity and field, never by parsing a bespoke message per call site.
const foreignReferenceReason = "This references something that belongs to a different household."
