// Package configcompose composes a board's full desired sensor list from its
// current DB sensor inventory, its last accepted DeviceConfig, and a set of
// requested overrides. It is shared, unmodified, by both //leaflab/api's
// caller-invoked PushDeviceConfig (FR8) and //leaflab/processor's
// process-internal corrective push (FR9) -- see ComposeDesiredSensors' doc
// comment for the composition semantics and #1772's "Composition parity
// with FR8" note for why this must be one function, not two copies.
package configcompose

import (
	"fmt"
	"sort"
	"strings"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
)

// InventorySensor is one board sensor's DB-side hardware identity and
// current desired state, as read by Repository.ListSensorInventoryForBoard.
// It carries no proto types beyond MuxPath so ComposeDesiredSensors can
// build SensorConfig entries purely from plain data, without a DB or
// transport dependency.
type InventorySensor struct {
	SensorID   int64
	Name       string // from the open sensor_name_history row
	Unit       string
	SensorType firmwarepb.SensorType // resolved from sensor_type.name
	MuxPath    []*configpb.MuxHop
	I2CAddress uint32
	RegionID   *int64 // nil = sensor has never been placed in a region
}

// ComposeDesiredSensors merges, in this precedence order (lowest first):
//  1. the board's current DB sensor inventory,
//  2. the board's last accepted DeviceConfig,
//  3. the caller's requested overrides.
//
// Sensors are matched by hardware identity — (mux_path, i2c_address) — per
// PushDeviceConfigRequest.sensors' existing contract, never by name (a
// rename changes the name, so name matching would fork a sensor into two
// entries). sensor_type only enters the match when the DB inventory shows
// two or more sensors co-located at the same (mux_path, i2c_address) —
// e.g. an SHT3x's temperature and humidity virtual sensors — since that is
// the only situation where (mux_path, i2c_address) alone is ambiguous. For
// every other address (the common, single-sensor-per-address case),
// matching stays address-only, so overrides that (correctly, per
// SensorConfig.sensor_type's own doc comment) leave sensor_type at its
// proto3 zero value keep matching that one sensor. A lastAccepted or
// override entry for a co-located address that itself leaves sensor_type
// at the zero value is genuinely ambiguous (more than one candidate, none
// named) — such an override is applied to none of the co-located sensors
// (every one of them is carried through unchanged, not dropped); such a
// lastAccepted entry is skipped (defensive only: a previous compose's
// output always carries the real, non-zero sensor_type for a co-located
// sensor, per the DB-inventory loop below, so this should be unreachable
// in practice).
//
// Every sensor known from the DB inventory or the last accepted config
// appears in the output exactly once, whether or not the caller mentioned
// it. An override entry matching no known hardware identity is added to the
// output (that is how a new sensor is configured), not dropped and not an
// error. Output ordering is deterministic (sorted by hardware identity) so
// two composes of the same state produce byte-identical protos.
func ComposeDesiredSensors(
	inventory []InventorySensor,
	lastAccepted []*configpb.SensorConfig,
	overrides []*configpb.SensorConfig,
) []*configpb.SensorConfig {
	base := make(map[hwKey]*configpb.SensorConfig, len(inventory))
	order := make([]hwKey, 0, len(inventory))
	// idx tracks hardware-address co-location live as entries are added to
	// base below (inventory, then lastAccepted, then overrides, in that
	// precedence order) -- not just from inventory. A sensor known only
	// from lastAccepted (the DB has not caught up yet, criterion 4) must
	// still register as "the one sensor at this address" so a same-address
	// override that omits sensor_type keeps matching it, exactly as it
	// would if the sensor were already in inventory.
	idx := newHWIndex()

	// Lowest precedence: DB inventory. Every sensor the board has ever
	// registered gets a stand-in entry, even if no config has ever
	// mentioned it. Keyed on the full (address, sensor_type) identity since
	// inventory is the one input that always carries a real, resolved
	// sensor_type (see InventorySensor.SensorType's doc comment) -- this is
	// what lets two co-located sensors get distinct entries here.
	for _, inv := range inventory {
		key := makeHWKey(inv.I2CAddress, inv.MuxPath, inv.SensorType)
		if _, exists := base[key]; !exists {
			order = append(order, key)
			idx.add(inv.I2CAddress, inv.MuxPath, key)
		}
		base[key] = &configpb.SensorConfig{
			MuxPath:    inv.MuxPath,
			I2CAddress: inv.I2CAddress,
			Name:       inv.Name,
			SensorType: inv.SensorType,
			RegionId:   regionIDToUint32(inv.RegionID),
		}
	}

	// Next precedence: the last accepted config. Each entry there is
	// already a fully-resolved SensorConfig (not a sparse override), so it
	// replaces the inventory-derived stand-in wholesale. This is also how a
	// sensor the DB inventory has not caught up on (present in the last
	// accepted config but not yet in the DB) is carried through.
	for _, la := range lastAccepted {
		key, ok := idx.resolve(la.GetI2CAddress(), la.GetMuxPath(), la.GetSensorType())
		if !ok {
			continue
		}
		if _, exists := base[key]; !exists {
			order = append(order, key)
			idx.add(la.GetI2CAddress(), la.GetMuxPath(), key)
		}
		base[key] = la
	}

	// Highest precedence: caller overrides, applied field-by-field onto the
	// matched base entry.
	for _, ov := range overrides {
		key, ok := idx.resolve(ov.GetI2CAddress(), ov.GetMuxPath(), ov.GetSensorType())
		if !ok {
			// Co-located address, override doesn't say which sensor it
			// means: apply to none rather than guess or drop a sibling --
			// see this function's doc comment.
			continue
		}
		existing, found := base[key]
		if !found {
			order = append(order, key)
			idx.add(ov.GetI2CAddress(), ov.GetMuxPath(), key)
			existing = &configpb.SensorConfig{
				MuxPath:    ov.GetMuxPath(),
				I2CAddress: ov.GetI2CAddress(),
			}
		}
		base[key] = applyOverride(existing, ov)
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]*configpb.SensorConfig, 0, len(order))
	for _, k := range order {
		out = append(out, base[k])
	}
	return out
}

