package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

func (app *App) handleSGCUpdatePorts(w http.ResponseWriter, r *http.Request, sgcIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid SGC ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	portBindings, err := parsePortBindingsJSON(r.FormValue("port_bindings_json"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	_, err = app.grpc.UpdateServerGameConfig(ctx, &manmanpb.UpdateServerGameConfigRequest{
		ServerGameConfigId: sgcID,
		PortBindings:       portBindings,
		UpdatePaths:        []string{"port_bindings"},
	})
	if err != nil {
		log.Printf("Error updating port bindings for SGC %d: %v", sgcID, err)
		http.Error(w, "Failed to update port bindings", http.StatusInternalServerError)
		return
	}

	redirectURL := fmt.Sprintf("/sgc/%d", sgcID)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}
