package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseIdleTimeout_AcceptsWholeSecondDurations(t *testing.T) {
	cases := map[string]time.Duration{
		"24h":   24 * time.Hour,
		"90s":   90 * time.Second,
		"1m30s": 90 * time.Second,
		"1s":    time.Second,
	}
	for raw, want := range cases {
		got, err := ParseIdleTimeout("idle_timeout", raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
}

func TestParseIdleTimeout_RejectsInvalidNamingFieldAndValue(t *testing.T) {
	cases := []string{"", "soon", "24", "0s", "0h", "-1h", "1.5s", "500ms"}
	for _, raw := range cases {
		_, err := ParseIdleTimeout("idle_timeout_override", raw)
		require.Error(t, err, "%q must be rejected", raw)
		assert.Contains(t, err.Error(), "idle_timeout_override", "%q", raw)
		assert.Contains(t, err.Error(), raw, "%q", raw)
	}
}

func TestValidateIdleTimeout(t *testing.T) {
	assert.NoError(t, ValidateIdleTimeout("idle_timeout", time.Second))
	assert.NoError(t, ValidateIdleTimeout("idle_timeout", 24*time.Hour))

	for _, d := range []time.Duration{0, -time.Second} {
		err := ValidateIdleTimeout("idle_timeout", d)
		require.Error(t, err, d.String())
		assert.Contains(t, err.Error(), "idle_timeout")
		assert.Contains(t, err.Error(), d.String())
	}

	tooLarge := time.Duration(1<<31) * time.Second
	err := ValidateIdleTimeout("idle_timeout", tooLarge)
	require.Error(t, err, "a count of seconds past INT must be rejected")
	assert.Contains(t, err.Error(), "idle_timeout")
}

func TestEffectiveIdleTimeout_Precedence(t *testing.T) {
	platform := 24 * time.Hour
	override := int32(90)
	defSecs := int32(3600)

	cases := []struct {
		name     string
		override *int32
		defSecs  *int32
		want     time.Duration
	}{
		{"override wins over definition", &override, &defSecs, 90 * time.Second},
		{"override wins with no definition", &override, nil, 90 * time.Second},
		{"definition used when no override", nil, &defSecs, time.Hour},
		{"platform default when neither set", nil, nil, platform},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def := AgentDefinition{IdleTimeoutSeconds: tc.defSecs}
			got := EffectiveIdleTimeout(tc.override, def, platform)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.defSecs, def.IdleTimeoutSeconds, "definition must not be mutated")
		})
	}
}

func TestDefaultIdleTimeout_IsTwentyFourHours(t *testing.T) {
	assert.Equal(t, 24*time.Hour, DefaultIdleTimeout)
}

func TestFormatIdleTimeout_RoundTripsThroughParse(t *testing.T) {
	for _, secs := range []int32{1, 59, 60, 90, 3600, 3660, 86400, 90061} {
		s := FormatIdleTimeout(secs)
		d, err := ParseIdleTimeout("idle_timeout", s)
		require.NoError(t, err, s)
		assert.Equal(t, secs, IdleTimeoutSeconds(d), "%d formatted as %q", secs, s)
	}
	assert.Equal(t, "24h", FormatIdleTimeout(86400))
	assert.Equal(t, "1m30s", FormatIdleTimeout(90))
}
