// Command bridge runs on a host that has the `pi` CLI installed. It spawns
// one `pi --mode rpc` subprocess per session and exposes it over a small
// HTTP+SSE API so the pi_console UI can drive it remotely.
//
// The pi RPC protocol is a stdin/stdout JSONL affair scoped to a single
// process (see https://pi.dev/docs/latest/rpc) — this bridge is what turns
// that into something reachable over the network for a multi-host UI.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func authMiddleware(token string, next http.Handler) http.Handler {
	if token == "" {
		log.Printf("WARNING: PI_CONSOLE_BRIDGE_TOKEN is not set; this bridge grants unauthenticated agent/shell access to anyone who can reach it")
		return next
	}
	want := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	piBin := getenv("PI_CONSOLE_PI_BIN", "pi")
	port := getenv("PORT", "8787")
	token := os.Getenv("PI_CONSOLE_BRIDGE_TOKEN")

	var extraArgs []string
	if raw := os.Getenv("PI_CONSOLE_PI_ARGS"); raw != "" {
		extraArgs = strings.Fields(raw)
	}

	mgr := newManager(piBin, extraArgs)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		sess, err := mgr.create(req.Provider, req.Model)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to start pi: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"id": sess.id})
	})

	mux.HandleFunc("POST /v1/sessions/{id}/prompt", func(w http.ResponseWriter, r *http.Request) {
		sess := mgr.get(r.PathValue("id"))
		if sess == nil {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Message) == "" {
			http.Error(w, "message is required", http.StatusBadRequest)
			return
		}
		if err := sess.send(map[string]string{"type": "prompt", "message": req.Message}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("POST /v1/sessions/{id}/abort", func(w http.ResponseWriter, r *http.Request) {
		sess := mgr.get(r.PathValue("id"))
		if sess == nil {
			http.NotFound(w, r)
			return
		}
		if err := sess.send(map[string]string{"type": "abort"}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("DELETE /v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		mgr.delete(r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /v1/sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		sess := mgr.get(r.PathValue("id"))
		if sess == nil {
			http.NotFound(w, r)
			return
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch, hist := sess.subscribe()
		defer sess.unsubscribe(ch)

		for _, line := range hist {
			fmt.Fprintf(w, "data: %s\n\n", line)
		}
		fl.Flush()

		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case line, ok := <-ch:
				if !ok {
					fmt.Fprint(w, ": session ended\n\n")
					fl.Flush()
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", line)
				fl.Flush()
			case <-ticker.C:
				fmt.Fprint(w, ": keepalive\n\n")
				fl.Flush()
			}
		}
	})

	handler := authMiddleware(token, mux)
	log.Printf("pi_console bridge listening on :%s (pi binary: %s)", port, piBin)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
