package claim

import (
	"testing"
	"time"
)

// emptyEnv stands in for a process with no LEAFLAB_API_CLAIM_* variables
// set: every field falls back to DefaultConfig.
func emptyEnv(string) string { return "" }

func TestLoadConfigFromEnv_DefaultsWhenUnset(t *testing.T) {
	cfg, err := LoadConfigFromEnv(emptyEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != DefaultConfig {
		t.Errorf("LoadConfigFromEnv with no env vars set = %+v, want DefaultConfig %+v", cfg, DefaultConfig)
	}
}

func TestLoadConfigFromEnv_OverridesFromEnv(t *testing.T) {
	env := map[string]string{
		EnvRoundsRequired:        "3",
		EnvRoundBoundSeconds:     "120",
		EnvChallengeLifetimeSecs: "600",
		EnvAttemptsPerRound:      "5",
		EnvCooldownSeconds:       "900",
		EnvRestartThresholdSecs:  "60",
		EnvMaxConcurrentOpen:     "7",
	}
	getenv := func(key string) string { return env[key] }

	cfg, err := LoadConfigFromEnv(getenv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Config{
		RoundsRequired:              3,
		RoundBound:                  120 * time.Second,
		ChallengeLifetime:           600 * time.Second,
		AttemptsPerRound:            5,
		CooldownDuration:            900 * time.Second,
		RestartUptimeThreshold:      60 * time.Second,
		MaxConcurrentOpenChallenges: 7,
	}
	if cfg != want {
		t.Errorf("LoadConfigFromEnv with every var overridden = %+v, want %+v", cfg, want)
	}
}

// TestLoadConfigFromEnv_RoundsRequiredBelowTwo_Rejected is the Validation
// section's "r >= 2 is enforced at startup" requirement: the requirement
// text's hard floor must fail loudly, not silently clamp or accept a weaker
// challenge.
func TestLoadConfigFromEnv_RoundsRequiredBelowTwo_Rejected(t *testing.T) {
	for _, badValue := range []string{"0", "1", "-1"} {
		env := map[string]string{EnvRoundsRequired: badValue}
		getenv := func(key string) string { return env[key] }

		_, err := LoadConfigFromEnv(getenv)
		if err == nil {
			t.Errorf("LoadConfigFromEnv with %s=%q returned nil error, want ErrRoundsRequiredTooLow", EnvRoundsRequired, badValue)
			continue
		}
		if err != ErrRoundsRequiredTooLow {
			t.Errorf("LoadConfigFromEnv with %s=%q error = %v, want ErrRoundsRequiredTooLow", EnvRoundsRequired, badValue, err)
		}
	}
}

// TestLoadConfigFromEnv_RoundsRequiredAtTwo_Accepted proves the floor is
// inclusive: r=2 (A28's own minimum) must not be rejected.
func TestLoadConfigFromEnv_RoundsRequiredAtTwo_Accepted(t *testing.T) {
	env := map[string]string{EnvRoundsRequired: "2"}
	getenv := func(key string) string { return env[key] }

	cfg, err := LoadConfigFromEnv(getenv)
	if err != nil {
		t.Fatalf("LoadConfigFromEnv with %s=2 returned an error, want accepted: %v", EnvRoundsRequired, err)
	}
	if cfg.RoundsRequired != 2 {
		t.Errorf("RoundsRequired = %d, want 2", cfg.RoundsRequired)
	}
}

// TestLoadConfigFromEnv_MalformedValue_Rejected proves a non-integer value
// is an error, not a silent fallback to the default.
func TestLoadConfigFromEnv_MalformedValue_Rejected(t *testing.T) {
	env := map[string]string{EnvRoundBoundSeconds: "not-a-number"}
	getenv := func(key string) string { return env[key] }

	if _, err := LoadConfigFromEnv(getenv); err == nil {
		t.Errorf("LoadConfigFromEnv with %s=%q returned nil error, want a parse error", EnvRoundBoundSeconds, "not-a-number")
	}
}

func TestDefaultConfig_RoundsRequiredMeetsFloor(t *testing.T) {
	if DefaultConfig.RoundsRequired < 2 {
		t.Errorf("DefaultConfig.RoundsRequired = %d, want >= 2 (A28: \"r >= 2 is the requirement\")", DefaultConfig.RoundsRequired)
	}
}
