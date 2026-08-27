// Package claim carries FR76's self-service board claim (possession
// challenge) configuration: A28's five named constants, plus the one
// additional constant the Implementation section's restart-detection logic
// needs (RestartUptimeThreshold) that A28 does not itself name.
//
// This is a Scaffold-phase package (task #1342): only the constants and
// their env-var loader live here. The challenge/round/cooldown business
// logic (leaflab/migrate/migrations/021_claim_challenge.up.sql's tables,
// the OpenClaimChallenge/MarkClaimRound/GetClaimChallengeStatus/
// CompleteClaim RPCs in leaflab/api/proto/api.proto, and their handlers)
// is Implementation-phase work, matching households.go's scaffold/feat
// split precedent. Config is not yet wired into leaflab/api/main.go --
// that wiring happens once a handler actually needs it, same as
// ratelimit.LoadConfigFromEnv's own scaffold-to-wired history.
package claim

import (
	"fmt"
	"strconv"
	"time"
)

// Config is FR76's A28 constants, all configurable via environment
// variables (leaflab/api/ENV.md documents each one) and none hardcoded
// into request handling.
type Config struct {
	// RoundsRequired is A28's r: the number of distinct challenger-marked
	// device restarts a challenge must observe to discharge. The
	// requirement text's hard floor -- "r >= 2 is the requirement" -- is
	// enforced by LoadConfigFromEnv, not left to a caller's discipline.
	RoundsRequired int
	// RoundBound is the short bound each round's restart signal must fall
	// within, measured from the round's t0 (the instant MarkClaimRound was
	// called). A28 default: 3 minutes.
	RoundBound time.Duration
	// ChallengeLifetime is the challenge's total bounded lifetime from
	// OpenClaimChallenge (requirement 8: "long enough to walk to the
	// greenhouse and back"). A28 default: 15 minutes.
	ChallengeLifetime time.Duration
	// AttemptsPerRound bounds how many times a single round may be
	// re-marked (re-issuing t0) before the challenge is exhausted.
	// A28 default: 2 attempts per round.
	AttemptsPerRound int
	// CooldownDuration is how long a (principal, device_id) pair spends in
	// claim_cooldown after a challenge terminates as not-discharged
	// (requirement 5). A28 names "cooldown after failure" but gives no
	// explicit duration -- unlike the other four constants, this default is
	// this task's own choice, not a number carried in the requirement text.
	// Flagged here and in the PR per the issue's residual-risk caveat;
	// revisit if system-validator or a later task specifies a value.
	CooldownDuration time.Duration
	// RestartUptimeThreshold is the Implementation section's "below a
	// configured threshold, e.g. a few minutes" bound for treating an
	// uptime_s regression as a genuine restart rather than the uint32
	// millisecond-wrap-at-~49.7-days case (requirement 4). Not one of
	// A28's five named constants -- it belongs to the restart-detection
	// logic (leaflab/processor/handler.go, Implementation phase), grouped
	// here because it is configuration of the same kind (env-overridable,
	// documented in ENV.md) rather than a hardcoded value.
	RestartUptimeThreshold time.Duration
}

// DefaultConfig is Config's A28 fallback, used for any field whose
// environment variable (see the Env* constants below) is unset.
// leaflab/api/ENV.md documents these values alongside the variable names.
var DefaultConfig = Config{
	RoundsRequired:         2,
	RoundBound:             3 * time.Minute,
	ChallengeLifetime:      15 * time.Minute,
	AttemptsPerRound:       2,
	CooldownDuration:       30 * time.Minute,
	RestartUptimeThreshold: 5 * time.Minute,
}

// envVarPrefix is common to every Config field's environment variable name.
const envVarPrefix = "LEAFLAB_API_CLAIM_"

// Environment variable names for each Config field. Durations are
// configured in whole seconds, matching leaflab/api/ratelimit/env.go's
// WindowConfig convention.
const (
	EnvRoundsRequired        = envVarPrefix + "ROUNDS_REQUIRED"
	EnvRoundBoundSeconds     = envVarPrefix + "ROUND_BOUND_SECONDS"
	EnvChallengeLifetimeSecs = envVarPrefix + "CHALLENGE_LIFETIME_SECONDS"
	EnvAttemptsPerRound      = envVarPrefix + "ATTEMPTS_PER_ROUND"
	EnvCooldownSeconds       = envVarPrefix + "COOLDOWN_SECONDS"
	EnvRestartThresholdSecs  = envVarPrefix + "RESTART_UPTIME_THRESHOLD_SECONDS"
)

// ErrRoundsRequiredTooLow is returned by LoadConfigFromEnv when the
// resolved RoundsRequired is below 2 -- the requirement text's explicit
// floor ("r >= 2 is the requirement"), enforced at load time so a
// misconfigured environment fails loudly at startup (Validation section:
// "r >= 2 is enforced at startup") rather than silently accepting a weaker
// challenge.
var ErrRoundsRequiredTooLow = fmt.Errorf("%s must be >= 2 (FR76 requirement text: \"r >= 2 is the requirement\")", EnvRoundsRequired)

// LoadConfigFromEnv builds a Config from DefaultConfig, overriding any
// field whose environment variable is set. getenv is injected (rather than
// calling os.Getenv directly) so this is testable without mutating process
// environment, matching ratelimit.LoadConfigFromEnv's shape.
//
// A malformed value (not a valid integer) is an error, not a silent
// fallback -- same posture as ratelimit.LoadConfigFromEnv. RoundsRequired
// below 2, whether from the environment or (in theory) a caller-supplied
// default, is also an error: ErrRoundsRequiredTooLow.
func LoadConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := DefaultConfig

	if raw := getenv(EnvRoundsRequired); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q: %w", EnvRoundsRequired, raw, err)
		}
		cfg.RoundsRequired = v
	}
	if cfg.RoundsRequired < 2 {
		return Config{}, ErrRoundsRequiredTooLow
	}

	if raw := getenv(EnvRoundBoundSeconds); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q: %w", EnvRoundBoundSeconds, raw, err)
		}
		cfg.RoundBound = time.Duration(v) * time.Second
	}

	if raw := getenv(EnvChallengeLifetimeSecs); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q: %w", EnvChallengeLifetimeSecs, raw, err)
		}
		cfg.ChallengeLifetime = time.Duration(v) * time.Second
	}

	if raw := getenv(EnvAttemptsPerRound); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q: %w", EnvAttemptsPerRound, raw, err)
		}
		cfg.AttemptsPerRound = v
	}

	if raw := getenv(EnvCooldownSeconds); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q: %w", EnvCooldownSeconds, raw, err)
		}
		cfg.CooldownDuration = time.Duration(v) * time.Second
	}

	if raw := getenv(EnvRestartThresholdSecs); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("%s=%q: %w", EnvRestartThresholdSecs, raw, err)
		}
		cfg.RestartUptimeThreshold = time.Duration(v) * time.Second
	}

	return cfg, nil
}
