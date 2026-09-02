package server

// Pure-Go coverage for computeFingerprint (idempotency.go): stable across
// repeated calls with identical (tool, input), and distinguishing whenever
// either changes -- the property RegisterWrite relies on to tell a
// genuine replay (matching fingerprint) apart from a same-key conflict
// (differing fingerprint).
import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeFingerprint_StableForIdenticalInputDistinguishesChanges(t *testing.T) {
	channelID := uuid.New()
	in1 := writeInput{ChannelID: channelID.String(), Value: "a"}

	fp1a, err := computeFingerprint("scoped_write", in1)
	require.NoError(t, err)
	fp1b, err := computeFingerprint("scoped_write", in1)
	require.NoError(t, err)
	assert.Equal(t, fp1a, fp1b, "the same tool + identical input must produce the same fingerprint every time")

	in2 := writeInput{ChannelID: channelID.String(), Value: "b"}
	fp2, err := computeFingerprint("scoped_write", in2)
	require.NoError(t, err)
	assert.NotEqual(t, fp1a, fp2, "different arguments must produce a different fingerprint")

	fp3, err := computeFingerprint("other_tool", in1)
	require.NoError(t, err)
	assert.NotEqual(t, fp1a, fp3, "a different tool name must produce a different fingerprint even for identical arguments")
}
