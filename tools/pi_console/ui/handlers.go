package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (a *App) hostByName(name string) (Host, bool) {
	for _, h := range a.hosts {
		if h.Name == name {
			return h, true
		}
	}
	return Host{}, false
}

// bridgeRequest issues an authenticated request to a named host's bridge.
func (a *App) bridgeRequest(ctx context.Context, method, host, path string, body io.Reader) (*http.Response, error) {
	h, ok := a.hostByName(host)
	if !ok {
		return nil, fmt.Errorf("unknown host %q", host)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return http.DefaultClient.Do(req)
}

func (a *App) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", a.handleIndex)
	mux.HandleFunc("POST /hosts/{host}/sessions", a.handleCreateSession)
	mux.HandleFunc("GET /chat", a.handleChatPage)
	mux.HandleFunc("POST /hosts/{host}/sessions/{id}/prompt", a.handlePrompt)
	mux.HandleFunc("POST /hosts/{host}/sessions/{id}/abort", a.handleAbort)
	mux.HandleFunc("GET /hosts/{host}/sessions/{id}/events", a.handleEventsProxy)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if err := a.tmpl.ExecuteTemplate(w, "index.html", map[string]any{"Hosts": a.hosts}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *App) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if _, ok := a.hostByName(host); !ok {
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}

	resp, err := a.bridgeRequest(r.Context(), http.MethodPost, host, "/v1/sessions", strings.NewReader("{}"))
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to reach host %q: %v", host, err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		http.Error(w, fmt.Sprintf("host %q failed to start session: %s", host, body), http.StatusBadGateway)
		return
	}

	var out struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.ID == "" {
		http.Error(w, "host returned an invalid session response", http.StatusBadGateway)
		return
	}

	http.Redirect(w, r, "/chat?host="+url.QueryEscape(host)+"&id="+url.QueryEscape(out.ID), http.StatusSeeOther)
}

func (a *App) handleChatPage(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	id := r.URL.Query().Get("id")
	if _, ok := a.hostByName(host); !ok || id == "" {
		http.Error(w, "unknown host or session", http.StatusBadRequest)
		return
	}
	if err := a.tmpl.ExecuteTemplate(w, "chat.html", map[string]any{"Host": host, "ID": id}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *App) handlePrompt(w http.ResponseWriter, r *http.Request) {
	host, id := r.PathValue("host"), r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	message := strings.TrimSpace(r.FormValue("message"))
	if message == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	body, _ := json.Marshal(map[string]string{"message": message})
	resp, err := a.bridgeRequest(r.Context(), http.MethodPost, host, "/v1/sessions/"+id+"/prompt", bytes.NewReader(body))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil || resp.StatusCode >= 300 {
		fmt.Fprintf(w, `<div class="bubble system error">failed to send prompt to %s</div>`, esc(host))
		return
	}
	resp.Body.Close()

	fmt.Fprintf(w, `<div class="bubble user">%s</div>`, esc(message))
}

func (a *App) handleAbort(w http.ResponseWriter, r *http.Request) {
	host, id := r.PathValue("host"), r.PathValue("id")
	resp, err := a.bridgeRequest(r.Context(), http.MethodPost, host, "/v1/sessions/"+id+"/abort", strings.NewReader("{}"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
}

func (a *App) handleEventsProxy(w http.ResponseWriter, r *http.Request) {
	host, id := r.PathValue("host"), r.PathValue("id")
	if _, ok := a.hostByName(host); !ok {
		http.Error(w, "unknown host", http.StatusNotFound)
		return
	}

	upstream, err := a.bridgeRequest(r.Context(), http.MethodGet, host, "/v1/sessions/"+id+"/events", nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to reach host %q: %v", host, err), http.StatusBadGateway)
		return
	}
	defer upstream.Body.Close()
	if upstream.StatusCode != http.StatusOK {
		http.Error(w, "session not found on host", http.StatusBadGateway)
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

	state := newTranscriptState()
	scanner := bufio.NewScanner(upstream.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		html := renderEvent(state, payload)
		if html == "" {
			continue
		}
		writeSSE(w, "chat", html)
		fl.Flush()
	}
}
