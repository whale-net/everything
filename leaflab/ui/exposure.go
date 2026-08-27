package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/whale-net/everything/leaflab/ui/components"
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
// principal key is confirmed at Implementation: it is the one field a
// human operator can read off an IdP admin screen without dereferencing a
// subject UUID, and it is the field this BFF's own session already carries
// (see htmxauth.UserInfo) with no extra plumbing needed to reach it from
// RequireExposureFunc below.
func exposureAllows(user *htmxauth.UserInfo, allowlist map[string]struct{}) bool {
	if user == nil {
		return false
	}
	_, allowed := allowlist[user.Email]
	return allowed
}

// RequireExposureFunc wraps a protected handler with A30's gate: an
// authenticated user not on allowlist sees the styled
// components.ExposureRefusalPage instead of the wrapped handler's content,
// with a 403 status matching the API side's PermissionDenied class (FR59).
// Rendered through RenderTempl/htmxbase's shared layout -- never a JSON
// body, never a blank page (FR59.2).
//
// Registered in main.go's setupRoutes immediately after RequireAuthFunc and
// before WithAccessToken: the gate only needs the user already placed in
// context by RequireAuthFunc, and running before WithAccessToken skips that
// call's access-token fetch (and its own possible redirect-to-login) for a
// request this gate is about to refuse anyway.
func RequireExposureFunc(allowlist map[string]struct{}, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := htmxauth.GetUser(r.Context())
		if !exposureAllows(user, allowlist) {
			w.WriteHeader(http.StatusForbidden)
			if err := RenderTempl(w, r, "LeafLab", components.ExposureRefusalPage(user)); err != nil {
				log.Printf("ERROR: failed to render exposure refusal page: %v", err)
			}
			return
		}
		next(w, r)
	}
}
