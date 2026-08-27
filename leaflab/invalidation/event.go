// Package invalidation is FR73's cross-process signalling path: it lets any
// LeafLab writer -- the API's direct assignment or rewire, or the
// processor's own config-apply -- tell every other process that a cached
// view of a sensor (its region, identity or cache key) is stale, so no
// process stamps a reading with a value that a commit has already
// superseded.
//
// The chosen mechanism is a RabbitMQ fanout exchange (ExchangeName):
// Publisher publishes one Event per change, and every Subscriber gets its
// own exclusive queue bound to that exchange, so every subscribing process
// -- not just one of a competing set -- observes every event. This is the
// same broadcast constraint NFR15 imposes on the API's own bounded-wait
// observability (Phase 4), which is why Phase 4 reuses this package rather
// than inventing a second signalling path. See leaflab/ARCHITECTURE.md for
// the write-up of why this choice satisfies both FR73's 5s bound and
// NFR15's every-replica constraint.
package invalidation

import "time"

// Kind identifies which facet of a sensor's cached view changed.
type Kind string

const (
	// KindRegion means sensor.region_id changed -- written by the API's
	// direct region assignment (FR51, Phase 5) or the processor's own
	// ApplyConfigRegions (FR1.3).
	KindRegion Kind = "region"
	// KindIdentity means the sensor's identity/hardware key changed --
	// written by a rewire (FR16), including a rewire performed via the
	// device manifest path (FR16.3's elimination-resolved case) or the
	// API's explicit RewireSensor RPC.
	KindIdentity Kind = "identity"
	// KindName means the sensor's display name changed -- written by a
	// rename (FR52, Phase 5). PriorSensorName is always set for this kind.
	KindName Kind = "name"
)

// Event is the cross-process signal that a cached view of a sensor is
// stale. A Publisher publishes it only after the write it describes has
// committed (see Publisher.Publish's doc comment) -- never before.
//
// Event carries everything a Subscriber's handler needs to know which
// cache entry to evict; it deliberately does not carry the new value
// itself. A handler that received this event re-reads the current value
// from the database rather than trusting the event's payload, so a
// duplicate, reordered, or replayed event converges to the same result
// (idempotent) -- see leaflab/processor's SensorCache.Invalidate.
type Event struct {
	Kind Kind

	DeviceID   string
	SensorID   int64
	SensorName string

	// PriorSensorName is the sensor's name immediately before this event's
	// change, and is set only for Kind == KindName. A cache keyed
	// device_id -> sensor_name (see leaflab/processor's SensorCache) must
	// evict the entry under this prior key explicitly, or a rename leaves
	// an orphaned entry that SensorName alone (the new key) never touches.
	PriorSensorName string

	// ObservedAt is when the publishing process observed the change that
	// produced this event -- not necessarily the exact commit timestamp,
	// but always at or after it (see Publisher.Publish).
	ObservedAt time.Time
}
