package server

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestWhagentEnvFromEnv(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		grantSet bool
		wantErr  bool
		enabled  bool
	}{
		{"unset is disabled", nil, false, false, false},
		{"unset with grant is disabled", nil, true, false, false},
		{"whagent without grant", map[string]string{EnvWhagentJWKSURL: "u", EnvWhagentIssuer: "i", "MCP_PUBLIC_URL": "p"}, false, true, false},
		{"whagent without public url", map[string]string{EnvWhagentJWKSURL: "u", EnvWhagentIssuer: "i"}, true, true, false},
		{"only one var", map[string]string{EnvWhagentIssuer: "i", "MCP_PUBLIC_URL": "p"}, true, true, false},
		{"fully configured", map[string]string{EnvWhagentJWKSURL: "u", EnvWhagentIssuer: "i", "MCP_PUBLIC_URL": "p"}, true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := WhagentEnvFromEnv(env(c.env), c.grantSet)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got.Enabled() != c.enabled {
				t.Fatalf("enabled = %v, want %v", got.Enabled(), c.enabled)
			}
		})
	}
}
