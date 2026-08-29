package config

import (
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
)

// diffByKind buckets diffs by their Kind for order-independent assertions.
func diffByKind(diffs []EntryDiff) map[DiffKind][]EntryDiff {
	out := make(map[DiffKind][]EntryDiff)
	for _, d := range diffs {
		out[d.Kind] = append(out[d.Kind], d)
	}
	return out
}

// TestDiff_ClassifiesAddedRemovedChangedUnchanged covers FR37's core
// classification: an entry only in target is ADDED, only in base is
// REMOVED, in both but with a different payload is CHANGED, and in both
// with an identical payload is UNCHANGED.
func TestDiff_ClassifiesAddedRemovedChangedUnchanged(t *testing.T) {
	base := []Entry{
		entryAt("stays-same", 0x10, typeIlluminance, nil, ProvenanceAuthored),
		entryAt("will-change", 0x20, typeTemperature, nil, ProvenanceAuthored),
		entryAt("will-be-removed", 0x30, typeHumidity, nil, ProvenanceAuthored),
	}
	changed := entryAt("will-change-renamed", 0x20, typeTemperature, nil, ProvenanceAuthored)
	target := []Entry{
		base[0], // unchanged
		changed, // same key, different payload (name)
		entryAt("newly-added", 0x40, typeECO2, nil, ProvenanceAuthored),
	}

	diffs := Diff(base, target)
	byKind := diffByKind(diffs)

	if len(byKind[DiffUnchanged]) != 1 || byKind[DiffUnchanged][0].Base.Name != "stays-same" {
		t.Errorf("DiffUnchanged = %+v, want exactly 'stays-same'", byKind[DiffUnchanged])
	}
	if len(byKind[DiffChanged]) != 1 || byKind[DiffChanged][0].Target.Name != "will-change-renamed" {
		t.Errorf("DiffChanged = %+v, want exactly 'will-change-renamed'", byKind[DiffChanged])
	}
	if len(byKind[DiffRemoved]) != 1 || byKind[DiffRemoved][0].Base.Name != "will-be-removed" {
		t.Errorf("DiffRemoved = %+v, want exactly 'will-be-removed'", byKind[DiffRemoved])
	}
	if len(byKind[DiffAdded]) != 1 || byKind[DiffAdded][0].Target.Name != "newly-added" {
		t.Errorf("DiffAdded = %+v, want exactly 'newly-added'", byKind[DiffAdded])
	}
}

// TestDiff_BothRawPayloadsPresent covers FR37's "both raw payloads
// available": Base is nil exactly for ADDED, Target is nil exactly for
// REMOVED, and both sides are populated for CHANGED/UNCHANGED.
func TestDiff_BothRawPayloadsPresent(t *testing.T) {
	base := []Entry{
		entryAt("removed", 0x10, typeIlluminance, nil, ProvenanceAuthored),
		entryAt("unchanged", 0x20, typeTemperature, nil, ProvenanceAuthored),
	}
	target := []Entry{
		base[1],
		entryAt("added", 0x30, typeHumidity, nil, ProvenanceAuthored),
	}

	for _, d := range Diff(base, target) {
		switch d.Kind {
		case DiffAdded:
			if d.Base != nil {
				t.Errorf("DiffAdded entry has non-nil Base: %+v", d)
			}
			if d.Target == nil {
				t.Errorf("DiffAdded entry has nil Target: %+v", d)
			}
		case DiffRemoved:
			if d.Target != nil {
				t.Errorf("DiffRemoved entry has non-nil Target: %+v", d)
			}
			if d.Base == nil {
				t.Errorf("DiffRemoved entry has nil Base: %+v", d)
			}
		case DiffUnchanged, DiffChanged:
			if d.Base == nil || d.Target == nil {
				t.Errorf("%s entry missing a raw side: %+v", d.Kind, d)
			}
		}
	}
}

// TestDiff_REMOVED_ReachableFromEditMaterialisation covers FR37's stated
// property that REMOVED is reachable from an EDIT push: Materialise the
// EDIT (dropping one base entry via a remove key), then diff the
// materialised result against the original base -- the dropped entry
// must classify REMOVED.
func TestDiff_REMOVED_ReachableFromEditMaterialisation(t *testing.T) {
	base := []Entry{
		entryAt("keep", 0x10, typeIlluminance, nil, ProvenanceAuthored),
		entryAt("drop", 0x20, typeTemperature, nil, ProvenanceAuthored),
	}
	removes := []RemoveKey{removeFullKey(0x20, typeTemperature, nil)}

	result, err := Materialise(base, nil, removes)
	if err != nil {
		t.Fatalf("Materialise: %v", err)
	}

	diffs := Diff(base, result.Entries)
	byKind := diffByKind(diffs)
	if len(byKind[DiffRemoved]) != 1 || byKind[DiffRemoved][0].Base.Name != "drop" {
		t.Fatalf("DiffRemoved = %+v, want exactly 'drop' (dropped by the EDIT push's remove key)", byKind[DiffRemoved])
	}
	if len(byKind[DiffUnchanged]) != 1 || byKind[DiffUnchanged][0].Base.Name != "keep" {
		t.Errorf("DiffUnchanged = %+v, want exactly 'keep'", byKind[DiffUnchanged])
	}
}

