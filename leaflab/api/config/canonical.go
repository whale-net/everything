package config

import (
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// CanonicalKey builds sc's canonical hardware key (FR18: i2c_address,
// mux_path, sensor_type) given its already-resolved sensor_type_id.
// Canonicalisation of i2c_address and mux_path is entirely
// //leaflab/hwkey's job; this only assembles the three components sc and
// sensorTypeID contribute -- resolving a proto SensorType to a
// SensorTypeID is the sensor_type catalog's job (see
// leaflab/api/repository.go's resolveSensorTypeID), not this package's:
// no function here talks to the catalog.
//
// sc.I2cAddress == 0 is preserved as hwkey's "unknown address" sentinel
// (AddressOpt.IsUnknownSentinel), never folded into Absent -- FR82.4
// depends on being able to tell the two apart (RemoveKey.Match rejects an
// attempt to remove an unaddressable entry). This differs deliberately
// from leaflab/api's HardwareAddressFromSensorConfig (FR16/FR17 identity
// resolution), which treats both as "no usable hardware address to match
// on" and has no reason to keep them apart.
func CanonicalKey(sc *configpb.SensorConfig, sensorTypeID hwkey.SensorTypeID) hwkey.Key {
	return hwkey.Key{
		I2CAddress:   hwkey.Address(uint16(sc.GetI2CAddress())),
		MuxPath:      canonicalMuxPath(sc.GetMuxPath()),
		SensorTypeID: sensorTypeID,
	}
}

func canonicalMuxPath(hops []*configpb.MuxHop) hwkey.MuxPath {
	path := make(hwkey.MuxPath, len(hops))
	for i, h := range hops {
		path[i] = hwkey.MuxHop{MuxAddress: h.GetMuxAddress(), MuxChannel: h.GetMuxChannel()}
	}
	return path
}
