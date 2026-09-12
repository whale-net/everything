// Pure-Go unit coverage for Encode/Decode's round trip -- no database, no
// HTTP, so this runs as part of `bazel test //...`.
package identity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEncodeDecode_RoundTrips covers two real shapes: an issuer URL
// containing "/" and ":" (a real Keycloak realm issuer) and a sub that is
// a UUID (Keycloak's actual subject shape).
func TestEncodeDecode_RoundTrips(t *testing.T) {
	cases := []struct {
		name string
		iss  string
		sub  string
	}{
		{
			name: "issuer with scheme, host, and path segments; UUID sub",
			iss:  "https://keycloak.example.com/realms/krill",
			sub:  "3fa85f64-5717-4562-b3fc-2c963f66afa6",
		},
		{
			name: "issuer with a port",
			iss:  "http://localhost:8081/realms/krill",
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
	_, err := Encode("https://keycloak.example.com/realms/krill", "")
	assert.Error(t, err)
}

func TestEncode_IssContainingSeparator_Fails(t *testing.T) {
	_, err := Encode("https://keycloak.example.com/realms/kr|ill", "some-sub")
	assert.Error(t, err)
}

func TestEncode_SubContainingSeparator_Fails(t *testing.T) {
	_, err := Encode("https://keycloak.example.com/realms/krill", "sub|with|pipes")
	assert.Error(t, err)
}

// TestDecode_MalformedInput_FailsLoudly covers Decode's "reject anything
// ambiguous rather than guessing" contract.
func TestDecode_MalformedInput_FailsLoudly(t *testing.T) {
	cases := []struct {
		name    string
		encoded string
	}{
		{name: "no separator at all", encoded: "no-separator-here"},
		{name: "empty string", encoded: ""},
		{name: "more than one separator", encoded: "https://a|b|extra-sub"},
		{name: "empty iss half", encoded: "|some-sub"},
		{name: "empty sub half", encoded: "https://keycloak.example.com/realms/krill|"},
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
