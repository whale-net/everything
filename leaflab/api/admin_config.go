package main

import (
	"time"

	"github.com/whale-net/everything/leaflab/api/staleness"
)

// AdminConfig holds the FR10 admin elevation constants and the A23 staleness
// evaluator configuration. All fields are configuration, not code constants
// (see leaflab/api/ENV.md) — the existence of a time-box and a threshold is
// not arbitrary (A22), but their specific values are.
type AdminConfig struct {
	// ElevationDuration is how long an EnterElevation or RenewElevation call
	// grants (FR10, A22). 60 minutes by default.
	ElevationDuration time.Duration
	// Staleness computes the A23 "not reporting" threshold, consumed by
	// FR79, FR42.2 and FR62.
	Staleness staleness.Config
}

// DefaultAdminConfig returns the A22/A23 defaults: a 60-minute elevation
// window and A23's default multiplier and floor.
func DefaultAdminConfig() AdminConfig {
	return AdminConfig{
		ElevationDuration: 60 * time.Minute,
		Staleness:         staleness.NewConfig(),
	}
}

// LoadAdminConfig reads the FR10/A23 constants from environment variables,
// falling back to DefaultAdminConfig for any unset or invalid value.
func LoadAdminConfig(getEnvInt func(key string, def int) int) AdminConfig {
	def := DefaultAdminConfig()
	return AdminConfig{
		ElevationDuration: time.Duration(getEnvInt("LEAFLAB_ELEVATION_DURATION_SECONDS", int(def.ElevationDuration.Seconds()))) * time.Second,
		Staleness: staleness.Config{
			Multiplier: getEnvInt("LEAFLAB_STALENESS_MULTIPLIER", def.Staleness.Multiplier),
			Floor:      time.Duration(getEnvInt("LEAFLAB_STALENESS_FLOOR_SECONDS", int(def.Staleness.Floor.Seconds()))) * time.Second,
		},
	}
}
