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
// entries).
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

	// Lowest precedence: DB inventory. Every sensor the board has ever
	// registered gets a stand-in entry, even if no config has ever
	// mentioned it.
	for _, inv := range inventory {
		key := makeHWKey(inv.I2CAddress, inv.MuxPath)
		if _, exists := base[key]; !exists {
			order = append(order, key)
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
		key := makeHWKey(la.GetI2CAddress(), la.GetMuxPath())
		if _, exists := base[key]; !exists {
			order = append(order, key)
		}
		base[key] = la
	}

	// Highest precedence: caller overrides, applied field-by-field onto the
	// matched base entry.
	for _, ov := range overrides {
		key := makeHWKey(ov.GetI2CAddress(), ov.GetMuxPath())
		existing, found := base[key]
		if !found {
			order = append(order, key)
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
// hardware identity (i2c_address, mux_path) matches one of overrides --
// i.e. the sensors a caller-driven config push (FR8) actually named, as
// opposed to every sensor ComposeDesiredSensors carries through in its
// output. An override matching no inventory entry (configuring a brand-new
// sensor for the first time) has no existing sensor_id and is omitted.
//
// Used by leaflab/api's PushDeviceConfig to know which sensors' NFR4
// corrective-push retry counters an explicit push re-arms: only the
// sensors the caller actually pushed, not the whole board's composed list
// -- see leaflab plan #1756/#1772 NFR4's "Reset" note ("only a fresh
// legitimate rename or an explicit config push for that sensor resets the
// attempt count").
func TouchedSensorIDs(inventory []InventorySensor, overrides []*configpb.SensorConfig) []int64 {
	byKey := make(map[hwKey]int64, len(inventory))
	for _, inv := range inventory {
		byKey[makeHWKey(inv.I2CAddress, inv.MuxPath)] = inv.SensorID
	}
	var out []int64
	for _, ov := range overrides {
		if id, ok := byKey[makeHWKey(ov.GetI2CAddress(), ov.GetMuxPath())]; ok {
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

// hwKey is the hardware-identity match key shared by ComposeDesiredSensors'
// three input lists: (i2c_address, mux_path). Fixed-width numeric encoding
// keeps string sorting equivalent to numeric sorting, so output ordering is
// both deterministic and stable across permutations of the input lists.
type hwKey string

func makeHWKey(i2cAddress uint32, muxPath []*configpb.MuxHop) hwKey {
	var b strings.Builder
	fmt.Fprintf(&b, "%010d|", i2cAddress)
	for _, hop := range muxPath {
		fmt.Fprintf(&b, "%010d:%010d>", hop.GetMuxAddress(), hop.GetMuxChannel())
	}
	return hwKey(b.String())
}
