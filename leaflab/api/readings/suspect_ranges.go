package readings

import (
	"context"
	"fmt"
)

// measurementRange is a fixed, physically-plausible bound for one
// measurement type's value (FR26.1: "an out-of-range value is presented
// as suspect rather than as fact"). V1 has no per-household or per-sensor
// override anywhere in the schema (sensor_type carries only name and
// default_unit, migration 001) -- a value outside this range is
// out-of-range for every sensor of that type, everywhere.
type measurementRange struct {
	Min, Max float64
}

// measurementRangesByName is keyed by sensor_type.name (migration 001's
// seed rows: illuminance, temperature, humidity) rather than
// sensor_type_id, since ids are BIGSERIAL-assigned and not guaranteed
// stable across environments/tests. Deliberately generous (a real sensor
// glitch is expected to blow well past these, not brush up against them)
// -- this check exists to catch "the sensor is clearly lying," not to
// second-guess a plausible reading.
var measurementRangesByName = map[string]measurementRange{
	"illuminance": {Min: 0, Max: 200000}, // lux; direct sun is roughly 100-130k lx
	"temperature": {Min: -40, Max: 60},   // degrees C; generous ambient/greenhouse bound
	"humidity":    {Min: 0, Max: 100},    // relative humidity, percent
}

// measurementRanges loads every sensor_type row and resolves each to its
// measurementRangesByName entry, keyed by sensor_type_id -- the id that
// actually appears on sensor.sensor_type_id and travels with a reading. A
// sensor_type with no entry in measurementRangesByName (a future type this
// table hasn't been extended for) is simply absent from the returned map;
// callers must treat a missing entry as "no out-of-range check applies,"
// never as an error -- FR26.1 constrains what a reading is presented as,
// not what types this V1 table happens to know about.
func (r *Reader) measurementRanges(ctx context.Context) (map[int64]measurementRange, error) {
	rows, err := r.db.Query(ctx, `SELECT sensor_type_id, name FROM sensor_type`)
	if err != nil {
		return nil, fmt.Errorf("readings: load sensor_type ranges: %w", err)
	}
	defer rows.Close()

	ranges := make(map[int64]measurementRange)
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("readings: scan sensor_type row: %w", err)
		}
		if mr, ok := measurementRangesByName[name]; ok {
			ranges[id] = mr
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("readings: iterate sensor_type rows: %w", err)
	}
	return ranges, nil
}

// outOfRange reports whether value falls outside sensorTypeID's
// measurementRange in ranges. A sensorTypeID absent from ranges (see
// measurementRanges' doc comment) is never out-of-range -- absence of a
// defined range is not itself suspect.
func outOfRange(ranges map[int64]measurementRange, sensorTypeID int64, value float64) bool {
	mr, ok := ranges[sensorTypeID]
	if !ok {
		return false
	}
	return value < mr.Min || value > mr.Max
}
