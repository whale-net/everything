package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"google.golang.org/protobuf/encoding/protojson"
)

// Scenario is a parsed leaflab/scripts/scenarios/*.json file: the sensor
// hardware layout (decoded into the same configpb.DeviceConfig shape
// push-config.sh pushes to real boards), plus the two free-text keys used
// only for emulator logging. The emulator reads these files as-is --
// push-config.sh still owns their format.
type Scenario struct {
	Name        string // basename without extension, e.g. "mux-light-temp"
	Description string
	Hardware    string
	Config      *configpb.DeviceConfig
}

// scenarioMeta captures the two free-text keys that live alongside "sensors"
// in a scenario file but aren't part of configpb.DeviceConfig.
type scenarioMeta struct {
	Description string `json:"description"`
	Hardware    string `json:"hardware"`
}

// LoadScenario decodes one scenario JSON file into a Scenario.
func LoadScenario(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario %s: %w", path, err)
	}

	var meta scenarioMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("decode scenario %s metadata: %w", path, err)
	}

	cfg := &configpb.DeviceConfig{}
	unmarshalOpts := protojson.UnmarshalOptions{DiscardUnknown: true}
	if err := unmarshalOpts.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("decode scenario %s sensors: %w", path, err)
	}

	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	if err := validateScenarioSensors(name, cfg); err != nil {
		return nil, err
	}

	return &Scenario{
		Name:        name,
		Description: meta.Description,
		Hardware:    meta.Hardware,
		Config:      cfg,
	}, nil
}

// validateScenarioSensors enforces the load-time invariants the emulator
// requires beyond what protojson decoding alone checks: unique non-empty
// sensor names, a known chip type, and single-hop mux paths (the real
// firmware -- LB7 -- is single-hop-only, and the emulator mirrors that
// limitation rather than exceeding it).
func validateScenarioSensors(scenarioName string, cfg *configpb.DeviceConfig) error {
	seen := make(map[string]struct{}, len(cfg.GetSensors()))
	for _, s := range cfg.GetSensors() {
		if s.GetName() == "" {
			return fmt.Errorf("scenario %s: sensor has empty name", scenarioName)
		}
		if _, ok := seen[s.GetName()]; ok {
			return fmt.Errorf("scenario %s: duplicate sensor name %q", scenarioName, s.GetName())
		}
		seen[s.GetName()] = struct{}{}

		if s.GetChipType() == configpb.ChipType_CHIP_TYPE_UNKNOWN {
			return fmt.Errorf("scenario %s: sensor %q has CHIP_TYPE_UNKNOWN", scenarioName, s.GetName())
		}

		if len(s.GetMuxPath()) > 1 {
			return fmt.Errorf("scenario %s: sensor %q has a %d-hop muxPath; only single-hop mux chains are supported", scenarioName, s.GetName(), len(s.GetMuxPath()))
		}
	}
	return nil
}

// LoadScenarioDir loads every *.json file in dir, keyed by basename without
// extension (e.g. "mux-light-temp").
func LoadScenarioDir(dir string) (map[string]*Scenario, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read scenario dir %s: %w", dir, err)
	}

	scenarios := make(map[string]*Scenario)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		s, err := LoadScenario(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		scenarios[s.Name] = s
	}
	return scenarios, nil
}
