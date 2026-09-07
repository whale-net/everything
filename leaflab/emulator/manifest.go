// manifest.go builds the retained DeviceManifest published on connect (and
// re-published after a config apply, in a later task). See leaflab/MQTT.md
// for the wire shape this must match exactly.
package main

import (
	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
)

// chipModelNames maps configpb.ChipType to the catalog "name:" value in
// firmware/sensor/catalog/chips.yaml. Casing must match exactly -- the
// processor joins sensor rows to the catalog on this string.
var chipModelNames = map[configpb.ChipType]string{
	configpb.ChipType_CHIP_TYPE_BH1750: "BH1750",
	configpb.ChipType_CHIP_TYPE_SHT3X:  "SHT3x",
	configpb.ChipType_CHIP_TYPE_CCS811: "CCS811",
}

// BuildManifest constructs the DeviceManifest for b: one SensorDescriptor
// per enabled sensor. Disabled sensors are omitted entirely -- they also
// publish no readings (runner.go).
func BuildManifest(b Board) *firmwarepb.DeviceManifest {
	descriptors := make([]*firmwarepb.SensorDescriptor, 0, len(b.Sensors))
	for _, s := range b.Sensors {
		if !s.Enabled {
			continue
		}
		descriptors = append(descriptors, &firmwarepb.SensorDescriptor{
			Name:       s.Name,
			Type:       s.SensorType,
			Unit:       UnitFor(s.SensorType),
			I2CAddress: s.I2CAddress,
			MuxAddress: s.MuxAddress,
			MuxChannel: s.MuxChannel,
			ChipModel:  chipModelNames[s.ChipType],
		})
	}

	return &firmwarepb.DeviceManifest{
		DeviceId: b.DeviceID,
		Sensors:  descriptors,
	}
}
