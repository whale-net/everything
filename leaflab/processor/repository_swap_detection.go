package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// SensorState represents a sensor's current hardware and type state.
type SensorState struct {
	SensorID int64
	Name     string
	TypeID   int64
	HW       *HardwareAddress
}

// GetSensorsByBoard returns all sensors on a board with their current hardware addresses.
func (r *Repository) GetSensorsByBoard(ctx context.Context, boardID int64) ([]SensorState, error) {
	rows, err := r.db.Query(ctx, `
		SELECT s.sensor_id, s.name, s.sensor_type_id, s.i2c_address, s.mux_path
		FROM sensor s
		WHERE s.board_id = $1
	`, boardID)
	if err != nil {
		return nil, fmt.Errorf("get sensors for board %d: %w", boardID, err)
	}
	defer rows.Close()

	var sensors []SensorState
	for rows.Next() {
		var sensorID, typeID int64
		var name string
		var i2cAddr *int16
		var muxJSON []byte
		if err := rows.Scan(&sensorID, &name, &typeID, &i2cAddr, &muxJSON); err != nil {
			return nil, fmt.Errorf("scan sensor row: %w", err)
		}

		var hw *HardwareAddress
		if i2cAddr != nil && *i2cAddr > 0 {
			hw = &HardwareAddress{I2CAddress: uint32(*i2cAddr)}
			var muxPath []MuxHop
			if err := json.Unmarshal(muxJSON, &muxPath); err != nil {
				return nil, fmt.Errorf("unmarshal mux_path: %w", err)
			}
			hw.MuxPath = muxPath
		}

		sensors = append(sensors, SensorState{
			SensorID: sensorID,
			Name:     name,
			TypeID:   typeID,
			HW:       hw,
		})
	}
	return sensors, rows.Err()
}