// TestDiff_REMOVED_ReachableFromCompletePushOmission covers FR37's other
// stated path to REMOVED: a COMPLETE push's target payload simply omits
// an entry the base payload had -- no remove key involved at all, since
// scope=COMPLETE has no removes list -- and the diff still classifies it
// REMOVED.
func TestDiff_REMOVED_ReachableFromCompletePushOmission(t *testing.T) {
	base := []Entry{
		entryAt("keep", 0x10, typeIlluminance, nil, ProvenanceAuthored),
		entryAt("omitted", 0x20, typeTemperature, nil, ProvenanceAuthored),
	}
	// A COMPLETE push's target is exactly what the caller submitted --
	// here, simply missing "omitted", with no remove key of any kind.
	target := []Entry{
		entryAt("keep", 0x10, typeIlluminance, nil, ProvenanceAuthored),
	}

	diffs := Diff(base, target)
	byKind := diffByKind(diffs)
	if len(byKind[DiffRemoved]) != 1 || byKind[DiffRemoved][0].Base.Name != "omitted" {
		t.Fatalf("DiffRemoved = %+v, want exactly 'omitted' (dropped by a COMPLETE push that didn't name it)", byKind[DiffRemoved])
	}
}

// TestDiff_ChangedComparesFullPayload_NotJustCanonicalKey covers the
// documented DiffChanged/DiffUnchanged split: a poll_interval_ms-only
// edit (no change to the canonical key's own components) still classifies
// CHANGED, via proto.Equal on the full stored SensorConfig.
func TestDiff_ChangedComparesFullPayload_NotJustCanonicalKey(t *testing.T) {
	base := []Entry{{
		Key:    CanonicalKey(&configpb.SensorConfig{I2CAddress: 0x44, PollIntervalMs: 1000}, typeTemperature),
		Sensor: &configpb.SensorConfig{I2CAddress: 0x44, PollIntervalMs: 1000},
	}}
	target := []Entry{{
		Key:    CanonicalKey(&configpb.SensorConfig{I2CAddress: 0x44, PollIntervalMs: 5000}, typeTemperature),
		Sensor: &configpb.SensorConfig{I2CAddress: 0x44, PollIntervalMs: 5000},
	}}

	diffs := Diff(base, target)
	if len(diffs) != 1 || diffs[0].Kind != DiffChanged {
		t.Fatalf("Diff = %+v, want exactly one DiffChanged entry", diffs)
	}
}

// TestDiff_EntryMovedPositionInList_IsUnchangedNotChanged proves Diff
// compares by canonical key, not list index -- an entry that simply moved
// position (otherwise identical) is DiffUnchanged, never spuriously
// DiffChanged from a naive index-by-index comparison.
func TestDiff_EntryMovedPositionInList_IsUnchangedNotChanged(t *testing.T) {
	a := entryAt("a", 0x10, typeIlluminance, nil, ProvenanceAuthored)
	b := entryAt("b", 0x20, typeTemperature, nil, ProvenanceAuthored)
	base := []Entry{a, b}
	target := []Entry{b, a} // same two entries, reversed order

	diffs := Diff(base, target)
	byKind := diffByKind(diffs)
	if len(byKind[DiffChanged]) != 0 || len(byKind[DiffAdded]) != 0 || len(byKind[DiffRemoved]) != 0 {
		t.Fatalf("Diff of a reordered-but-identical set produced non-Unchanged entries: %+v", diffs)
	}
	if len(byKind[DiffUnchanged]) != 2 {
		t.Fatalf("DiffUnchanged = %d entries, want 2", len(byKind[DiffUnchanged]))
	}
}

// TestDiff_EmptyBothSides is the degenerate control case: diffing two
// empty payloads produces no entries at all.
func TestDiff_EmptyBothSides(t *testing.T) {
	diffs := Diff(nil, nil)
	if len(diffs) != 0 {
		t.Errorf("Diff(nil, nil) = %+v, want empty", diffs)
	}
}
