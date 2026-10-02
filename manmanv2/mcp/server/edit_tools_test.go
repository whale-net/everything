package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// fakeEditAPI is a stateful in-memory control API for the edit tools.
type fakeEditAPI struct {
	EditAPI
	sgc        *manmanpb.ServerGameConfig
	gc         *manmanpb.GameConfig
	patch      *manmanpb.ConfigurationPatch
	live       bool
	valid      bool
	calls      map[string]int
	updatePath []string
}

func newFakeEditAPI() *fakeEditAPI {
	return &fakeEditAPI{
		sgc: &manmanpb.ServerGameConfig{ServerGameConfigId: 7, ServerId: 1, GameConfigId: 3, Status: "active",
			PortBindings: []*manmanpb.PortBinding{{ContainerPort: 25565, HostPort: 30000, Protocol: "TCP"}}},
		gc:    &manmanpb.GameConfig{ConfigId: 3, GameId: 2, Name: "survival", Image: "mc:1", EnvTemplate: map[string]string{"MODE": "easy", "KEEP": "x"}},
		valid: true,
		calls: map[string]int{},
	}
}

func (f *fakeEditAPI) mutations() int {
	n := 0
	for _, k := range []string{"deploy", "updateSGC", "deleteSGC", "updateGC", "deleteGC", "createPatch", "updatePatch", "deletePatch", "createStrategy"} {
		n += f.calls[k]
	}
	return n
}

