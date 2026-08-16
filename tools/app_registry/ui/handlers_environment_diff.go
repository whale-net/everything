package main

import (
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// defaultDiffPair picks the FR-3 default environment pair when neither
// ?from= nor ?to= is set: the two highest-rank environments (ListEnvironments
// is rank-ordered ascending), matching the wireframe's "stage vs prod"
// example -- the pair closest to production is the one an operator most
// often wants to compare. Returns ("", "") when fewer than two environments
// exist; the handler renders an explicit "need at least two environments"
// state in that case rather than guessing.
func defaultDiffPair(environments []*pb.Environment) (from, to string) {
	if len(environments) < 2 {
		return "", ""
	}
	return environments[len(environments)-2].GetKey(), environments[len(environments)-1].GetKey()
}

// handleEnvironmentDiff is screen 12-environment-diff (FR-20): every entity
// present in either environment, diffed at an optional "as of" instant
// (FR-7), with the selected pair and instant carried in the URL (FR-3) so a
// pasted link reproduces exactly. See environment_diff_data.go's
// buildEnvironmentDiff.
func (app *App) handleEnvironmentDiff(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	at, err := parseAsOf(r.URL.Query().Get("at"))
	if err != nil {
		http.Error(w, "invalid 'at' parameter: "+err.Error(), http.StatusBadRequest)
		return
	}

	envResp, err := app.registry.Environment.ListEnvironments(r.Context(), &pb.ListEnvironmentsRequest{})
	if err != nil {
		log.Printf("ListEnvironments failed: %v", err)
		http.Error(w, "Failed to load environments from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}
	environments := envResp.GetEnvironments()

	fromKey := strings.TrimSpace(r.URL.Query().Get("from"))
	toKey := strings.TrimSpace(r.URL.Query().Get("to"))

	// FR-3: land on a concrete pair, reflected in the URL itself, rather
	// than silently rendering a default the address bar doesn't show --
	// redirect once so a pasted/bookmarked link always reproduces exactly.
	if fromKey == "" && toKey == "" {
		if df, dt := defaultDiffPair(environments); df != "" {
			v := url.Values{}
			v.Set("from", df)
			v.Set("to", dt)
			if raw := r.URL.Query().Get("at"); raw != "" {
				v.Set("at", raw)
			}
			http.Redirect(w, r, "/environments/diff?"+v.Encode(), http.StatusSeeOther)
			return
		}
	}

	var fromEnv, toEnv *pb.Environment
	for _, e := range environments {
		if e.GetKey() == fromKey {
			fromEnv = e
		}
		if e.GetKey() == toKey {
			toEnv = e
		}
	}

	if fromEnv != nil && toEnv != nil {
		diff, err := app.buildEnvironmentDiff(r.Context(), fromEnv, toEnv, at)
		if err != nil {
			log.Printf("buildEnvironmentDiff(%q, %q) failed: %v", fromKey, toKey, err)
			http.Error(w, "Failed to load environment diff from app-registry-api: "+err.Error(), http.StatusBadGateway)
			return
		}
		if err := RenderTempl(w, r, "Diff: "+fromKey+" vs "+toKey, pages.EnvironmentDiff(user, environments, fromKey, toKey, at, diff)); err != nil {
			log.Printf("Failed to render environment diff page: %v", err)
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}

	// Either an unknown from/to key, or fewer than two environments exist
	// (defaultDiffPair returned ""). Render the picker with no table rather
	// than error -- an operator with zero/one environment configured, or a
	// stale/typo'd query param, should still see a usable screen.
	if err := RenderTempl(w, r, "Diff", pages.EnvironmentDiff(user, environments, fromKey, toKey, at, nil)); err != nil {
		log.Printf("Failed to render environment diff page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
