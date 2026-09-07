// valuegen.go implements synthetic sensor value generation (FR17): each
// simulated sensor produces a value randomized within a plausible range
// for its SensorType and evolving by a small random walk on each tick.
// This is self-contained pure logic — no MQTT, no board wiring, and
// deliberately not physically modeled (no day/night cycles, no
// cross-sensor correlation — see leaflab plan #1757 § Out of scope).
package main

import (
	"math/rand"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
)

// valueRange describes the plausible range and per-tick random-walk step
// for one SensorType. step is the maximum absolute change per tick.
type valueRange struct {
	min, max, step float32
	unit           string
}

// valueRanges is the single source of truth for both the synthetic value
// range/step (Walker) and the SI unit string (UnitFor), so the two cannot
// drift apart.
var valueRanges = map[firmwarepb.SensorType]valueRange{
	firmwarepb.SensorType_SENSOR_TYPE_ILLUMINANCE: {min: 0, max: 2000, step: 50, unit: "lx"},
	firmwarepb.SensorType_SENSOR_TYPE_TEMPERATURE: {min: 15, max: 35, step: 0.3, unit: "°C"},
	firmwarepb.SensorType_SENSOR_TYPE_HUMIDITY:    {min: 20, max: 90, step: 1.0, unit: "%RH"},
	firmwarepb.SensorType_SENSOR_TYPE_ECO2:        {min: 400, max: 2000, step: 25, unit: "ppm"},
	firmwarepb.SensorType_SENSOR_TYPE_TVOC:        {min: 0, max: 1000, step: 15, unit: "ppb"},
	firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN:     {min: 0, max: 100, step: 1.0, unit: ""},
}

// rangeFor returns the valueRange for t, falling back to the UNKNOWN entry
// for any type not in the table (e.g. a future SensorType this mapping has
// not caught up to yet) so callers always get a usable, finite range.
func rangeFor(t firmwarepb.SensorType) valueRange {
	if r, ok := valueRanges[t]; ok {
		return r
	}
	return valueRanges[firmwarepb.SensorType_SENSOR_TYPE_UNKNOWN]
}

// UnitFor returns the SI unit string for t ("lx", "°C", "%RH", "ppm",
// "ppb"), or "" for UNKNOWN and any unrecognized SensorType.
func UnitFor(t firmwarepb.SensorType) string {
	return rangeFor(t).unit
}

// Walker produces one sensor's synthetic value series. Not safe for
// concurrent use: one Walker per simulated sensor.
type Walker struct {
	rng   *rand.Rand
	r     valueRange
	value float32
}

// NewWalker seeds a walker for the given sensor type from rng. The first
// Next() value is uniform in the type's range; later values random-walk
// from it. Pass a *rand.Rand derived from the process seed plus the
// sensor's identity -- never share one *rand.Rand across goroutines, and
// never call the global math/rand package functions here.
func NewWalker(t firmwarepb.SensorType, rng *rand.Rand) *Walker {
	r := rangeFor(t)
	initial := r.min + rng.Float32()*(r.max-r.min)
	return &Walker{
		rng:   rng,
		r:     r,
		value: initial,
	}
}

// Next advances the walk one tick and returns the new value: a uniform
// random delta in [-step, +step] is added to the current value, then the
// result is clamped (not reflected or wrapped) to [min, max] so a
// long-running walk cannot drift out of a physically plausible range.
func (w *Walker) Next() float32 {
	delta := (w.rng.Float32()*2 - 1) * w.r.step
	v := w.value + delta
	if v < w.r.min {
		v = w.r.min
	}
	if v > w.r.max {
		v = w.r.max
	}
	w.value = v
	return w.value
}
