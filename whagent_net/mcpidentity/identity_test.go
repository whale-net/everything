// Pure-Go unit coverage for Encode/Decode's round trip (issue #2245's
// Testing section, NFR7) -- no database, no HTTP, so this runs as part of
// `bazel test //...`.
package mcpidentity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEncodeDecode_RoundTrips covers the two shapes the issue's Testing
// section calls out explicitly: an issuer URL containing "/" and ":" (a
// real Keycloak realm issuer, e.g.
// "https://keycloak.example.com/realms/whagent") and a sub that is a UUID
// (Keycloak's actual subject shape).
func TestEncodeDecode_RoundTrips(t *testing.T) {
	cases := []struct {
		name string
		iss  string
		sub  string
	}{
		{
			name: "issuer with scheme, host, and path segments; UUID sub",
			iss:  "https://keycloak.example.com/realms/whagent",
			sub:  "3fa85f64-5717-4562-b3fc-2c963f66afa6",
		},
		{
			name: "issuer with a port",
			iss:  "http://localhost:8081/realms/whagent",
			sub:  "another-sub-value",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := Encode(tc.iss, tc.sub)
			require.NoError(t, err)
			require.NotEmpty(t, encoded)

			gotIss, gotSub, err := Decode(encoded)
			require.NoError(t, err)
			assert.Equal(t, tc.iss, gotIss)
			assert.Equal(t, tc.sub, gotSub)
		})
	}
}

func TestEncode_EmptyIss_Fails(t *testing.T) {
	_, err := Encode("", "some-sub")
	assert.Error(t, err)
}

func TestEncode_EmptySub_Fails(t *testing.T) {
	_, err := Encode("https://keycloak.example.com/realms/whagent", "")
	assert.Error(t, err)
}

func TestEncode_IssContainingSeparator_Fails(t *testing.T) {
	_, err := Encode("https://keycloak.example.com/realms/wh|agent", "some-sub")
	assert.Error(t, err)
}

func TestEncode_SubContainingSeparator_Fails(t *testing.T) {
	_, err := Encode("https://keycloak.example.com/realms/whagent", "sub|with|pipes")
	assert.Error(t, err)
}

// TestDecode_MalformedInput_FailsLoudly covers Decode's "reject anything
// ambiguous rather than guessing" contract: no separator at all, more than
// one separator, and an empty half either side of a lone separator must
// all be errors, never a best-effort guess at which half is which.
//
// Red/green discipline (verified by hand, then reverted): changing
// Decode's strings.Split(encoded, separator) to
// strings.SplitN(encoded, separator, 2) (identity.go) -- which greedily
// treats everything after the first separator as sub instead of rejecting
// a second one -- made this test's "more than one separator" case fail
// (an error was expected but Decode returned nil). Reverting the change
// restored it to green.
func TestDecode_MalformedInput_FailsLoudly(t *testing.T) {
	cases := []struct {
		name    string
		encoded string
	}{
		{name: "no separator at all", encoded: "no-separator-here"},
		{name: "empty string", encoded: ""},
		{name: "more than one separator", encoded: "https://a|b|extra-sub"},
		{name: "empty iss half", encoded: "|some-sub"},
		{name: "empty sub half", encoded: "https://keycloak.example.com/realms/whagent|"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iss, sub, err := Decode(tc.encoded)
			require.Error(t, err)
			assert.Empty(t, iss)
			assert.Empty(t, sub)
		})
	}
}
