package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	firmwarepb "github.com/whale-net/everything/firmware/proto"
	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/hwkey"
	"github.com/whale-net/everything/leaflab/invalidation"
	"github.com/whale-net/everything/leaflab/metrics"
	"github.com/whale-net/everything/libs/go/rmq"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// SensorRepository is the persistence interface used by MessageHandler.
type SensorRepository interface {
	UpsertBoard(ctx context.Context, deviceID string) (int64, error)
	UpsertSensorType(ctx context.Context, name, unit string) (int64, error)
	UpsertSensor(ctx context.Context, boardID, sensorTypeID int64, name, unit string, hw *HardwareAddress) (int64, *int64, error)
	LoadBoardSensorIdentities(ctx context.Context, boardID int64) ([]BoardSensorIdentity, error)
	RewireAndRenameSensor(ctx context.Context, sensorID, sensorTypeID int64, name, unit string, hw *HardwareAddress) (*int64, error)
	UpsertSensorLabel(ctx context.Context, sensorID int64, name string) error
	UpsertSensorHWHistory(ctx context.Context, sensorID int64, hw *HardwareAddress) error
	GetSensor(ctx context.Context, deviceID, sensorName string) (SensorInfo, bool, error)
	InsertReading(ctx context.Context, sensorID int64, regionID *int64, value float64, valid bool, uptimeS uint32, recordedAt time.Time, configVersion *int64) error
	UpsertDeviceConfig(ctx context.Context, boardID int64, version int64, configJSON []byte) error
	// AckDeviceConfig is FR45's only writer of device_config's ack columns
	// (see Repository.AckDeviceConfig's doc comment for the database-level
	// guard this depends on). Returns pushed_at/acked_at so the caller
	// (handleConfigAck) can record NFR15's push-to-ack duration.
	AckDeviceConfig(ctx context.Context, boardID int64, version int64, accepted bool, reason string) (pushedAt, ackedAt time.Time, err error)
	// ApplyConfigRegions applies region_id assignments from the accepted
	// config at (boardID, version), re-validating each entry's household
	// ownership and staleness immediately before the write (FR1.2/FR1.3)
	// rather than trusting the validation PushDeviceConfig already did at
	// push time -- the whole point of the two-layer check is that time
	// passes between the two. An entry that no longer validates is
	// skipped, not failed: it is returned in the RegionApplySkip slice
	// (each carrying an audit trail per FR8, written by the
	// implementation) rather than aborting the remaining entries, and
	// remaining valid entries are still applied. Also returns every sensor
	// whose region_id actually changed (RegionChange), for FR73's
	// cross-process invalidation broadcast -- see repository.go's
	// ApplyConfigRegions.
	ApplyConfigRegions(ctx context.Context, boardID int64, version int64) ([]RegionApplySkip, []RegionChange, error)
	// CloseRemovedSensorHWHistory is FR82.6's ack-time close: for every
	// entry this accepted version's EDIT-scope push dropped
	// (device_config_removal, migration 031), close that entry's
	// currently-open sensor_hw_history interval at (effectively) this
	// call's own instant -- "the version's accepted-at time". See
	// repository.go's CloseRemovedSensorHWHistory.
	CloseRemovedSensorHWHistory(ctx context.Context, boardID int64, version int64) error
	SetSensorChipID(ctx context.Context, sensorID int64, chipModel string) error
	IsKnownChipAddress(ctx context.Context, chipModel string, i2cAddress uint32) (bool, error)
	// UpsertBoardManifestReport is FR49's write path: replace boardID's
	// board_manifest_report/board_manifest_report_entry rows (migration
	// 035) with this manifest's entries, stamping reportedAt. Best-effort
	// from handleManifest's perspective -- see its own call site.
	UpsertBoardManifestReport(ctx context.Context, boardID int64, entries []ManifestReportEntry, reportedAt time.Time) error
}

// MessageHandler decodes leaflab MQTT messages and persists them.
type MessageHandler struct {
	logger *slog.Logger
	repo   SensorRepository
	cache  *SensorCache
	// invalidationPub broadcasts an FR73 invalidation event after this
	// process's own ApplyConfigRegions commits a region change, so every
	// process's SensorCache -- including this one, via the Subscriber
	// wired in main.go -- is told, regardless of which process (API or
	// processor) wrote the assignment. See leaflab/invalidation's doc
	// comment.
	invalidationPub *invalidation.Publisher
}

