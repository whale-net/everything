package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

type fakeWorkshop struct {
	WorkshopAPI
	mu         sync.Mutex
	installs   map[int64]*manmanpb.WorkshopInstallation
	nextID     int64
	downloads  int
	forced     int
	removed    int
	resets     int
	listAddons int
}

func newFakeWorkshop() *fakeWorkshop {
	return &fakeWorkshop{installs: map[int64]*manmanpb.WorkshopInstallation{}, nextID: 100}
}

func (f *fakeWorkshop) ListAddons(_ context.Context, in *manmanpb.ListAddonsRequest, _ ...grpc.CallOption) (*manmanpb.ListAddonsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listAddons++
	return &manmanpb.ListAddonsResponse{Addons: []*manmanpb.WorkshopAddon{{AddonId: 5, GameId: 2, WorkshopId: "w1", Name: "mod"}}}, nil
}
func (f *fakeWorkshop) ListInstallations(_ context.Context, in *manmanpb.ListInstallationsRequest, _ ...grpc.CallOption) (*manmanpb.ListInstallationsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &manmanpb.ListInstallationsResponse{}
	for _, i := range f.installs {
		if i.SgcId == in.SgcId && (in.AddonId == 0 || i.AddonId == in.AddonId) {
			out.Installations = append(out.Installations, i)
		}
	}
	return out, nil
}
func (f *fakeWorkshop) GetInstallation(_ context.Context, in *manmanpb.GetInstallationRequest, _ ...grpc.CallOption) (*manmanpb.GetInstallationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.installs[in.InstallationId]
	if !ok {
		return nil, status.Error(codes.NotFound, "nope")
	}
	return &manmanpb.GetInstallationResponse{Installation: i}, nil
}
func (f *fakeWorkshop) InstallAddon(_ context.Context, in *manmanpb.InstallAddonRequest, _ ...grpc.CallOption) (*manmanpb.InstallAddonResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, i := range f.installs {
		if i.SgcId == in.SgcId && i.AddonId == in.AddonId {
			if in.ForceReinstall {
				f.forced++
			}
			return &manmanpb.InstallAddonResponse{Installation: i}, nil
		}
	}
	f.nextID++
	i := &manmanpb.WorkshopInstallation{InstallationId: f.nextID, SgcId: in.SgcId, AddonId: in.AddonId, Status: "pending"}
	f.installs[i.InstallationId] = i
	f.downloads++
	return &manmanpb.InstallAddonResponse{Installation: i}, nil
}
func (f *fakeWorkshop) RemoveInstallation(_ context.Context, in *manmanpb.RemoveInstallationRequest, _ ...grpc.CallOption) (*manmanpb.RemoveInstallationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.installs, in.InstallationId)
	f.removed++
	return &manmanpb.RemoveInstallationResponse{}, nil
}
func (f *fakeWorkshop) ResetInstallation(_ context.Context, in *manmanpb.ResetInstallationRequest, _ ...grpc.CallOption) (*manmanpb.ResetInstallationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resets++
	return &manmanpb.ResetInstallationResponse{Installation: f.installs[in.InstallationId]}, nil
}

func workshopSession(t *testing.T, api WorkshopAPI) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "t"}, nil)
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(ContextWithCaller(ctx, &Caller{Issuer: "iss", Subject: "alice", Token: "tok"}), method, req)
		}
	})
	AddWorkshopTools(srv, api, &Gate{Store: &MemoryConfirmationStore{}})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callWS(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return resultText(res), res.IsError
}

func tokenFrom(t *testing.T, txt string) string {
	t.Helper()
	// resultText is JSON-encoded content; the inner payload is escaped.
	var content []struct{ Text string }
	if err := json.Unmarshal([]byte(txt), &content); err != nil {
		t.Fatal(err)
	}
	var o Outcome
	if err := json.Unmarshal([]byte(content[0].Text), &o); err != nil || o.ConfirmationToken == "" || o.Applied {
		t.Fatalf("expected preview with token, got %s", txt)
	}
	return o.ConfirmationToken
}

func TestWorkshopReads(t *testing.T) {
	f := newFakeWorkshop()
	cs := workshopSession(t, f)
	txt, isErr := callWS(t, cs, "list_addons", nil)
	if isErr || !strings.Contains(txt, "mod") {
		t.Fatalf("list_addons: %s", txt)
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_installations", Arguments: map[string]any{"deployment_id": 7}}); err != nil {
		t.Fatal(err)
	}
}

func TestInstallNewThenAlreadyInstalled(t *testing.T) {
	f := newFakeWorkshop()
	cs := workshopSession(t, f)
	a := map[string]any{"deployment_id": 7, "addon_id": 5}
	txt, isErr := callWS(t, cs, "install_addon", a)
	if isErr || !strings.Contains(txt, "pending") || f.downloads != 1 {
		t.Fatalf("new install: %s downloads=%d", txt, f.downloads)
	}
	txt2, isErr := callWS(t, cs, "install_addon", a)
	if isErr || f.downloads != 1 || f.forced != 0 || f.removed != 0 || f.resets != 0 {
		t.Fatalf("second install must not download again: %s downloads=%d", txt2, f.downloads)
	}
	if txt != txt2 {
		t.Fatalf("existing installation should be returned: %s vs %s", txt, txt2)
	}
}

func TestReinstallGated(t *testing.T) {
	f := newFakeWorkshop()
	cs := workshopSession(t, f)
	a := map[string]any{"deployment_id": 7, "addon_id": 5}
	callWS(t, cs, "install_addon", a)
	ra := map[string]any{"deployment_id": 7, "addon_id": 5, "reinstall": true}
	txt, _ := callWS(t, cs, "install_addon", ra)
	tok := tokenFrom(t, txt)
	if f.forced != 0 {
		t.Fatal("preview discarded content")
	}
	ra["confirmation_token"] = tok
	txt, isErr := callWS(t, cs, "install_addon", ra)
	if isErr || f.forced != 1 {
		t.Fatalf("confirmed reinstall: %s forced=%d", txt, f.forced)
	}
}

func TestRemoveAndResetGated(t *testing.T) {
	for _, tc := range []struct {
		tool string
		done func(*fakeWorkshop) int
	}{
		{"remove_installation", func(f *fakeWorkshop) int { return f.removed }},
		{"reset_installation", func(f *fakeWorkshop) int { return f.resets }},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			f := newFakeWorkshop()
			cs := workshopSession(t, f)
			callWS(t, cs, "install_addon", map[string]any{"deployment_id": 7, "addon_id": 5})
			a := map[string]any{"installation_id": 101}
			txt, _ := callWS(t, cs, tc.tool, a)
			tok := tokenFrom(t, txt)
			if tc.done(f) != 0 {
				t.Fatal("applied without confirmation")
			}
			a["confirmation_token"] = tok
			if txt, isErr := callWS(t, cs, tc.tool, a); isErr || tc.done(f) != 1 {
				t.Fatalf("confirm: %s", txt)
			}
			// Token is single-use.
			if _, isErr := callWS(t, cs, tc.tool, a); !isErr || tc.done(f) != 1 {
				t.Fatal("token reuse must be rejected")
			}
		})
	}
}