func (f *fakeEditAPI) ValidateDeployment(context.Context, *manmanpb.ValidateDeploymentRequest, ...grpc.CallOption) (*manmanpb.ValidateDeploymentResponse, error) {
	r := &manmanpb.ValidateDeploymentResponse{Valid: f.valid}
	if !f.valid {
		r.Issues = []*manmanpb.ValidationIssue{{Field: "port_bindings", Message: "port in use"}}
	}
	return r, nil
}
func (f *fakeEditAPI) DeployGameConfig(context.Context, *manmanpb.DeployGameConfigRequest, ...grpc.CallOption) (*manmanpb.DeployGameConfigResponse, error) {
	f.calls["deploy"]++
	return &manmanpb.DeployGameConfigResponse{Config: &manmanpb.ServerGameConfig{ServerGameConfigId: 99}}, nil
}
func (f *fakeEditAPI) GetServerGameConfig(context.Context, *manmanpb.GetServerGameConfigRequest, ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	return &manmanpb.GetServerGameConfigResponse{Config: f.sgc}, nil
}
func (f *fakeEditAPI) ListServerGameConfigs(context.Context, *manmanpb.ListServerGameConfigsRequest, ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	return &manmanpb.ListServerGameConfigsResponse{Configs: []*manmanpb.ServerGameConfig{f.sgc}}, nil
}
func (f *fakeEditAPI) UpdateServerGameConfig(_ context.Context, in *manmanpb.UpdateServerGameConfigRequest, _ ...grpc.CallOption) (*manmanpb.UpdateServerGameConfigResponse, error) {
	f.calls["updateSGC"]++
	f.sgc.PortBindings = in.PortBindings
	return &manmanpb.UpdateServerGameConfigResponse{}, nil
}
func (f *fakeEditAPI) DeleteServerGameConfig(context.Context, *manmanpb.DeleteServerGameConfigRequest, ...grpc.CallOption) (*manmanpb.DeleteServerGameConfigResponse, error) {
	f.calls["deleteSGC"]++
	return &manmanpb.DeleteServerGameConfigResponse{}, nil
}
func (f *fakeEditAPI) GetGameConfig(context.Context, *manmanpb.GetGameConfigRequest, ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return &manmanpb.GetGameConfigResponse{Config: f.gc}, nil
}
func (f *fakeEditAPI) UpdateGameConfig(_ context.Context, in *manmanpb.UpdateGameConfigRequest, _ ...grpc.CallOption) (*manmanpb.UpdateGameConfigResponse, error) {
	f.calls["updateGC"]++
	f.updatePath = in.UpdatePaths
	return &manmanpb.UpdateGameConfigResponse{}, nil
}
func (f *fakeEditAPI) DeleteGameConfig(context.Context, *manmanpb.DeleteGameConfigRequest, ...grpc.CallOption) (*manmanpb.DeleteGameConfigResponse, error) {
	f.calls["deleteGC"]++
	return &manmanpb.DeleteGameConfigResponse{}, nil
}
func (f *fakeEditAPI) ListSessions(context.Context, *manmanpb.ListSessionsRequest, ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	if f.live {
		return &manmanpb.ListSessionsResponse{Sessions: []*manmanpb.Session{{SessionId: 1}}}, nil
	}
	return &manmanpb.ListSessionsResponse{}, nil
}
func (f *fakeEditAPI) ListConfigurationStrategies(context.Context, *manmanpb.ListConfigurationStrategiesRequest, ...grpc.CallOption) (*manmanpb.ListConfigurationStrategiesResponse, error) {
	return &manmanpb.ListConfigurationStrategiesResponse{Strategies: []*manmanpb.ConfigurationStrategy{{StrategyId: 5, StrategyType: "env_vars"}}}, nil
}
func (f *fakeEditAPI) CreateConfigurationStrategy(context.Context, *manmanpb.CreateConfigurationStrategyRequest, ...grpc.CallOption) (*manmanpb.CreateConfigurationStrategyResponse, error) {
	f.calls["createStrategy"]++
	return &manmanpb.CreateConfigurationStrategyResponse{Strategy: &manmanpb.ConfigurationStrategy{StrategyId: 5}}, nil
}
func (f *fakeEditAPI) ListConfigurationPatches(context.Context, *manmanpb.ListConfigurationPatchesRequest, ...grpc.CallOption) (*manmanpb.ListConfigurationPatchesResponse, error) {
	if f.patch == nil {
		return &manmanpb.ListConfigurationPatchesResponse{}, nil
	}
	return &manmanpb.ListConfigurationPatchesResponse{Patches: []*manmanpb.ConfigurationPatch{f.patch}}, nil
}
func (f *fakeEditAPI) CreateConfigurationPatch(_ context.Context, in *manmanpb.CreateConfigurationPatchRequest, _ ...grpc.CallOption) (*manmanpb.CreateConfigurationPatchResponse, error) {
	f.calls["createPatch"]++
	f.patch = &manmanpb.ConfigurationPatch{PatchId: 11, StrategyId: in.StrategyId, PatchLevel: in.PatchLevel, EntityId: in.EntityId, PatchContent: in.PatchContent, UpdatedAt: 100}
	return &manmanpb.CreateConfigurationPatchResponse{}, nil
}
func (f *fakeEditAPI) UpdateConfigurationPatch(_ context.Context, in *manmanpb.UpdateConfigurationPatchRequest, _ ...grpc.CallOption) (*manmanpb.UpdateConfigurationPatchResponse, error) {
	f.calls["updatePatch"]++
	f.patch.PatchContent = in.PatchContent
	f.patch.UpdatedAt++
	return &manmanpb.UpdateConfigurationPatchResponse{}, nil
}
func (f *fakeEditAPI) DeleteConfigurationPatch(context.Context, *manmanpb.DeleteConfigurationPatchRequest, ...grpc.CallOption) (*manmanpb.DeleteConfigurationPatchResponse, error) {
	f.calls["deletePatch"]++
	f.patch = nil
	return &manmanpb.DeleteConfigurationPatchResponse{}, nil
}

func newEditGate() *Gate { return &Gate{Store: &MemoryConfirmationStore{}} }

// previewEdit runs the first call and returns the preview as generic JSON plus the token.
func previewEdit(t *testing.T, g *Gate, tool GatedTool, a string) (map[string]any, string) {
	t.Helper()
	out, err := g.Call(context.Background(), alice, nil, tool, args(a))
	if err != nil || out.Applied || out.ConfirmationToken == "" {
		t.Fatalf("expected preview+token, got %+v err=%v", out, err)
	}
	b, _ := json.Marshal(out.Preview)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m, out.ConfirmationToken
}

func confirmEdit(g *Gate, tool GatedTool, a, tok string) (*Outcome, error) {
	return g.Call(context.Background(), alice, nil, tool, withToken(a, tok))
}

func changesOf(t *testing.T, p map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, c := range p["changes"].([]any) {
		m := c.(map[string]any)
		out[m["field"].(string)] = m
	}
	return out
}