func NewMessageHandler(logger *slog.Logger, repo SensorRepository, cache *SensorCache, invalidationPub *invalidation.Publisher) *MessageHandler {
	return &MessageHandler{logger: logger, repo: repo, cache: cache, invalidationPub: invalidationPub}
}

func (h *MessageHandler) Handle(ctx context.Context, msg rmq.Message) error {
	parts := strings.Split(msg.RoutingKey, ".")
	if len(parts) < 3 || parts[0] != "leaflab" {
		return &rmq.PermanentError{Err: fmt.Errorf("unexpected routing key: %s", msg.RoutingKey)}
	}

	deviceID := parts[1]

	switch {
	case len(parts) == 3 && parts[2] == "manifest":
		return h.handleManifest(ctx, deviceID, msg.Body)
	case len(parts) == 4 && parts[2] == "sensor":
		return h.handleSensorReading(ctx, deviceID, parts[3], msg.Body)
	case len(parts) == 3 && parts[2] == "config":
		return h.handleConfigPush(ctx, deviceID, msg.Body)
	case len(parts) == 4 && parts[2] == "config" && parts[3] == "ack":
		return h.handleConfigAck(ctx, deviceID, msg.Body)
	default:
		h.logger.Warn("unhandled routing key", "key", msg.RoutingKey)
		return nil
	}
}

