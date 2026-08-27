package ratelimit

import (
	"testing"
	"time"
)

// emptyEnv stands in for a process with no LEAFLAB_API_RATELIMIT_* variables
// set: every bucket falls back to DefaultConfigs.
func emptyEnv(string) string { return "" }

func TestLoadConfigFromEnv_DefaultsWhenUnset(t *testing.T) {
	configs, err := LoadConfigFromEnv(emptyEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, bucket := range Buckets {
		got, ok := configs[bucket]
		if !ok {
			t.Errorf("bucket %q missing from loaded configs", bucket)
			continue
		}
		want := DefaultConfigs[bucket]
		if got != want {
			t.Errorf("bucket %q = %+v, want default %+v", bucket, got, want)
		}
	}
}

func TestLoadConfigFromEnv_OverridesFromEnv(t *testing.T) {
	limitVar, windowVar := EnvVarNames(BucketReadDefault)
	env := map[string]string{
		limitVar:  "7",
		windowVar: "42",
	}
	getenv := func(key string) string { return env[key] }

	configs, err := LoadConfigFromEnv(getenv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := configs[BucketReadDefault]
	want := WindowConfig{Limit: 7, Window: 42 * time.Second}
	if got != want {
		t.Errorf("BucketReadDefault = %+v, want %+v", got, want)
	}

	// A bucket with no override still falls back to its default.
	if got := configs[BucketResend]; got != DefaultConfigs[BucketResend] {
		t.Errorf("BucketResend = %+v, want default %+v (no override supplied)", got, DefaultConfigs[BucketResend])
	}
}

func TestLoadConfigFromEnv_MalformedLimit_FailsLoudly(t *testing.T) {
	limitVar, _ := EnvVarNames(BucketReadDefault)
	getenv := func(key string) string {
		if key == limitVar {
			return "not-a-number"
		}
		return ""
	}

	if _, err := LoadConfigFromEnv(getenv); err == nil {
		t.Fatal("LoadConfigFromEnv with a malformed limit returned nil error, want a boot-time failure")
	}
}

func TestLoadConfigFromEnv_MalformedWindow_FailsLoudly(t *testing.T) {
	_, windowVar := EnvVarNames(BucketClaimOpen)
	getenv := func(key string) string {
		if key == windowVar {
			return "also-not-a-number"
		}
		return ""
	}

	if _, err := LoadConfigFromEnv(getenv); err == nil {
		t.Fatal("LoadConfigFromEnv with a malformed window returned nil error, want a boot-time failure")
	}
}
