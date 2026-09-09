package grpcauth

import "testing"

// TestIsServiceAccountUsername proves the sole classification rule
// (KEYCLOAK.md § "Service accounts"): a preferred_username is a service
// account iff it starts with the fixed "service-account-" prefix Keycloak
// itself assigns -- never a substring match, never case-insensitive, since
// neither is how Keycloak actually names these users.
func TestIsServiceAccountUsername(t *testing.T) {
	tests := []struct {
		name              string
		preferredUsername string
		want              bool
	}{
		{
			name:              "service account, single-word client id",
			preferredUsername: "service-account-app-registry-builder",
			want:              true,
		},
		{
			name:              "service account, whagent-net's own FR6 client",
			preferredUsername: "service-account-whagent-scheduler",
			want:              true,
		},
		{
			name:              "human, email-shaped username",
			preferredUsername: "alice@example.com",
			want:              false,
		},
		{
			name:              "human, plain sAMAccountName-shaped username",
			preferredUsername: "alice",
			want:              false,
		},
		{
			name:              "empty preferred_username (e.g. a claim set that omits it)",
			preferredUsername: "",
			want:              false,
		},
		{
			name: "human username that merely contains the prefix, not as a leading match -- " +
				"must not be misclassified: HasPrefix, never Contains",
			preferredUsername: "not-service-account-alice",
			want:              false,
		},
		{
			name:              "human username differing from the prefix only by case must not match -- Keycloak's own naming is exact-case",
			preferredUsername: "SERVICE-ACCOUNT-app-registry-builder",
			want:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isServiceAccountUsername(tt.preferredUsername)
			if got != tt.want {
				t.Errorf("isServiceAccountUsername(%q) = %v, want %v", tt.preferredUsername, got, tt.want)
			}
		})
	}
}

// TestClassifyKeycloakClaims_RepresentativeClaimSets proves
// classifyKeycloakClaims over whole representative Keycloak token claim
// sets -- both the ClientID (azp-preferred, client_id-fallback) and
// IsServiceAccount derivations together, the shape oidcVerifier.Verify
// actually calls it with. Includes a human token that must not be
// misclassified as a service account (this task's Testing section) even
// though it carries a ClientID exactly as a service-account token does --
// proving ClientID presence alone is never what IsServiceAccount branches
// on.
func TestClassifyKeycloakClaims_RepresentativeClaimSets(t *testing.T) {
	tests := []struct {
		name          string
		raw           keycloakTokenClaims
		wantClientID  string
		wantIsService bool
	}{
		{
			name: "human interactive login: azp set, preferred_username is the human's own name",
			raw: keycloakTokenClaims{
				Azp:               "whagent-net-ui",
				PreferredUsername: "alice",
			},
			wantClientID:  "whagent-net-ui",
			wantIsService: false,
		},
		{
			name: "service account via client_credentials: azp set, preferred_username is Keycloak's fixed service-account-<client> name",
			raw: keycloakTokenClaims{
				Azp:               "whagent-scheduler",
				PreferredUsername: "service-account-whagent-scheduler",
			},
			wantClientID:  "whagent-scheduler",
			wantIsService: true,
		},
		{
			name: "azp absent, client_id present (a non-Keycloak-shaped issuer): client_id is the fallback",
			raw: keycloakTokenClaims{
				ClientID:          "some-client",
				PreferredUsername: "service-account-some-client",
			},
			wantClientID:  "some-client",
			wantIsService: true,
		},
		{
			name: "azp preferred over client_id when both are present",
			raw: keycloakTokenClaims{
				Azp:               "azp-client",
				ClientID:          "client-id-client",
				PreferredUsername: "alice",
			},
			wantClientID:  "azp-client",
			wantIsService: false,
		},
		{
			name: "human token that carries a ClientID -- must not be misclassified as a service account just because ClientID is set",
			raw: keycloakTokenClaims{
				Azp:               "whagent-net-ui",
				PreferredUsername: "alice@example.com",
			},
			wantClientID:  "whagent-net-ui",
			wantIsService: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClientID, gotIsService := classifyKeycloakClaims(tt.raw)
			if gotClientID != tt.wantClientID {
				t.Errorf("clientID = %q, want %q", gotClientID, tt.wantClientID)
			}
			if gotIsService != tt.wantIsService {
				t.Errorf("isServiceAccount = %v, want %v", gotIsService, tt.wantIsService)
			}
		})
	}
}
