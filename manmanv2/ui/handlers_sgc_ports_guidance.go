package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// FR13 + FR14 guidance surface for the ports editor (task #2098, plan
// #2080). Everything here is UI-side guidance only -- the API keeps
// today's save-time semantics and session-start allocation (FR12
// enforcement) remains the correctness backstop (Decision 7). All data
// comes through the public API (NFR3): GetServer's allowed_port_ranges,
// ListAllocatedPorts, and sibling SGCs' saved port_bindings via
// ListServerGameConfigs.

// buildSGCPortContext fetches the FR13/FR14 context for a server through
// the public API. Individual fetch failures degrade to empty components
// (guidance silently weaker, never an error page) -- the same posture
// handleSGCDetail takes for its auxiliary fetches.
func buildSGCPortContext(ctx context.Context, api manmanpb.ManManAPIClient, serverID, excludeSGCID int64) *pages.SGCPortContext {
	out := &pages.SGCPortContext{
		Ranges: map[string][]*manmanpb.PortRange{},
		InUse:  map[string][]int32{},
	}

	if serverResp, err := api.GetServer(ctx, &manmanpb.GetServerRequest{ServerId: serverID}); err != nil {
		log.Printf("port guidance: failed to fetch server %d ranges: %v", serverID, err)
	} else if serverResp.GetServer() != nil {
		for _, pr := range serverResp.GetServer().GetAllowedPortRanges() {
			proto := strings.ToUpper(pr.GetProtocol())
			out.Ranges[proto] = append(out.Ranges[proto], pr)
		}
	}

	if allocResp, err := api.ListAllocatedPorts(ctx, &manmanpb.ListAllocatedPortsRequest{ServerId: serverID}); err != nil {
		log.Printf("port guidance: failed to list allocated ports for server %d: %v", serverID, err)
	} else {
		for _, p := range allocResp.GetPorts() {
			proto := strings.ToUpper(p.GetProtocol())
			out.InUse[proto] = append(out.InUse[proto], p.GetPort())
		}
	}

	// Saved bindings of sibling deployments on this server. The SGC's own
	// saved bindings are deliberately excluded: editing a deployment's
	// existing bindings must not flag them as "already in use" while the
	// user works on them.
	if sgcs, err := listServerGameConfigsForServer(ctx, api, serverID); err == nil {
		for _, sgc := range sgcs {
			if sgc.GetServerGameConfigId() == excludeSGCID {
				continue
			}
			for _, pb := range sgc.GetPortBindings() {
				proto := strings.ToUpper(pb.GetProtocol())
				out.InUse[proto] = append(out.InUse[proto], pb.GetHostPort())
			}
		}
	} else {
		log.Printf("port guidance: failed to list sibling SGCs for server %d: %v", serverID, err)
	}

	return out
}

// listServerGameConfigsForServer pages through ListServerGameConfigs for
// one server. The ControlClient helper hardcodes a page size and ignores
// continuation, so the paging loop lives here (small servers today; the
// loop is correct if the set grows).
func listServerGameConfigsForServer(ctx context.Context, api manmanpb.ManManAPIClient, serverID int64) ([]*manmanpb.ServerGameConfig, error) {
	var all []*manmanpb.ServerGameConfig
	pageToken := ""
	for {
		resp, err := api.ListServerGameConfigs(ctx, &manmanpb.ListServerGameConfigsRequest{
			ServerId:  serverID,
			PageToken: pageToken,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, resp.GetConfigs()...)
		if resp.GetNextPageToken() == "" {
			return all, nil
		}
		pageToken = resp.GetNextPageToken()
	}
}

// portInRange reports whether port falls inside one of the protocol's
// configured ranges.
func portInRange(ranges []*manmanpb.PortRange, port int32) bool {
	for _, pr := range ranges {
		if port >= pr.GetStart() && port <= pr.GetEnd() {
			return true
		}
	}
	return false
}

// portWarning implements FR14's exact warning semantics for one candidate
// host port: (a) "already in use" when the port is currently allocated on
// the server OR saved in another deployment's configuration; (b)
// "outside configured range" when ranges exist for the protocol and the
// port is in none of them. With no ranges configured, only (a) applies.
// An empty string means no warning. Guidance only: callers must never
// reject a save on this basis.
func portWarning(ctx *pages.SGCPortContext, protocol string, port int32) string {
	if ctx == nil {
		return ""
	}
	proto := strings.ToUpper(protocol)
	for _, used := range ctx.InUse[proto] {
		if used == port {
			return fmt.Sprintf("Host port %d is already in use on this server.", port)
		}
	}
	if ctx.HasRanges(proto) && !portInRange(ctx.Ranges[proto], port) {
		return fmt.Sprintf("Host port %d is outside the configured allowed ranges.", port)
	}
	return ""
}

// pickRandomAvailablePort implements FR13: pick a host port within the
// protocol's configured ranges that is neither currently allocated nor
// saved in another deployment's configuration on this server. Returns an
// error when the server has no ranges for the protocol (random is a
// no-op there, SB-1.2) or when every in-range port is taken. rng is
// injected so callers (and tests) get deterministic picks.
func pickRandomAvailablePort(rng *rand.Rand, ctx *pages.SGCPortContext, protocol string) (int32, error) {
	proto := strings.ToUpper(protocol)
	if !ctx.HasRanges(proto) {
		return 0, fmt.Errorf("no allowed port ranges configured for %s", proto)
	}
	used := map[int32]bool{}
	for _, p := range ctx.InUse[proto] {
		used[p] = true
	}

	// Deterministic candidate order, then one uniform pick.
	var candidates []int32
	seen := map[int32]bool{}
	for _, pr := range ctx.Ranges[proto] {
		for p := pr.GetStart(); p <= pr.GetEnd(); p++ {
			if !used[p] && !seen[p] {
				seen[p] = true
				candidates = append(candidates, p)
			}
		}
	}
	if len(candidates) == 0 {
		return 0, fmt.Errorf("no available host port within the configured %s ranges", proto)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	return candidates[rng.Intn(len(candidates))], nil
}

// handleSGCRandomPort answers GET /sgc/{id}/random-port?protocol=X with
// {"port": N} -- one FR13 pick for the ports editor's random affordance.
// The editor calls it per binding row; failures are guidance-grade (409),
// never save-blocking.
func (app *App) handleSGCRandomPort(w http.ResponseWriter, r *http.Request, sgcIDStr string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil || sgcID <= 0 {
		http.Error(w, "Invalid SGC ID", http.StatusBadRequest)
		return
	}
	protocol := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("protocol")))
	if protocol != "TCP" && protocol != "UDP" {
		http.Error(w, "Protocol must be TCP or UDP.", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	sgcResp, err := app.grpc.GetAPI().GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{ServerGameConfigId: sgcID})
	if err != nil || sgcResp.GetConfig() == nil {
		http.Error(w, "SGC not found", http.StatusNotFound)
		return
	}
	serverID := sgcResp.GetConfig().GetServerId()

	portCtx := buildSGCPortContext(ctx, app.grpc.GetAPI(), serverID, sgcID)
	port, err := pickRandomAvailablePort(rand.New(rand.NewSource(rand.Int63())), portCtx, protocol)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int32{"port": port})
}
