package main

import (
	"fmt"
)

// HardwareKey represents the canonical hardware identifier for a sensor.
type HardwareKey struct {
	I2CAddress uint32
	MuxPath    string // JSON-serialized
	TypeID     int64
}

// String returns a debug representation of the hardware key.
func (k HardwareKey) String() string {
	return fmt.Sprintf("i2c=0x%02x,mux=%s,type=%d", k.I2CAddress, k.MuxPath, k.TypeID)
}

// DetectSwap checks if a manifest would create a hardware key swap.
// A swap occurs when two sensors exchange hardware keys in the same manifest.
// Returns (swapped, sensor1Name, sensor2Name, conflictingKey) if a swap is detected.
func DetectSwap(existingSensors []SensorState, manifestSensors map[string]*ManifestSensorInfo) (bool, string, string, string) {
	// Build a map of existing hardware keys to sensor names
	existingKeyToName := make(map[string]string)
	existingNameToKey := make(map[string]string)

	for _, sensor := range existingSensors {
		if sensor.HW != nil && sensor.HW.I2CAddress > 0 {
			key := hardwareKeyString(sensor.HW, sensor.TypeID)
			existingKeyToName[key] = sensor.Name
			existingNameToKey[sensor.Name] = key
		}
	}

	// For each sensor in the manifest, check if its new key collides with another sensor's old key
	for manifestName, manifestInfo := range manifestSensors {
		if manifestInfo.HW == nil || manifestInfo.HW.I2CAddress == 0 {
			continue // No hardware address, no swap possible
		}

		newKey := hardwareKeyString(manifestInfo.HW, manifestInfo.TypeID)

		// Check if this new key was used by a different sensor
		if existingName, ok := existingKeyToName[newKey]; ok && existingName != manifestName {
			// There's a collision! Now check if it's a swap
			// A swap means: old_key(manifestName) = new_key(existingName)
			if oldManifestKey, hasOldKey := existingNameToKey[manifestName]; hasOldKey {
				// Check if the other sensor's new key would be our old key
				otherManifestInfo, hasOther := manifestSensors[existingName]
				if hasOther && otherManifestInfo.HW != nil && otherManifestInfo.HW.I2CAddress > 0 {
					otherNewKey := hardwareKeyString(otherManifestInfo.HW, otherManifestInfo.TypeID)
					if otherNewKey == oldManifestKey {
						// This is a swap! manifestName and existingName are swapping keys
						return true, manifestName, existingName, newKey
					}
				}
			}
		}
	}

	return false, "", "", ""
}

// hardwareKeyString returns a canonical string representation of a hardware key
func hardwareKeyString(hw *HardwareAddress, typeID int64) string {
	if hw == nil || hw.I2CAddress == 0 {
		return ""
	}
	// We use a simple format: "i2c_address_muxpath_typeid"
	// This avoids JSON marshaling overhead for the comparison
	muxStr := ""
	if len(hw.MuxPath) > 0 {
		// Simple mux path representation
		for _, hop := range hw.MuxPath {
			muxStr += fmt.Sprintf(":%d:%d", hop.MuxAddress, hop.MuxChannel)
		}
	}
	return fmt.Sprintf("%d%s:%d", hw.I2CAddress, muxStr, typeID)
}

// ManifestSensorInfo represents a sensor from the manifest being processed.
type ManifestSensorInfo struct {
	Name   string
	TypeID int64
	HW     *HardwareAddress
}
