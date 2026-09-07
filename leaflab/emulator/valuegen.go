// valuegen.go implements synthetic sensor value generation (FR17): each
// simulated sensor produces a value randomized within a plausible range
// for its SensorType and evolving by a small random walk on each tick.
// This is self-contained pure logic — no MQTT, no board wiring, and
// deliberately not physically modeled (no day/night cycles, no
// cross-sensor correlation — see leaflab plan #1757 § Out of scope).
package main

import (
	firmwarepb "github.com/whale-net/everything/firmware/proto"
)

// TODO(scaffold): implement Walker, NewWalker, Next, and UnitFor per issue
// #1766. Left as a scaffold stub so the file compiles and is wired into the
// BUILD target ahead of implementation.

// Walker produces one sensor's synthetic value series. Not safe for
// concurrent use: one Walker per simulated sensor.
type Walker struct {
	sensorType firmwarepb.SensorType
}