func TestUpdateDeploymentPreviewDiffAndRestartFlag(t *testing.T) {
	for _, live := range []bool{false, true} {
		api := newFakeEditAPI()
		api.live = live
		e := &editor{api: api}
		a := `{"deployment_id":7,"env":{"MODE":"hard","NEW":"1"},"remove_env":["KEEP"]}`
		p, _ := previewEdit(t, newEditGate(), e.updateDeploymentTool(), a)
		ch := changesOf(t, p)
		if ch["env.MODE"]["current"] != "easy" || ch["env.MODE"]["proposed"] != "hard" {
			t.Errorf("MODE diff wrong: %v", ch["env.MODE"])
		}
		if ch["env.NEW"]["current"] != nil || ch["env.NEW"]["proposed"] != "1" {
			t.Errorf("NEW diff wrong: %v", ch["env.NEW"])
		}
		if _, ok := ch["env.KEEP"]; ok {
			t.Error("removing an override that was never set must not be a change")
		}
		if p["restart_needed"] != live {
			t.Errorf("live=%v restart_needed=%v", live, p["restart_needed"])
		}
		if api.mutations() != 0 {
			t.Fatalf("preview mutated: %v", api.calls)
		}
	}
}

func TestUpdateDeploymentNoChangeNeedsNoRestart(t *testing.T) {
	api := newFakeEditAPI()
	api.live = true
	p, _ := previewEdit(t, newEditGate(), (&editor{api: api}).updateDeploymentTool(), `{"deployment_id":7,"env":{"MODE":"easy"}}`)
	if len(p["changes"].([]any)) != 0 || p["restart_needed"] != false {
		t.Errorf("no-op edit: %v", p)
	}
}

func TestUpdateDeploymentPortDiffIncludesValidation(t *testing.T) {
	api := newFakeEditAPI()
	a := `{"deployment_id":7,"port_bindings":[{"container_port":25565,"host_port":31000,"protocol":"tcp"}]}`
	p, _ := previewEdit(t, newEditGate(), (&editor{api: api}).updateDeploymentTool(), a)
	if _, ok := changesOf(t, p)["port_bindings"]; !ok {
		t.Fatalf("missing port_bindings change: %v", p)
	}
	if _, ok := p["validation"]; !ok {
		t.Error("port change preview must include validation")
	}
}

func TestUpdateDeploymentConfirmWritesPatchNotSessionLevel(t *testing.T) {
	api := newFakeEditAPI()
	e := &editor{api: api}
	g, tool := newEditGate(), e.updateDeploymentTool()
	a := `{"deployment_id":7,"env":{"MODE":"hard"}}`
	_, tok := previewEdit(t, g, tool, a)
	out, err := confirmEdit(g, tool, a, tok)
	if err != nil || !out.Applied {
		t.Fatalf("confirm: %+v %v", out, err)
	}
	if api.patch == nil || api.patch.PatchLevel != "server_game_config" || api.patch.EntityId != 7 || api.patch.PatchContent != "MODE=hard" {
		t.Fatalf("deployment-level patch not written: %+v", api.patch)
	}
	// Removing the last override deletes the patch.
	a2 := `{"deployment_id":7,"remove_env":["MODE"]}`
	_, tok = previewEdit(t, g, tool, a2)
	if _, err := confirmEdit(g, tool, a2, tok); err != nil || api.patch != nil {
		t.Fatalf("patch should be deleted: err=%v patch=%v", err, api.patch)
	}
}

func TestStalePreviewRejectedAndNothingMutated(t *testing.T) {
	api := newFakeEditAPI()
	api.patch = &manmanpb.ConfigurationPatch{PatchId: 11, StrategyId: 5, PatchContent: "MODE=hard", UpdatedAt: 100}
	e := &editor{api: api}
	g, tool := newEditGate(), e.updateDeploymentTool()
	a := `{"deployment_id":7,"env":{"X":"1"}}`
	_, tok := previewEdit(t, g, tool, a)

	api.patch.PatchContent, api.patch.UpdatedAt = "MODE=other", 200 // changed by someone else
	out, err := confirmEdit(g, tool, a, tok)
	if !errors.Is(err, ErrEntityChanged) || out != nil {
		t.Fatalf("want ErrEntityChanged, got %+v %v", out, err)
	}
	if api.mutations() != 0 {
		t.Fatalf("stale confirm mutated: %v", api.calls)
	}
	// The token is spent; a fresh preview/confirm succeeds.
	if _, err := confirmEdit(g, tool, a, tok); err == nil {
		t.Error("stale token must not be reusable")
	}
	_, tok = previewEdit(t, g, tool, a)
	if out, err := confirmEdit(g, tool, a, tok); err != nil || !out.Applied {
		t.Fatalf("re-preview confirm: %+v %v", out, err)
	}
}

