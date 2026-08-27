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
//
// Phase 4 (FR45/FR47/NFR15) extends this package with KindAck rather than
// adding a sibling topic or a second exchange: the processor's ack write
// path (leaflab/processor/handler.go's handleConfigAck) publishes one
// KindAck Event after leaflab/processor/repository.go's AckDeviceConfig
// commits, and every API replica's own Subscriber -- the same one already
// wired for FR73 -- observes it and resolves any AwaitConfigAck waiter
// registered for that (DeviceID, Version) in that replica (see
// leaflab/api/ackwait.Registry). Because fanout delivers a copy to every
// replica rather than one of a competing set, a bounded wait pinned to any
// one replica (FR47) resolves the same way regardless of which replica
// received the request -- NFR15's falsifiable claim.
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
	// KindAck means a pushed device config version's ack resolved --
	// written only from the processor's ack path (handleConfigAck ->
	// AckDeviceConfig, FR45: the three ack columns are never written from
	// any API request handler, in any role). DeviceID and Version
	// identify which push; Accepted and RejectionReason carry the
	// board's verbatim outcome. A handler for this kind resolves an
	// FR47 AwaitConfigAck waiter rather than evicting a cache entry --
	// see leaflab/api/ackwait.Registry.Notify, this event's only Phase 4
	// consumer.
	KindAck Kind = "ack"
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

	// Version is the device_config version this event concerns. Set only
	// for Kind == KindAck; every other Kind leaves it zero.
	Version int64
	// Accepted is the board's ack outcome for (DeviceID, Version). Set
	// only for Kind == KindAck; meaningless for any other Kind.
	Accepted bool
	// RejectionReason is the firmware's verbatim rejection reason. Set
	// only for Kind == KindAck when Accepted is false -- never paraphrased
	// or normalised, mirroring device_config.rejection_reason (FR45).
	RejectionReason string

	// ObservedAt is when the publishing process observed the change that
	// produced this event -- not necessarily the exact commit timestamp,
	// but always at or after it (see Publisher.Publish).
	ObservedAt time.Time
}
