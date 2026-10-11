package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

func TestEffectiveIdleTimeoutPrecedence(t *testing.T) {
	platform := 2 * time.Hour
	override := int32(90)
	defSecs := int32(3600)
	def := session.AgentDefinition{IdleTimeoutSeconds: &defSecs}

	t.Run("override wins over definition and platform", func(t *testing.T) {
		got := effectiveIdleTimeout(&session.Session{IdleTimeoutOverride: &override}, def, platform)
		assert.Equal(t, 90*time.Second, got)
	})
	t.Run("definition wins over platform when no override", func(t *testing.T) {
		got := effectiveIdleTimeout(&session.Session{}, def, platform)
		assert.Equal(t, time.Hour, got)
	})
	t.Run("platform default when neither is set", func(t *testing.T) {
		got := effectiveIdleTimeout(&session.Session{}, session.AgentDefinition{}, platform)
		assert.Equal(t, platform, got)
	})
}

func TestActivitiesPlatformIdleTimeoutFallsBackToSessionDefault(t *testing.T) {
	assert.Equal(t, session.DefaultIdleTimeout, (&Activities{}).platformIdleTimeout())
	assert.Equal(t, time.Minute, (&Activities{DefaultIdleTimeout: time.Minute}).platformIdleTimeout())
}

func TestParseDefaultIdleTimeout(t *testing.T) {
	t.Run("unset is the session default", func(t *testing.T) {
		d, err := parseDefaultIdleTimeout("")
		require.NoError(t, err)
		assert.Equal(t, session.DefaultIdleTimeout, d)
	})
	t.Run("valid duration parses", func(t *testing.T) {
		d, err := parseDefaultIdleTimeout("90m")
		require.NoError(t, err)
		assert.Equal(t, 90*time.Minute, d)
	})
	for _, bad := range []string{"banana", "0s", "-5m", "1.5s"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			_, err := parseDefaultIdleTimeout(bad)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "WHAGENT_DEFAULT_IDLE_TIMEOUT")
		})
	}
}