func TestUpdateGameConfigStaleAndDiff(t *testing.T) {
	api := newFakeEditAPI()
	e := &editor{api: api}
	g, tool := newEditGate(), e.updateGameConfigTool()
	a := `{"config_id":3,"image":"mc:2","env_template":{"MODE":"hard","KEEP":"x"}}`
	p, tok := previewEdit(t, g, tool, a)
	ch := changesOf(t, p)
	if ch["image"]["current"] != "mc:1" || ch["image"]["proposed"] != "mc:2" || ch["env_template.MODE"]["proposed"] != "hard" {
		t.Errorf("diff wrong: %v", ch)
	}
	if _, ok := ch["env_template.KEEP"]; ok {
		t.Error("unchanged env key must not appear")
	}
	deps := p["affected_deployments"].([]any)
	if len(deps) != 1 || deps[0].(map[string]any)["deployment_id"] != float64(7) {
		t.Errorf("affected deployments: %v", deps)
	}
	if api.mutations() != 0 {
		t.Fatal("preview mutated")
	}

	api.gc.Image = "mc:other"
	if _, err := confirmEdit(g, tool, a, tok); !errors.Is(err, ErrEntityChanged) {
		t.Fatalf("want ErrEntityChanged, got %v", err)
	}
	if api.mutations() != 0 {
		t.Fatalf("stale confirm mutated: %v", api.calls)
	}
}

func TestUpdateGameConfigConfirmSendsOnlyChangedPaths(t *testing.T) {
	api := newFakeEditAPI()
	g, tool := newEditGate(), (&editor{api: api}).updateGameConfigTool()
	a := `{"config_id":3,"name":"survival","image":"mc:2"}`
	_, tok := previewEdit(t, g, tool, a)
	if _, err := confirmEdit(g, tool, a, tok); err != nil {
		t.Fatal(err)
	}
	if api.calls["updateGC"] != 1 || strings.Join(api.updatePath, ",") != "image" {
		t.Errorf("update paths = %v calls=%v", api.updatePath, api.calls)
	}
}

func TestDeployGameConfigPreviewIncludesValidationAndCreatesOnlyOnConfirm(t *testing.T) {
	api := newFakeEditAPI()
	g, tool := newEditGate(), (&editor{api: api}).deployTool()
	a := `{"server_id":1,"game_config_id":3}`
	p, tok := previewEdit(t, g, tool, a)
	if v, ok := p["validation"].(map[string]any); !ok || v["valid"] != true {
		t.Errorf("validation missing: %v", p)
	}
	if api.calls["deploy"] != 0 {
		t.Fatal("preview created a deployment")
	}
	out, err := confirmEdit(g, tool, a, tok)
	if err != nil || !out.Applied || api.calls["deploy"] != 1 {
		t.Fatalf("confirm: %+v %v %v", out, err, api.calls)
	}
}

func TestDeployInvalidConfirmRefused(t *testing.T) {
	api := newFakeEditAPI()
	api.valid = false
	g, tool := newEditGate(), (&editor{api: api}).deployTool()
	a := `{"server_id":1,"game_config_id":3}`
	_, tok := previewEdit(t, g, tool, a)
	if _, err := confirmEdit(g, tool, a, tok); err == nil || api.calls["deploy"] != 0 {
		t.Fatalf("invalid deployment must not be created: %v %v", err, api.calls)
	}
}

func TestValidateDeploymentMutatesNothing(t *testing.T) {
	api := newFakeEditAPI()
	e := &editor{api: api}
	for _, in := range []validateIn{{DeploymentID: 7}, {ServerID: 1, GameConfigID: 3}} {
		if _, out, err := e.validate(context.Background(), nil, in); err != nil || !out.Valid {
			t.Fatalf("validate %+v: %+v %v", in, out, err)
		}
	}
	if _, _, err := e.validate(context.Background(), nil, validateIn{}); err == nil {
		t.Error("empty input must be rejected")
	}
	if api.mutations() != 0 {
		t.Fatalf("validate mutated: %v", api.calls)
	}
}

