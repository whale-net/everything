package auth

import (
	"encoding/json"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// ResourceServerBearerOptions returns the RequireBearerTokenOptions every
// MCP resource server needs: 401 challenges point at the protected-resource
// metadata when meta is enabled. Callers set any further options
// (e.g. AllowMissingExpiration) on the result.
func ResourceServerBearerOptions(meta ProtectedResourceMetadataConfig) *sdkauth.RequireBearerTokenOptions {
	opts := &sdkauth.RequireBearerTokenOptions{}
	if meta.Enabled() {
		opts.ResourceMetadataURL = ProtectedResourceMetadataURL(meta.Resource)
	}
	return opts
}

// NewResourceServerMux builds the mux of an MCP resource server: an
// unauthenticated GET /healthz, RFC 9728 protected-resource metadata when
// meta is enabled, and each guarded handler mounted at its path. Guards
// (bearer-token middleware) are the caller's: only they are auth-specific.
func NewResourceServerMux(meta ProtectedResourceMetadataConfig, mounts map[string]http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", HealthzHandler)
	MountProtectedResourceMetadata(mux, meta)
	for path, h := range mounts {
		mux.Handle(path, h)
	}
	return mux
}

// HealthzHandler answers k8s liveness/readiness probes with {"status":"ok"}.
func HealthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
