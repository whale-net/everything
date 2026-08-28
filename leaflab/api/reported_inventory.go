package main

import (
	"context"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/config"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/hwkey"
)

// GetReportedInventory returns deviceID's board's most recently reported
// manifest (FR49) -- what the board's ConfigApplier successfully
// instantiated from its last applied config, never an independent survey
// of its I2C bus (see api.proto's doc comment). Household-scoped like
// GetDeviceConfig: "doesn't exist" and "exists, out of scope" collapse to
// the same refusal (NFR2).
func (s *LeafLabAPIServer) GetReportedInventory(ctx context.Context, req *pb.GetReportedInventoryRequest) (*pb.GetReportedInventoryResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("get_reported_inventory", "device_id", reason)
	}
	if err := s.authorizeBoardAccess(ctx, req.DeviceId); err != nil {
		return nil, err
	}

	found, rows, reportedAt, err := s.repo.GetReportedInventory(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("get reported inventory failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("get_reported_inventory", "", "Could not look up this device's reported inventory right now. Please try again.")
	}
	if !found {
		// FR49: "no report exists yet" is distinct from "empty inventory" --
		// entries is always empty on the wire either way, so Found is the
		// one thing a caller can key off to tell them apart.
		return &pb.GetReportedInventoryResponse{Found: false}, nil
	}

	entries := make([]*pb.ReportedInventoryEntry, len(rows))
	for i, row := range rows {
		entries[i] = reportedInventoryRowToProto(row)
	}
	return &pb.GetReportedInventoryResponse{
		Found:      true,
		Entries:    entries,
		ReportedAt: contract.ToInstant(reportedAt),
	}, nil
}

func reportedInventoryRowToProto(row ReportedInventoryRow) *pb.ReportedInventoryEntry {
	return &pb.ReportedInventoryEntry{
		MuxPath:    muxPathToProto(row.Key.MuxPath),
		I2CAddress: i2cAddressToProto(row.Key.I2CAddress),
		SensorType: sensorTypeFromDBName(row.SensorTypeName),
		Name:       row.Name,
		Unit:       row.Unit,
		ChipModel:  row.ChipModel,
	}
}

// GetConfigDrift compares deviceID's board's stored desired state (its
// latest accepted device_config) against its reported inventory
// (GetReportedInventory) and classifies every canonical hardware key
// (FR18) seen on either side (FR49). See api.proto's doc comment for the
// three classifications' meaning.
//
// Deliberately reads GetLatestAcceptedConfig directly, the same way
// GetDeviceConfig does -- never anything from leaflab/api/config's
// materialisation path, which must never see reported-inventory data at
// all (see leaflab/conformance's structural guard).
func (s *LeafLabAPIServer) GetConfigDrift(ctx context.Context, req *pb.GetConfigDriftRequest) (*pb.GetConfigDriftResponse, error) {
	if reason := validateDeviceID(req.DeviceId); reason != "" {
		return nil, contract.InvalidArgument("get_config_drift", "device_id", reason)
	}
	if err := s.authorizeBoardAccess(ctx, req.DeviceId); err != nil {
		return nil, err
	}

	desiredCfg, err := s.repo.GetLatestAcceptedConfig(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("get latest accepted config for drift failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("get_config_drift", "", "Could not process this request right now. Please try again.")
	}
	var desired []config.Entry
	if desiredCfg != nil {
		desired, err = s.resolveConfigEntries(ctx, desiredCfg.Sensors)
		if err != nil {
			return nil, err
		}
	}

	found, reported, reportedAt, err := s.repo.GetReportedInventory(ctx, req.DeviceId)
	if err != nil {
		s.logger.Error("get reported inventory for drift failed", "device_id", req.DeviceId, "error", err)
		return nil, contract.Internal("get_config_drift", "", "Could not process this request right now. Please try again.")
	}

	resp := &pb.GetConfigDriftResponse{Entries: computeConfigDrift(desired, reported)}
	if found {
		resp.ReportedAt = contract.ToInstant(reportedAt)
	}
	return resp, nil
}

// computeConfigDrift classifies every canonical hardware key (FR18) seen
// on either side of (desired, reported) -- desired's own order first
// (IN_DESIRED_NOT_REPORTED/MATCHED, in desired's stable order), then
// reported-only keys (REPORTED_NOT_IN_DESIRED, in reported's order),
// mirroring config.Diff's own "base order first, then target-only" shape.
func computeConfigDrift(desired []config.Entry, reported []ReportedInventoryRow) []*pb.DriftEntry {
	reportedByKey := make(map[string]ReportedInventoryRow, len(reported))
	for _, r := range reported {
		reportedByKey[r.Key.String()] = r
	}

	seen := make(map[string]bool, len(desired))
	var entries []*pb.DriftEntry

	for _, d := range desired {
		key := d.Key.String()
		seen[key] = true
		if _, ok := reportedByKey[key]; ok {
			entries = append(entries, driftEntry(pb.DriftClassification_DRIFT_CLASSIFICATION_MATCHED, d.Key, d.Sensor.GetSensorType()))
		} else {
			entries = append(entries, driftEntry(pb.DriftClassification_DRIFT_CLASSIFICATION_IN_DESIRED_NOT_REPORTED, d.Key, d.Sensor.GetSensorType()))
		}
	}

	for _, r := range reported {
		key := r.Key.String()
		if seen[key] {
			continue
		}
		entries = append(entries, driftEntry(pb.DriftClassification_DRIFT_CLASSIFICATION_REPORTED_NOT_IN_DESIRED, r.Key, sensorTypeFromDBName(r.SensorTypeName)))
	}

	return entries
}

func driftEntry(c pb.DriftClassification, key hwkey.Key, sensorType firmwarepb.SensorType) *pb.DriftEntry {
	return &pb.DriftEntry{
		Classification: c,
		MuxPath:        muxPathToProto(key.MuxPath),
		I2CAddress:     i2cAddressToProto(key.I2CAddress),
		SensorType:     sensorType,
	}
}

// muxPathToProto converts a canonical hwkey.MuxPath back into the wire
// []*configpb.MuxHop shape -- the inverse of config.canonicalMuxPath.
func muxPathToProto(path hwkey.MuxPath) []*configpb.MuxHop {
	if len(path) == 0 {
		return nil
	}
	hops := make([]*configpb.MuxHop, len(path))
	for i, h := range path {
		hops[i] = &configpb.MuxHop{MuxAddress: h.MuxAddress, MuxChannel: h.MuxChannel}
	}
	return hops
}

// i2cAddressToProto converts a canonical hwkey.AddressOpt back into the
// wire plain uint32 an entry's i2c_address field uses -- Absent renders as
// 0, the same as the legacy "unknown address" sentinel, matching how
// entryDiffToProto/removeFormToProto's callers already read i2c_address
// straight off a raw *configpb.SensorConfig (a proto3 scalar has no way to
// distinguish absent from 0 either).
func i2cAddressToProto(addr hwkey.AddressOpt) uint32 {
	v, _ := addr.Value()
	return uint32(v)
}
