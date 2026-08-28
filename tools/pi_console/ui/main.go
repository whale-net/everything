// Command ui serves the pi_console HTMX front end. It holds no agent state
// of its own — every session lives in a per-host bridge process (see
// ../bridge) and this server just proxies prompts and SSE event streams to
// whichever host the user picked, rendering pi RPC events into HTML.
package main

import (
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
)

// Host is one configured pi_console bridge.
type Host struct {
	Name    string
	BaseURL string
}

// parseHosts reads "name1=http://host1:8787,name2=http://host2:8787".
func parseHosts(spec string) []Host {
	var hosts []Host
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		name := strings.TrimSpace(kv[0])
		base := strings.TrimRight(strings.TrimSpace(kv[1]), "/")
		if name == "" || base == "" {
			continue
		}
		hosts = append(hosts, Host{Name: name, BaseURL: base})
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Name < hosts[j].Name })
	return hosts
}

func main() {
	hosts := parseHosts(os.Getenv("PI_CONSOLE_HOSTS"))
	if len(hosts) == 0 {
		log.Fatal(`PI_CONSOLE_HOSTS must list at least one host, e.g. PI_CONSOLE_HOSTS="dev=http://localhost:8787"`)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	app := &App{hosts: hosts, token: os.Getenv("PI_CONSOLE_BRIDGE_TOKEN")}
	if err := app.loadTemplates(); err != nil {
		log.Fatalf("failed to load templates: %v", err)
	}

	mux := http.NewServeMux()
	app.routes(mux)

	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Name
	}
	log.Printf("pi_console ui listening on :%s, hosts: %s", port, strings.Join(names, ", "))
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
