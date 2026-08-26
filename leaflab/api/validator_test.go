package main

import (
	"testing"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// TestValidatorI2CAddressRange tests that out-of-range I2C addresses are rejected.
func TestValidatorI2CAddressRange(t *testing.T) {
	// TODO: Implement test.
}

// TestValidatorChipSensorTypePair tests that invalid chip/sensor-type pairs are rejected.
func TestValidatorChipSensorTypePair(t *testing.T) {
	// TODO: Implement test.
}

// TestValidatorPollInterval tests that invalid poll_interval_ms values are rejected.
func TestValidatorPollInterval(t *testing.T) {
	// TODO: Implement test.
}

// TestValidatorHardwareKeyCollision tests that duplicate hardware keys are detected.
func TestValidatorHardwareKeyCollision(t *testing.T) {
	// TODO: Implement test.
}

// TestValidatorRemovalKeyNotFound tests that removal keys not in the base are errors.
func TestValidatorRemovalKeyNotFound(t *testing.T) {
	// TODO: Implement test.
}

// TestValidatorRemovalEntryNoAddress tests that removing an entry with i2c_address=0 is rejected.
func TestValidatorRemovalEntryNoAddress(t *testing.T) {
	// TODO: Implement test.
}

// TestValidatorKeySwapNotCaught tests that a swap is not caught here (FR16.4 handles it).
func TestValidatorKeySwapNotCaught(t *testing.T) {
	// TODO: Implement test.
}

// TestValidatorEditEffectivePayload tests that validation runs on the effective payload after materialisation.
func TestValidatorEditEffectivePayload(t *testing.T) {
	// TODO: Implement test.
}