// handleManifest upserts the board and all its sensors, then populates the cache.
func (h *MessageHandler) handleManifest(ctx context.Context, deviceID string, body []byte) error {
	var manifest firmwarepb.DeviceManifest
	if err := proto.Unmarshal(body, &manifest); err != nil {
		return &rmq.PermanentError{Err: fmt.Errorf("unmarshal DeviceManifest: %w", err)}
	}

	boardID, err := h.repo.UpsertBoard(ctx, deviceID)
	if err != nil {
		return err
	}
	h.logger.Info("board registered", "device_id", deviceID, "board_id", boardID)

	// Loaded once per manifest for FR16.3's elimination step (see
	// resolveManifestIdentities): a manifest entry that changes hardware
	// key *and* name in the same message matches neither of UpsertSensor's
	// own case-1/case-2 lookups individually, so this snapshot -- taken
	// before any of this manifest's writes -- lets handleManifest resolve
	// it by elimination instead of falling through to UpsertSensor's case
	// 3 (which would mint a second sensor row for the same physical
	// sensor). A load failure here is not fatal to the manifest: it just
	// means this manifest gets no elimination step and every entry falls
	// through to UpsertSensor's normal resolution, same as before this
	// feature existed.
	existing, err := h.repo.LoadBoardSensorIdentities(ctx, boardID)
	if err != nil {
		h.logger.Warn("failed to load board sensor identities; FR16.3 elimination skipped for this manifest", "device_id", deviceID, "err", err)
		existing = nil
	}

	// First pass: resolve sensor_type and hardware address for every entry
	// up front (needed by resolveManifestIdentities before any sensor row
	// is written), without yet deciding how each entry attaches to a
	// sensor_id.
	sensorTypeIDs := make([]int64, len(manifest.Sensors))
	hws := make([]*HardwareAddress, len(manifest.Sensors))
	names := make([]string, len(manifest.Sensors))
	typeUpsertErr := make([]error, len(manifest.Sensors))
	for i, sd := range manifest.Sensors {
		typeName := sensorTypeName(sd.Type)
		sensorTypeID, err := h.repo.UpsertSensorType(ctx, typeName, sd.Unit)
		if err != nil {
			typeUpsertErr[i] = err
			continue
		}
		sensorTypeIDs[i] = sensorTypeID

		// Build hardware address. Firmware currently sends single-hop mux via
		// scalar fields; multi-hop will be added when the firmware proto is updated.
		// sd.I2CAddress is always "present" on the wire (proto3 has no way to
		// distinguish absent from 0 for a plain scalar) -- 0 is the legacy
		// manifests' "unknown address" sentinel (FR18.2, hwkey.AddressOpt), so a
		// sensor reporting it gets no HardwareAddress at all, same as before.
		addr := hwkey.Address(uint16(sd.I2CAddress))
		if !addr.IsUnknownSentinel() {
			hw := &HardwareAddress{I2CAddress: addr}
			if sd.MuxAddress > 0 {
				hw.MuxPath = hwkey.MuxPath{{MuxAddress: sd.MuxAddress, MuxChannel: sd.MuxChannel}}
			}
			hws[i] = hw
		}
		names[i] = sd.Name
	}

	eliminated := resolveManifestIdentities(existing, sensorTypeIDs, hws, names)

	// FR49: capture this manifest's reported inventory as its own fact
	// (board_manifest_report/board_manifest_report_entry, migration 035),
	// independent of the per-entry sensor bookkeeping below -- an entry
	// this loop later fails to upsert as a sensor row is still something
	// the board reported. Only entries whose sensor_type never resolved
	// (typeUpsertErr[i] != nil) are excluded, matching
	// board_manifest_report_entry's NOT NULL sensor_type_id. Logged, not
	// fatal: no other write on this path depends on this table (FR49's own
	// "a report, never a source" -- see leaflab/api/config's doc comment).
	reportEntries := make([]ManifestReportEntry, 0, len(manifest.Sensors))
	for i, sd := range manifest.Sensors {
		if typeUpsertErr[i] != nil {
			continue
		}
		reportEntries = append(reportEntries, ManifestReportEntry{
			HW:           hws[i],
			SensorTypeID: sensorTypeIDs[i],
			Name:         sd.Name,
			Unit:         sd.Unit,
			ChipModel:    sd.ChipModel,
		})
	}
	if err := h.repo.UpsertBoardManifestReport(ctx, boardID, reportEntries, time.Now()); err != nil {
		h.logger.Warn("failed to upsert board manifest report", "device_id", deviceID, "err", err)
	}

	var firstErr error
	for i, sd := range manifest.Sensors {
		typeName := sensorTypeName(sd.Type)

		if err := typeUpsertErr[i]; err != nil {
			h.logger.Error("failed to upsert sensor_type", "name", typeName, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sensorTypeID := sensorTypeIDs[i]
		hw := hws[i]

		var sensorID int64
		var regionID *int64
		if eliminatedID, ok := eliminated[i]; ok {
			// FR16.3: this entry's address and name both changed in the
			// same message, so it was resolved by elimination rather than
			// by UpsertSensor's own case-1/case-2 lookups -- update the
			// already-identified row in place instead of letting
			// UpsertSensor fall through to case 3 and mint a new one.
			sensorID = eliminatedID
			regionID, err = h.repo.RewireAndRenameSensor(ctx, eliminatedID, sensorTypeID, sd.Name, sd.Unit, hw)
		} else {
			sensorID, regionID, err = h.repo.UpsertSensor(ctx, boardID, sensorTypeID, sd.Name, sd.Unit, hw)
		}
		if err != nil {
			h.logger.Error("failed to upsert sensor", "name", sd.Name, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		if err := h.repo.UpsertSensorLabel(ctx, sensorID, sd.Name); err != nil {
			h.logger.Warn("failed to upsert sensor label", "name", sd.Name, "err", err)
		}

		if err := h.repo.UpsertSensorHWHistory(ctx, sensorID, hw); err != nil {
			h.logger.Error("failed to upsert sensor hw history", "name", sd.Name, "err", err)
		}

		if err := h.repo.SetSensorChipID(ctx, sensorID, sd.ChipModel); err != nil {
			h.logger.Warn("failed to set sensor_chip_id", "name", sd.Name, "chip_model", sd.ChipModel, "err", err)
		}

		if sd.ChipModel != "" && sd.I2CAddress > 0 {
			if ok, err := h.repo.IsKnownChipAddress(ctx, sd.ChipModel, sd.I2CAddress); err != nil {
				h.logger.Warn("chip address check failed", "name", sd.Name, "err", err)
			} else if !ok {
				h.logger.Warn("sensor reports unrecognised address for chip — possible misconfiguration",
					"name", sd.Name,
					"chip_model", sd.ChipModel,
					"i2c_address", fmt.Sprintf("0x%02x", sd.I2CAddress),
				)
			}
		}

		h.cache.Set(deviceID, sd.Name, SensorInfo{SensorID: sensorID, RegionID: regionID})
		h.logger.Info("sensor registered",
			"device_id", deviceID,
			"sensor", sd.Name,
			"type", typeName,
			"unit", sd.Unit,
			"sensor_id", sensorID,
			"region_id", regionID,
			"i2c_address", sd.I2CAddress,
			"mux_address", sd.MuxAddress,
			"mux_channel", sd.MuxChannel,
			"chip_model", sd.ChipModel,
		)
	}

	return firstErr
}

// handleSensorReading writes a reading row.
func (h *MessageHandler) handleSensorReading(ctx context.Context, deviceID, sensorName string, body []byte) error {
	info, ok := h.cache.Get(deviceID, sensorName)
	if !ok {
		var err error
		info, ok, err = h.repo.GetSensor(ctx, deviceID, sensorName)
		if err != nil {
			return fmt.Errorf("cache miss DB lookup for %s/%s: %w", deviceID, sensorName, err)
		}
		if !ok {
			h.logger.Warn("reading dropped — sensor not in DB yet, manifest not received",
				"device_id", deviceID,
				"sensor", sensorName,
			)
			return nil
		}
		h.cache.Set(deviceID, sensorName, info)
	}

	var reading firmwarepb.SensorReading
	if err := proto.Unmarshal(body, &reading); err != nil {
		return &rmq.PermanentError{Err: fmt.Errorf("unmarshal SensorReading: %w", err)}
	}

	var configVersion *int64
	if v, ok := h.cache.GetConfigVersion(deviceID); ok {
		configVersion = &v
	}

	if err := h.repo.InsertReading(
		ctx,
		info.SensorID,
		info.RegionID,
		float64(reading.Value),
		true,
		reading.UptimeMs/1000,
		time.Now(),
		configVersion,
	); err != nil {
		return err
	}

	var cvLog any = "none"
	if configVersion != nil {
		cvLog = *configVersion
	}
	h.logger.Debug("reading written",
		"device_id", deviceID,
		"sensor", sensorName,
		"value", reading.Value,
		"uptime_s", reading.UptimeMs/1000,
		"config_version", cvLog,
	)
	return nil
}

// handleConfigPush records a DeviceConfig push observed on the broker.
func (h *MessageHandler) handleConfigPush(ctx context.Context, deviceID string, body []byte) error {
	var cfg configpb.DeviceConfig
	if err := proto.Unmarshal(body, &cfg); err != nil {
		return &rmq.PermanentError{Err: fmt.Errorf("unmarshal DeviceConfig: %w", err)}
	}

	if cfg.Version > 1<<63-1 {
		return &rmq.PermanentError{Err: fmt.Errorf("DeviceConfig.version %d overflows int64", cfg.Version)}
	}

	configJSON, err := protojson.Marshal(&cfg)
	if err != nil {
		return &rmq.PermanentError{Err: fmt.Errorf("protojson DeviceConfig: %w", err)}
	}

	boardID, err := h.repo.UpsertBoard(ctx, deviceID)
	if err != nil {
		return err
	}
	if err := h.repo.UpsertDeviceConfig(ctx, boardID, int64(cfg.Version), configJSON); err != nil {
		return err
	}
	h.logger.Info("device_config recorded", "device_id", deviceID, "version", cfg.Version)
	return nil
}

// handleConfigAck records the device's ack for a config push, then
// broadcasts an invalidation.KindAck event (FR45/FR47/NFR15) so every API
// replica's own leaflab/api/ackwait.Registry -- observing this same event
// through its own Subscriber, see leaflab/invalidation's doc comment --
// resolves any AwaitConfigAck wait registered for this (device_id,
// version), regardless of accept or reject. On acceptance, also applies
// region assignments and updates the config version cache.
func (h *MessageHandler) handleConfigAck(ctx context.Context, deviceID string, body []byte) error {
	var ack configpb.DeviceConfigAck
	if err := proto.Unmarshal(body, &ack); err != nil {
		return &rmq.PermanentError{Err: fmt.Errorf("unmarshal DeviceConfigAck: %w", err)}
	}
	if ack.AppliedVersion > 1<<63-1 {
		return &rmq.PermanentError{Err: fmt.Errorf("DeviceConfigAck.applied_version %d overflows int64", ack.AppliedVersion)}
	}
	boardID, err := h.repo.UpsertBoard(ctx, deviceID)
	if err != nil {
		return err
	}
	pushedAt, ackedAt, err := h.repo.AckDeviceConfig(ctx, boardID, int64(ack.AppliedVersion), ack.Accepted, ack.Reason)
	if err != nil {
		return err
	}
	// NFR15: push-to-ack duration, per board (device_id attribute) and in
	// aggregate (the same histogram's no-filter view) -- see
	// leaflab/metrics.RecordPushToAck's doc comment.
	metrics.RecordPushToAck(ctx, deviceID, ackedAt.Sub(pushedAt))

	// FR47/NFR15: publish only after AckDeviceConfig's transaction has
	// committed (it has, by the time AckDeviceConfig returns -- see
	// Repository.AckDeviceConfig's tx.Commit) -- the same publish-after-commit
	// rule FR73's RegionChange publish below follows. A publish failure is
	// non-fatal: the ack already committed and is observable via
	// GetConfigStatus/AwaitConfigAck's own read of device_config either way;
	// a dropped event only delays a currently-open AwaitConfigAck wait to
	// its deadline (STILL_PENDING_AT_DEADLINE, never an error) rather than
	// losing the ack outcome.
	if h.invalidationPub != nil {
		if err := h.invalidationPub.Publish(ctx, invalidation.Event{
			Kind:            invalidation.KindAck,
			DeviceID:        deviceID,
			Version:         int64(ack.AppliedVersion),
			Accepted:        ack.Accepted,
			RejectionReason: ack.Reason,
			ObservedAt:      time.Now(),
		}); err != nil {
			h.logger.Warn("failed to publish invalidation event for config ack", "device_id", deviceID, "version", ack.AppliedVersion, "err", err)
		}
	}
	if ack.Accepted {
		skips, changes, err := h.repo.ApplyConfigRegions(ctx, boardID, int64(ack.AppliedVersion))
		if err != nil {
			h.logger.Warn("failed to apply config regions", "device_id", deviceID, "version", ack.AppliedVersion, "err", err)
		}
		// Each skip already wrote its own audit.Entry (FR8) inside
		// ApplyConfigRegions -- that audit row, read back via
		// leaflab/api's GetDeviceConfig (server.go), is FR1.3's
		// caller-visible surface: this ack path has no RPC response of
		// its own to carry skips on directly. Provenance (FR82.4) is
		// Phase 4 -- until it lands, GetDeviceConfig shows every skip for
		// a board to any caller with reach to it, not just the one who
		// authored the push. Logged here too, for operator visibility.
		for _, skip := range skips {
			h.logger.Warn("region assignment skipped at apply time",
				"device_id", deviceID,
				"version", ack.AppliedVersion,
				"sensor_id", skip.SensorID,
				"reason", skip.Reason)
		}
		// FR73: publish only after each change has committed (it has, by
		// the time ApplyConfigRegions returns it -- see RegionChange's doc
		// comment) -- this is the "processor's own config apply" writer
		// FR73 requires to be covered, in addition to the API's direct
		// assignment (Phase 5). A publish failure is non-fatal: the write
		// already committed, and the bounded staleness backstop
		// self-heals a dropped event.
		for _, change := range changes {
			if h.invalidationPub == nil {
				break
			}
			if err := h.invalidationPub.Publish(ctx, invalidation.Event{
				Kind:       invalidation.KindRegion,
				DeviceID:   deviceID,
				SensorID:   change.SensorID,
				SensorName: change.SensorName,
				ObservedAt: time.Now(),
			}); err != nil {
				h.logger.Warn("failed to publish invalidation event for region change", "device_id", deviceID, "sensor_id", change.SensorID, "err", err)
			}
		}
		// FR82.6: close the sensor_hw_history interval of every entry this
		// version's EDIT-scope push dropped, at this accepted-at instant.
		// Logged, not fatal, like ApplyConfigRegions above -- the ack
		// itself already committed; this is best-effort bookkeeping on top
		// of it, not a condition for having accepted the config.
		if err := h.repo.CloseRemovedSensorHWHistory(ctx, boardID, int64(ack.AppliedVersion)); err != nil {
			h.logger.Warn("failed to close removed sensor hw history", "device_id", deviceID, "version", ack.AppliedVersion, "err", err)
		}
		h.cache.SetConfigVersion(deviceID, int64(ack.AppliedVersion))
		h.logger.Info("device_config acked", "device_id", deviceID, "version", ack.AppliedVersion)
	} else {
		h.logger.Warn("device rejected config",
			"device_id", deviceID,
			"version", ack.AppliedVersion,
			"reason", ack.Reason)
	}
	return nil
}

// sensorTypeName converts a proto SensorType to the DB name.
func sensorTypeName(t firmwarepb.SensorType) string {
	raw := t.String()
	name, _ := strings.CutPrefix(raw, "SENSOR_TYPE_")
	return strings.ToLower(name)
}
