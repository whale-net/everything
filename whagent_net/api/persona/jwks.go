package persona

import (
	"encoding/json"
	"net/http"
)

// JWKSPath is the stable, fixed well-known path `api`'s JWKS endpoint is
// served at -- a domain's whagent.Verifier (constructed via
// whagent.NewVerifier) is pointed at this path appended to `api`'s
// externally reachable base URL. Fixed rather than configurable so every
// consuming domain's Verifier wiring is the same one-line JWKS URL
// (mirrors the RFC 8414/9728-style fixed well-known-path convention
// libs/go/mcpauth already uses for its own metadata endpoints).
const JWKSPath = "/.well-known/jwks.json"

// JWKSHandler serves ks's public JWKS document -- every key ks knows
// about (active and retired alike, see KeySet.JWKS), containing only
// public key material (this task's Testing section: no `d`/private JWK
// parameter ever appears).
func JWKSHandler(ks *KeySet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		set, err := ks.JWKS()
		if err != nil {
			http.Error(w, "jwks unavailable", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodHead {
			return
		}
		_ = json.NewEncoder(w).Encode(set)
	})
}

// NewMux returns the small net/http mux `api` serves its JWKS endpoint
// from, alongside its gRPC surface (this task's Scaffold section --
// mirrors manmanv2/api and audience_score_system/mcp, which each already
// run a small net/http mux alongside their primary protocol server).
func NewMux(ks *KeySet) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(JWKSPath, JWKSHandler(ks))
	return mux
}