func TestDeletesAreGated(t *testing.T) {
	api := newFakeEditAPI()
	api.patch = &manmanpb.ConfigurationPatch{PatchId: 11, PatchContent: "A=1\nB=2"}
	e := &editor{api: api}
	g, tool := newEditGate(), e.deleteDeploymentTool()
	a := `{"deployment_id":7}`
	p, tok := previewEdit(t, g, tool, a)
	if r := p["will_remove"].([]any); len(r) != 2 || !strings.Contains(r[1].(string), "2 variables") {
		t.Errorf("will_remove: %v", r)
	}
	if api.mutations() != 0 {
		t.Fatal("preview deleted")
	}
	// Wrong-argument confirm is rejected.
	if _, err := confirmEdit(g, tool, `{"deployment_id":8}`, tok); err == nil || api.mutations() != 0 {
		t.Fatalf("confirm with different args: %v %v", err, api.calls)
	}
	// Token was not burned by the mismatch.
	if out, err := confirmEdit(g, tool, a, tok); err != nil || !out.Applied || api.calls["deleteSGC"] != 1 || api.calls["deletePatch"] != 1 {
		t.Fatalf("confirm: %+v %v %v", out, err, api.calls)
	}
}

func TestDeleteDeploymentRefusedWithLiveSession(t *testing.T) {
	api := newFakeEditAPI()
	api.live = true
	if _, _, err := (&editor{api: api}).deleteDeploymentTool().Preview(context.Background(), args(`{"deployment_id":7}`)); err == nil {
		t.Fatal("preview must refuse")
	}
}

func TestDeleteGameConfigGatedAndBlockedByDeployments(t *testing.T) {
	api := newFakeEditAPI()
	e := &editor{api: api}
	tool := e.deleteGameConfigTool()
	if _, _, err := tool.Preview(context.Background(), args(`{"config_id":3}`)); err == nil || !strings.Contains(err.Error(), "7") {
		t.Fatalf("in-use config must be refused naming deployments: %v", err)
	}
	api.sgc.GameConfigId = 4 // no longer used
	g := newEditGate()
	a := `{"config_id":3}`
	_, tok := previewEdit(t, g, tool, a)
	if api.calls["deleteGC"] != 0 {
		t.Fatal("preview deleted")
	}
	if out, err := confirmEdit(g, tool, a, tok); err != nil || !out.Applied || api.calls["deleteGC"] != 1 {
		t.Fatalf("confirm: %+v %v", out, err)
	}
}

func TestEditToolsRegistrationAndSnapshotHooks(t *testing.T) {
	tools := EditTools(newFakeEditAPI())
	snap := map[string]bool{}
	for _, tl := range tools {
		if tl.MinPersona != PersonaServerManager {
			t.Errorf("%s persona = %v", tl.Name, tl.MinPersona)
		}
		snap[tl.Name] = tl.Snapshot != nil
	}
	for _, n := range []string{UpdateDeploymentToolName, UpdateGameConfigToolName, DeleteDeploymentToolName, DeleteGameConfigToolName} {
		if !snap[n] {
			t.Errorf("%s must capture a prior-state snapshot", n)
		}
	}
	if snap[ValidateDeploymentToolName] {
		t.Error("validate_deployment must not snapshot")
	}
}

func TestSnapshotCapturesPriorStateInAuditLog(t *testing.T) {
	api := newFakeEditAPI()
	api.patch = &manmanpb.ConfigurationPatch{PatchId: 11, PatchContent: "MODE=hard"}
	reg := NewRegistry(EditTools(api)...)
	var buf bytes.Buffer
	aud := LogAuditor{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	cc := CallContext{Context: context.Background(), Caller: alice}

	dep, _ := reg.Lookup(UpdateDeploymentToolName)
	s, err := dep.Snapshot(cc, args(`{"deployment_id":7,"env":{"MODE":"x"}}`))
	if err != nil {
		t.Fatal(err)
	}
	aud.Record(context.Background(), AuditRecord{Subject: "alice", Persona: PersonaServerManager, Tool: dep.Name, TargetID: "7", Outcome: OutcomeAllowed, Snapshot: s})
	line := buf.String()
	for _, want := range []string{`"snapshot"`, `"env_overrides"`, `"MODE":"hard"`, `"port_bindings"`, `"host_port":30000`} {
		if !strings.Contains(line, want) {
			t.Errorf("audit line missing %s: %s", want, line)
		}
	}

	gcTool, _ := reg.Lookup(UpdateGameConfigToolName)
	s, err = gcTool.Snapshot(cc, args(`{"config_id":3,"image":"mc:2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if gc, ok := s.(*manmanpb.GameConfig); !ok || gc.Image != "mc:1" || gc.EnvTemplate["MODE"] != "easy" {
		t.Errorf("game config snapshot is not the prior state: %v", s)
	}
	if api.mutations() != 0 {
		t.Fatal("snapshot mutated")
	}
}
