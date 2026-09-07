package main

import (
	"fmt"

	configpb "github.com/whale-net/everything/firmware/proto/config"
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

// LoadScenario decodes one scenario JSON file into a Scenario.
//
// TODO(#1764 Implementation): decode sensors with protojson
// (DiscardUnknown, so description/hardware don't break decoding), read
// description/hardware separately via encoding/json, and reject at load
// time: duplicate sensor name, empty name, CHIP_TYPE_UNKNOWN, and
// muxPath longer than one hop.
func LoadScenario(path string) (*Scenario, error) {
	return nil, fmt.Errorf("LoadScenario(%s): not implemented", path)
}

// LoadScenarioDir loads every *.json file in dir, keyed by basename without
// extension (e.g. "mux-light-temp").
//
// TODO(#1764 Implementation): glob dir for *.json, call LoadScenario per
// file, collect into a map.
func LoadScenarioDir(dir string) (map[string]*Scenario, error) {
	return nil, fmt.Errorf("LoadScenarioDir(%s): not implemented", dir)
}
