package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigValidate_DevAuthToken(t *testing.T) {
	require.NoError(t, config{}.validate())
	require.NoError(t, config{DevAuthToken: "x", Env: "dev"}.validate())
	require.Error(t, config{DevAuthToken: "x"}.validate())
	require.Error(t, config{DevAuthToken: "x", Env: "prod"}.validate())
}
