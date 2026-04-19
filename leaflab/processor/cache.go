package main

import "sync"

// SensorInfo holds the DB IDs needed to write a sensor_reading row.
type SensorInfo struct {
	SensorID int64
	RegionID *int64 // nil if sensor not yet placed in a region
}

// SensorCache is an in-memory lookup of registered sensors, keyed by
// device_id → sensor_name → SensorInfo. Populated on manifest receipt;
// consulted on every reading.
type SensorCache struct {
	mu      sync.RWMutex
	devices map[string]map[string]SensorInfo // device_id → name → info
}

func NewSensorCache() *SensorCache {
	return &SensorCache{devices: make(map[string]map[string]SensorInfo)}
}

// Set registers or updates a sensor entry for a device.
func (c *SensorCache) Set(deviceID, sensorName string, info SensorInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.devices[deviceID] == nil {
		c.devices[deviceID] = make(map[string]SensorInfo)
	}
	c.devices[deviceID][sensorName] = info
}

// Get returns the SensorInfo for a sensor, and whether it was found.
func (c *SensorCache) Get(deviceID, sensorName string) (SensorInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	sensors, ok := c.devices[deviceID]
	if !ok {
		return SensorInfo{}, false
	}
	info, ok := sensors[sensorName]
	return info, ok
}
