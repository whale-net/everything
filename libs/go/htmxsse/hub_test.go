package htmxsse

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewHub(t *testing.T) {
	config := Config{
		SubscriberBufferDepth: 10,
	}
	attachFunc := func(context.Context) (Transport, error) {
		return nil, nil
	}
	h := NewHub(attachFunc, config)
	require.NotNil(t, h)
}
