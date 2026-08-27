package main

import (
	"sync"

	"github.com/whale-net/everything/leaflab/invalidation"
)

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

// Invalidate evicts the cached entry for deviceID/sensorName, if present.
// FR73: the next handleReading for this device/sensor falls through to
// MessageHandler.handleSensorReading's existing cache-miss path, which
// re-reads the current value from the database and repopulates the cache
// -- so eviction alone is what makes the cache self-heal to the current
// value, without this method needing to know what that value now is.
//
// For a rename (invalidation.KindName), callers must invalidate the
// sensor's *prior* name (invalidation.Event.PriorSensorName), not its new
// one: the cache is keyed device_id -> sensor_name, so the prior name's
// entry is an orphan a rename never touches unless evicted explicitly.
func (c *SensorCache) Invalidate(deviceID, sensorName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sensors, ok := c.devices[deviceID]; ok {
		delete(sensors, sensorName)
	}
}

// InvalidateDevice evicts every cached sensor entry for deviceID.
func (c *SensorCache) InvalidateDevice(deviceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.devices, deviceID)
}

// ReplaceAll atomically replaces the entire cached sensor set with entries.
// Unlike Load (an additive merge, used only for the one-time startup
// pre-warm), ReplaceAll also evicts any entry not present in entries -- this
// is what lets the bounded staleness backstop (see RunCacheBackstop)
// self-heal a *dropped* rename event, not just a dropped region/identity
// one: a Load-style merge would leave a rename's orphaned prior-name entry
// (see Invalidate's doc comment) in place forever if the invalidation event
// that would have evicted it explicitly never arrived.
//
// A concurrent handleManifest that cache.Set's a brand new sensor while a
// ReplaceAll from a snapshot taken just before that write is in flight can
// have its Set overwritten by this call. That's not a correctness bug: the
// sensor is on the database by the time ReplaceAll's snapshot was read,
// so it is either in entries already, or, in the rare interleaving above,
// missing for the moment -- handleSensorReading's existing cache-miss path
// (repository.GetSensor) re-reads and repopulates it on the very next
// reading. No reading is ever stamped with a wrong value; at worst one
// reading takes the DB-lookup path instead of a cache hit.
func (c *SensorCache) ReplaceAll(entries map[string]map[string]SensorInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fresh := make(map[string]map[string]SensorInfo, len(entries))
	for deviceID, sensors := range entries {
		copied := make(map[string]SensorInfo, len(sensors))
		for name, info := range sensors {
			copied[name] = info
		}
		fresh[deviceID] = copied
	}
	c.devices = fresh
}

// ApplyInvalidation applies a single FR73 invalidation.Event to cache,
// evicting exactly the cache key the event describes:
//
//   - invalidation.KindRegion / invalidation.KindIdentity evict
//     ev.DeviceID/ev.SensorName -- the cache key the change was observed
//     under.
//   - invalidation.KindName (a rename) evicts ev.DeviceID/ev.PriorSensorName
//     -- the cache is keyed device_id -> sensor_name, so a rename's *new*
//     name was never a key to begin with; evicting the prior one is what
//     prevents the orphaned entry SensorCache.Invalidate's doc comment
//     describes.
//
// This is a package-level function, not a method on MessageHandler or
// SensorCache, precisely so both main.go's Subscriber.Start handler and a
// test can call the exact same decision logic without needing a real
// broker, a MessageHandler, or any of MessageHandler's other dependencies.
func ApplyInvalidation(cache *SensorCache, ev invalidation.Event) {
	if ev.Kind == invalidation.KindName {
		cache.Invalidate(ev.DeviceID, ev.PriorSensorName)
		return
	}
	cache.Invalidate(ev.DeviceID, ev.SensorName)
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