// TouchedSensorIDs returns the sensor_id of every inventory entry whose
// hardware identity matches one of overrides -- i.e. the sensors a
// caller-driven config push (FR8) actually named, as opposed to every
// sensor ComposeDesiredSensors carries through in its output. Matching
// follows the same address-only-unless-co-located rule as
// ComposeDesiredSensors (see its doc comment): an override matching no
// inventory entry (configuring a brand-new sensor for the first time), or
// an ambiguous override for a co-located address that omits sensor_type,
// has no single resolvable sensor_id and is omitted -- never guessed.
//
// Used by leaflab/api's PushDeviceConfig to know which sensors' NFR4
// corrective-push retry counters an explicit push re-arms: only the
// sensors the caller actually pushed, not the whole board's composed list
// -- see leaflab plan #1756/#1772 NFR4's "Reset" note ("only a fresh
// legitimate rename or an explicit config push for that sensor resets the
// attempt count").
func TouchedSensorIDs(inventory []InventorySensor, overrides []*configpb.SensorConfig) []int64 {
	idx := newHWIndexFromInventory(inventory)
	byKey := make(map[hwKey]int64, len(inventory))
	for _, inv := range inventory {
		byKey[makeHWKey(inv.I2CAddress, inv.MuxPath, inv.SensorType)] = inv.SensorID
	}
	var out []int64
	for _, ov := range overrides {
		key, ok := idx.resolve(ov.GetI2CAddress(), ov.GetMuxPath(), ov.GetSensorType())
		if !ok {
			continue
		}
		if id, exists := byKey[key]; exists {
			out = append(out, id)
		}
	}
	return out
}

// applyOverride returns a new SensorConfig combining base (the last-accepted
// or inventory-derived entry for this hardware identity) with ov's
// explicitly-set fields. A scalar field on ov is treated as "set" when it
// differs from its proto3 zero value — true to each field's own documented
// sentinel (poll_interval_ms: "0 = use device default"; sensor_type: leave
// as SENSOR_TYPE_UNKNOWN for single-ISensor chips; chip_type: UNKNOWN =
// legacy patch-only) rather than an ad hoc convention invented here. Enabled
// is the one field with real proto3 presence (optional bool) and is merged
// via nil-check accordingly.
//
// mux_path/i2c_address are not merged — they are the match key itself, so
// ov's values (which produced this match) are used directly.
func applyOverride(base, ov *configpb.SensorConfig) *configpb.SensorConfig {
	out := &configpb.SensorConfig{
		MuxPath:        ov.GetMuxPath(),
		I2CAddress:     ov.GetI2CAddress(),
		Name:           base.GetName(),
		Enabled:        base.Enabled,
		PollIntervalMs: base.GetPollIntervalMs(),
		SensorType:     base.GetSensorType(),
		RegionId:       base.GetRegionId(),
		ChipType:       base.GetChipType(),
	}
	if ov.GetName() != "" {
		out.Name = ov.GetName()
	}
	if ov.Enabled != nil {
		out.Enabled = ov.Enabled
	}
	if ov.GetPollIntervalMs() != 0 {
		out.PollIntervalMs = ov.GetPollIntervalMs()
	}
	if ov.GetSensorType() != 0 {
		out.SensorType = ov.GetSensorType()
	}
	if ov.GetRegionId() != 0 {
		out.RegionId = ov.GetRegionId()
	}
	if ov.GetChipType() != 0 {
		out.ChipType = ov.GetChipType()
	}
	return out
}

// regionIDToUint32 converts an InventorySensor's nullable DB region_id to
// SensorConfig.region_id's wire representation (0 = no region), matching
// the proto's own "server-side only" field.
func regionIDToUint32(regionID *int64) uint32 {
	if regionID == nil {
		return 0
	}
	return uint32(*regionID)
}

