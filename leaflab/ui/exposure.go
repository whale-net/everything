package main

import (
	"net/http"
	"os"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// ExposureAllowlistEnvVar names the env var this gate reads (A30, Phase 1
// exit criterion 7): a comma-separated list of authenticated principal
// emails permitted to reach any protected route. Mirrors
// leaflab/api/exposure.go's ExposureAllowlistEnvVar -- same mechanism
// (a principal allowlist), this surface's own config value. Not a shared
// chart value in Phase 1: leaflab-api and leaflab-ui are separate
// deployables with independent env (see leaflab/ui/BUILD.bazel's release_app
// comment -- this UI is "not yet wired into any Helm chart/ingress").
//
// TODO(FR5/NFR1.b): this entire gate -- this file, its BUILD.bazel entry,
// and every call site wired to it -- is removed in #1339, the Phase 2 task
// that lands per-entity household authorization (FR4, FR5, FR1.1, NFR2)
// and makes this UI safe to expose to real users on its own scoping.
// Deleting the gate in one change is the "removable in one identifiable
// change" A30 requires.
const ExposureAllowlistEnvVar = "LEAFLAB_UI_EXPOSURE_ALLOWLIST"

// ParseExposureAllowlist mirrors leaflab/api/exposure.go's function of the
// same name -- see that file's doc comment. Duplicated rather than shared
// because leaflab/api and leaflab/ui are independent main packages/
// deployables; this pair already duplicates its own getEnv helper the same
// way (see each package's main.go).
func ParseExposureAllowlist(raw string) map[string]struct{} {
	allowlist := make(map[string]struct{})
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		allowlist[entry] = struct{}{}
	}
	return allowlist
}

// LoadExposureAllowlistFromEnv reads ExposureAllowlistEnvVar via os.Getenv
// and parses it with ParseExposureAllowlist.
func LoadExposureAllowlistFromEnv() map[string]struct{} {
	return ParseExposureAllowlist(os.Getenv(ExposureAllowlistEnvVar))
}

// exposureAllows reports whether user is on allowlist, keyed by email
// (htmxauth.UserInfo.Email). Picking email over Sub as the working
// principal key is a Scaffold-time assumption -- it is the one field a
// human operator can read off an IdP admin screen without dereferencing a
// subject UUID; Implementation confirms or revises this when it picks the
// enforcement mechanism.
func exposureAllows(user *htmxauth.UserInfo, allowlist map[string]struct{}) bool {
	if user == nil {
		return false
	}
	_, allowed := allowlist[user.Email]
	return allowed
}

// exposureRefusalHTML is the plain-sentence page a refused caller sees
// (FR59.2 parity: no technical detail, never a blank screen). Placeholder
// markup -- Implementation replaces this with a styled templ component
// matching components.DegradedPage's precedent (see handlers_auth.go).
const exposureRefusalHTML = "<!DOCTYPE html><html><body><p>This isn't open yet.</p></body></html>"

// RequireExposureFunc wraps a protected handler with A30's gate: an
// authenticated user not on allowlist sees exposureRefusalHTML instead of
// the wrapped handler's content.
//
// Not yet registered on any route in main.go's setupRoutes -- wiring
// (which routes, at what point in the RequireAuthFunc/WithAccessToken
// chain), the rendered page, and picking the enforcement mechanism are
// this task's Implementation phase.
func RequireExposureFunc(allowlist map[string]struct{}, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := htmxauth.GetUser(r.Context())
		if !exposureAllows(user, allowlist) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(exposureRefusalHTML))
			return
		}
		next(w, r)
	}
}
