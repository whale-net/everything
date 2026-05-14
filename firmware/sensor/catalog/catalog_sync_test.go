package catalog_test

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/firmware/sensor/catalog"
	configpb "github.com/whale-net/everything/firmware/proto/config"
)

// TestCatalogSyncWithProtoEnum verifies that chips.yaml and the ChipType proto
// enum never drift: every chip in the YAML must have a corresponding
// CHIP_TYPE_<NAME> enum value, and every non-UNKNOWN proto value must appear
// in the YAML.
func TestCatalogSyncWithProtoEnum(t *testing.T) {
	chips, err := catalog.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Build a set of YAML chip names (upper-cased to match enum naming).
	yamlNames := make(map[string]bool, len(chips))
	for _, c := range chips {
		yamlNames[strings.ToUpper(c.Name)] = true
	}

	// Every YAML chip must have a proto enum value CHIP_TYPE_<NAME>.
	for _, c := range chips {
		key := "CHIP_TYPE_" + strings.ToUpper(c.Name)
		if _, ok := configpb.ChipType_value[key]; !ok {
			t.Errorf("chips.yaml chip %q has no proto enum value %q — add it to config.proto ChipType", c.Name, key)
		}
	}

	// Every non-UNKNOWN proto value must have a matching YAML chip.
	for name, val := range configpb.ChipType_value {
		if val == int32(configpb.ChipType_CHIP_TYPE_UNKNOWN) {
			continue
		}
		chipName := strings.TrimPrefix(name, "CHIP_TYPE_")
		if !yamlNames[chipName] {
			t.Errorf("proto enum %q has no matching chip in chips.yaml — add it or remove the enum value", name)
		}
	}
}
