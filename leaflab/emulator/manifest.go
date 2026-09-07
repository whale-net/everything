// manifest.go builds the retained DeviceManifest published on connect (and
// re-published after a config apply, in a later task). Full
// descriptor-building logic -- including the chip_model catalog-name
// mapping -- lands in the Implementation phase; see leaflab/MQTT.md for
// the wire shape this must match exactly.
package main

import (
	firmwarepb "github.com/whale-net/everything/firmware/proto"
)

// BuildManifest constructs the DeviceManifest for b: one SensorDescriptor
// per enabled sensor (disabled sensors are omitted). Stub -- full
// construction lands in the Implementation phase.
func BuildManifest(b Board) *firmwarepb.DeviceManifest {
	return &firmwarepb.DeviceManifest{
		DeviceId: b.DeviceID,
	}
}
