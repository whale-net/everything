package main

import "time"

// ClaimConfig holds the A28 constants governing the FR76 self-service board
// claim possession challenge. All fields are configuration, not code
// constants (see leaflab/api/ENV.md) — r >= 2 is the hard requirement, the
// specific values are a product tradeoff between claim usability and
// enumeration resistance (discussion 1131, round-2 architect confirmation).
//
// These values never change what is disclosed to the caller: initiation,
// round acknowledgements and terminal states are uniform regardless of the
// configured numbers (FR76.1, FR76.3, NFR2).
type ClaimConfig struct {
	// RoundsRequired is r: the number of distinct challenger-marked restarts
	// required to discharge a challenge (FR76.3). Must be >= 2.
	RoundsRequired int
	// RoundBound is the time window after marking a round (t0) within which
	// the restart signal must land (FR76.3).
	RoundBound time.Duration
	// Lifetime is the total time budget for a challenge — long enough to
	// walk to the greenhouse and back (FR76.9).
	Lifetime time.Duration
	// AttemptsPerRound bounds how many restart attempts are allowed per
	// round before the challenge is exhausted (FR76.6).
	AttemptsPerRound int
	// Cooldown is applied to a (principal, device_id) pair after a
	// challenge terminates without discharge (FR76.6).
	Cooldown time.Duration
}

// DefaultClaimConfig returns the round-2-reviewed A28 defaults.
func DefaultClaimConfig() ClaimConfig {
	return ClaimConfig{
		RoundsRequired:   2,
		RoundBound:       3 * time.Minute,
		Lifetime:         15 * time.Minute,
		AttemptsPerRound: 2,
		Cooldown:         15 * time.Minute,
	}
}

// LoadClaimConfig reads A28 constants from environment variables, falling
// back to DefaultClaimConfig for any unset or invalid value.
func LoadClaimConfig(getEnvInt func(key string, def int) int) ClaimConfig {
	def := DefaultClaimConfig()
	return ClaimConfig{
		RoundsRequired:   getEnvInt("LEAFLAB_CLAIM_ROUNDS_REQUIRED", def.RoundsRequired),
		RoundBound:       time.Duration(getEnvInt("LEAFLAB_CLAIM_ROUND_BOUND_SECONDS", int(def.RoundBound.Seconds()))) * time.Second,
		Lifetime:         time.Duration(getEnvInt("LEAFLAB_CLAIM_LIFETIME_SECONDS", int(def.Lifetime.Seconds()))) * time.Second,
		AttemptsPerRound: getEnvInt("LEAFLAB_CLAIM_ATTEMPTS_PER_ROUND", def.AttemptsPerRound),
		Cooldown:         time.Duration(getEnvInt("LEAFLAB_CLAIM_COOLDOWN_SECONDS", int(def.Cooldown.Seconds()))) * time.Second,
	}
}
