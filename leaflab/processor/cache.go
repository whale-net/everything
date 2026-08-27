package main

import "sync"

// SensorInfo holds the DB IDs needed to write a sensor_reading row.
type SensorInfo struct {
	SensorID int64
	RegionID *int64 // nil if sensor not yet placed in a region
}

// SensorCache is an in-memory lookup of registered sensors, keyed by
// device_id → sensor_name → SensorInfo. Also tracks the latest accepted
// config version per device so readings can be stamped at write time.
type SensorCache struct {
	mu             sync.RWMutex
	devices        map[string]map[string]SensorInfo // device_id → name → info
	configVersions map[string]int64                 // device_id → latest accepted version
}

func NewSensorCache() *SensorCache {
	return &SensorCache{
		devices:        make(map[string]map[string]SensorInfo),
		configVersions: make(map[string]int64),
	}
}

// Load bulk-populates the sensor entries, typically called at startup from a DB snapshot.
func (c *SensorCache) Load(entries map[string]map[string]SensorInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for deviceID, sensors := range entries {
		if c.devices[deviceID] == nil {
			c.devices[deviceID] = make(map[string]SensorInfo)
		}
		for name, info := range sensors {
			c.devices[deviceID][name] = info
		}
	}
}

// LoadConfigVersions bulk-populates config version entries at startup.
func (c *SensorCache) LoadConfigVersions(versions map[string]int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for deviceID, v := range versions {
		c.configVersions[deviceID] = v
	}
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

// SetConfigVersion records the latest accepted config version for a device.
func (c *SensorCache) SetConfigVersion(deviceID string, version int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.configVersions[deviceID] = version
}

// GetConfigVersion returns the latest accepted config version for a device,
// and whether one has been recorded. Returns (0, false) if no config has been
// pushed and accepted for this device.
func (c *SensorCache) GetConfigVersion(deviceID string) (int64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.configVersions[deviceID]
	return v, ok
}

// ApplyInvalidation processes a cache invalidation signal and updates the cache accordingly.
// This implements FR73: after any change to a sensor's region, identity or cache key,
// every cached view must be invalidated or updated.
func (c *SensorCache) ApplyInvalidation(signal *CacheInvalidationSignal) {
	c.mu.Lock()
	defer c.mu.Unlock()

	sensors, ok := c.devices[signal.DeviceID]
	if !ok {
		// Device not yet in cache; nothing to invalidate
		return
	}

	switch signal.ChangeType {
	case "region":
		// Region changed: update the cached RegionID
		// Find the sensor by name (current name if no rename, old name if renamed)
		sensorName := signal.NewName
		if sensorName == "" {
			sensorName = signal.OldName
		}

		if info, exists := sensors[sensorName]; exists {
			info.RegionID = signal.RegionID
			sensors[sensorName] = info
		}

	case "rename":
		// Rename: delete the old name, insert the new name
		if signal.OldName != "" {
			if info, exists := sensors[signal.OldName]; exists {
				delete(sensors, signal.OldName)
				// Re-insert with the new name
				if signal.NewName != "" {
					sensors[signal.NewName] = info
				}
			}
		}

	case "rewire":
		// Rewire: invalidate all entries for this device and force reload on next access
		// The sensor identity is preserved but the hardware key (canonical key) changed.
		// This requires a full cache invalidation since the old entry is stale.
		for name := range sensors {
			delete(sensors, name)
		}

	case "identity":
		// Identity change (catch-all for other structural changes):
		// Invalidate all entries for this device
		for name := range sensors {
			delete(sensors, name)
		}
	}
}
