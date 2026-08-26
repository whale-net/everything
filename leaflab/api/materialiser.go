package main

import (
	"fmt"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// Materialiser handles config materialisation for EDIT scope pushes (FR82).
type Materialiser struct{}

// NewMaterialiser creates a new config materialiser.
func NewMaterialiser() *Materialiser {
	return &Materialiser{}
}

// Materialize takes a base config (from the accepted version) and an EDIT request
// and produces a complete config by combining:
// - Entries named in the request (AUTHORED)
// - Entries carried forward from the base (MATERIALISED)
// - Minus entries in the remove list
//
// Returns the materialised config and a map of entry indices to provenance,
// or an error if the base is nil (no accepted config to complete from).
func (m *Materialiser) Materialize(
	baseConfig *configpb.DeviceConfig,
	editPayload *pb.PushDeviceConfigRequest,
) (*configpb.DeviceConfig, map[int]pb.Provenance, error) {
	if baseConfig == nil {
		return nil, nil, fmt.Errorf("materialiser: no base config to complete edit from; send a complete push")
	}

	// TODO: Implement materialisation logic
	// 1. Start with base sensors
	// 2. Apply edits (add/update sensors from editPayload)
	// 3. Apply removals (remove by canonical key or chip key)
	// 4. Build provenance map tracking AUTHORED vs MATERIALISED
	
	_ = editPayload // placeholder
	return nil, nil, fmt.Errorf("materialiser: not yet implemented")
}
