package config

import (
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/hwkey"
	"google.golang.org/protobuf/proto"
)

// DiffKind classifies one sensor entry's change between two complete
// config payloads (FR37).
type DiffKind string

const (
	DiffAdded     DiffKind = "added"
	DiffRemoved   DiffKind = "removed"
	DiffChanged   DiffKind = "changed"
	DiffUnchanged DiffKind = "unchanged"
)

// EntryDiff is one sensor entry's classification between base and target
// (FR37), plus both sides' raw entries so a caller renders what changed
// without a second lookup. Base is nil when Kind is DiffAdded (no prior
// entry to show); Target is nil when Kind is DiffRemoved (no new entry to
// show).
type EntryDiff struct {
	Key    hwkey.Key
	Kind   DiffKind
	Base   *configpb.SensorConfig
	Target *configpb.SensorConfig
}

// Diff computes FR37's server-side, per-entry diff between two complete
// config payloads -- never a partial EDIT payload (see doc.go: an EDIT
// push's adds/removes are materialised into a complete entry set by
// Materialise before this is ever the right function to call on them;
// PushDeviceConfig's own EDIT handling and DiffConfigVersions's draft
// side both materialise first). Because every stored payload is complete
// (FR82), DiffRemoved is reachable from a diff against either an EDIT
// push's materialised result (an entry present in the prior version but
// dropped by a remove) or a COMPLETE push that simply omitted it.
//
// Diff compares by canonical hardware key (FR18), via []Entry rather than
// raw wire sensors, so an entry that moved position in the list is
// DiffUnchanged (if otherwise identical), never spuriously DiffChanged
// from a naive index-by-index comparison.
//
// DiffUnchanged vs DiffChanged is decided by proto.Equal on the full
// stored SensorConfig, not just the three canonical-key components -- for
// example, a poll_interval_ms-only edit is DiffChanged even though the
// entry's canonical key (i2c_address/mux_path/sensor_type) never moved.
func Diff(base, target []Entry) []EntryDiff {
	targetByKey := make(map[string]Entry, len(target))
	for _, e := range target {
		targetByKey[e.Key.String()] = e
	}

	seen := make(map[string]bool, len(base))
	diffs := make([]EntryDiff, 0, len(base)+len(target))

	// base's own order first, so every entry that existed before the
	// target side (DiffUnchanged, DiffChanged, DiffRemoved) is reported in
	// its original, stable order.
	for _, b := range base {
		key := b.Key.String()
		seen[key] = true
		t, ok := targetByKey[key]
		switch {
		case !ok:
			diffs = append(diffs, EntryDiff{Key: b.Key, Kind: DiffRemoved, Base: b.Sensor})
		case proto.Equal(b.Sensor, t.Sensor):
			diffs = append(diffs, EntryDiff{Key: b.Key, Kind: DiffUnchanged, Base: b.Sensor, Target: t.Sensor})
		default:
			diffs = append(diffs, EntryDiff{Key: b.Key, Kind: DiffChanged, Base: b.Sensor, Target: t.Sensor})
		}
	}

	// Then target-only entries (DiffAdded), in target's own order.
	for _, t := range target {
		key := t.Key.String()
		if seen[key] {
			continue
		}
		diffs = append(diffs, EntryDiff{Key: t.Key, Kind: DiffAdded, Target: t.Sensor})
	}

	return diffs
}
