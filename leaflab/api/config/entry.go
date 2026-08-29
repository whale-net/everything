package config

import (
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// Provenance is FR82.4's per-entry provenance value: whether a stored
// config entry was named by the caller in the request that stored it
// (including in a remove list) or carried forward unchanged from an
// EDIT push's materialisation base. Stored per entry (migration 028),
// returned by FR35.2/FR37, and governs FR1.3's skip visibility so the
// skip signal keeps meaning "something you asked for did not happen".
type Provenance string

const (
	// ProvenanceAuthored: named by the caller in this request -- either in
	// the sensors list (an add/change) or in removes (a removal).
	ProvenanceAuthored Provenance = "authored"
	// ProvenanceMaterialised: carried forward unchanged from an EDIT
	// push's base, without the caller naming it at all.
	ProvenanceMaterialised Provenance = "materialised"
)

// Entry pairs one config entry's canonical hardware key (FR18) with its
// payload and FR82.4 provenance -- the shape this package's
// canonicalisation, removal-key resolution and materialisation all
// operate on. Key is always derived from Sensor by CanonicalKey; the two
// are kept as separate fields, rather than deriving Key on every use, so
// materialisation's key-based lookups (map keyed on Key.String()) don't
// recompute it per comparison.
type Entry struct {
	Key        hwkey.Key
	Sensor     *configpb.SensorConfig
	Provenance Provenance
}