// addrKey is the coarser (i2c_address, mux_path)-only identity -- a
// hardware address that may host one sensor or, for a chip like the SHT3x,
// several co-located virtual sensors differing only by sensor_type. It is
// used solely to detect co-location (see hwIndex); the base map itself is
// always keyed by the finer hwKey.
type addrKey string

// hwKey is the hardware-identity match key the base map in
// ComposeDesiredSensors/TouchedSensorIDs is keyed by: (i2c_address,
// mux_path, sensor_type). sensor_type is folded in unconditionally here --
// the DB inventory always resolves a real, non-zero SensorType (see
// InventorySensor.SensorType's doc comment), so every inventory-derived key
// is already fully qualified. Fixed-width numeric encoding keeps string
// sorting equivalent to numeric sorting, so output ordering is both
// deterministic and stable across permutations of the input lists.
//
// Callers matching a lastAccepted/override entry (whose sensor_type may
// legitimately be the proto3 zero value) against this map must not call
// makeHWKey directly -- use hwIndex.resolve, which applies
// ComposeDesiredSensors' documented address-only-unless-co-located rule.
type hwKey string

func makeAddrKey(i2cAddress uint32, muxPath []*configpb.MuxHop) addrKey {
	var b strings.Builder
	fmt.Fprintf(&b, "%010d|", i2cAddress)
	for _, hop := range muxPath {
		fmt.Fprintf(&b, "%010d:%010d>", hop.GetMuxAddress(), hop.GetMuxChannel())
	}
	return addrKey(b.String())
}

func makeHWKey(i2cAddress uint32, muxPath []*configpb.MuxHop, sensorType firmwarepb.SensorType) hwKey {
	return hwKey(fmt.Sprintf("%s#%010d", makeAddrKey(i2cAddress, muxPath), int32(sensorType)))
}

// hwIndex resolves a lastAccepted/override entry's hardware address and
// (possibly zero-value) sensor_type to the hwKey it should match, tracking
// how many distinct sensors are known at each hardware address as they are
// registered via add.
type hwIndex struct {
	// count is, per hardware address, how many distinct sensors have been
	// registered there so far.
	count map[addrKey]int
	// single is, per hardware address with exactly one registered sensor,
	// that sensor's full hwKey. Absent (including after being deleted, once
	// a second sensor registers at the address) means "no unambiguous
	// single sensor known here".
	single map[addrKey]hwKey
}

func newHWIndex() *hwIndex {
	return &hwIndex{count: make(map[addrKey]int), single: make(map[addrKey]hwKey)}
}

// newHWIndexFromInventory builds an hwIndex from a board's DB inventory
// alone -- what TouchedSensorIDs needs, since only inventory sensors have a
// real sensor_id to report.
func newHWIndexFromInventory(inventory []InventorySensor) *hwIndex {
	idx := newHWIndex()
	for _, inv := range inventory {
		idx.add(inv.I2CAddress, inv.MuxPath, makeHWKey(inv.I2CAddress, inv.MuxPath, inv.SensorType))
	}
	return idx
}

// add registers a newly-seen hwKey at the given hardware address, updating
// co-location bookkeeping. Callers must call this exactly once per distinct
// key the first time it is added to the composition's base map (never for
// an update to an already-present key) -- see its call sites in
// ComposeDesiredSensors.
func (idx *hwIndex) add(i2cAddress uint32, muxPath []*configpb.MuxHop, key hwKey) {
	ak := makeAddrKey(i2cAddress, muxPath)
	idx.count[ak]++
	if idx.count[ak] == 1 {
		idx.single[ak] = key
	} else {
		delete(idx.single, ak)
	}
}

// resolve returns the hwKey a lastAccepted/override entry with the given
// hardware address and sensor_type should match, and whether the match is
// well-defined. ok is false only when the address is co-located (idx.count
// > 1) and sensorType is the proto3 zero value (SENSOR_TYPE_UNKNOWN): more
// than one candidate sensor is known there and none was named, so the
// caller must not guess -- see ComposeDesiredSensors' doc comment for what
// each caller does with that.
func (idx *hwIndex) resolve(i2cAddress uint32, muxPath []*configpb.MuxHop, sensorType firmwarepb.SensorType) (key hwKey, ok bool) {
	ak := makeAddrKey(i2cAddress, muxPath)
	if idx.count[ak] <= 1 {
		if single, known := idx.single[ak]; known {
			return single, true
		}
		// No sensor registered at this address yet: key on address+type as
		// given so distinct sensor_types configured at the same new
		// address don't collide with each other.
		return makeHWKey(i2cAddress, muxPath, sensorType), true
	}
	if sensorType == 0 {
		return "", false
	}
	return makeHWKey(i2cAddress, muxPath, sensorType), true
}
