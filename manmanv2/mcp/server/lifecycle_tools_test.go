package server

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

type fakeLifecycleAPI struct {
	live                    *manmanpb.Session
	starts, stops, restarts int
	pending                 []*manmanpb.PendingRestartState
}

func (f *fakeLifecycleAPI) ListSessions(context.Context, *manmanpb.ListSessionsRequest, ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	if f.live == nil {
		return &manmanpb.ListSessionsResponse{}, nil
	}
	return &manmanpb.ListSessionsResponse{Sessions: []*manmanpb.Session{f.live}}, nil
}
func (f *fakeLifecycleAPI) StartSession(_ context.Context, in *manmanpb.StartSessionRequest, _ ...grpc.CallOption) (*manmanpb.StartSessionResponse, error) {
	f.starts++
	f.live = &manmanpb.Session{SessionId: 100 + int64(f.starts), ServerGameConfigId: in.ServerGameConfigId, Status: "starting"}
	return &manmanpb.StartSessionResponse{Session: f.live}, nil
}
func (f *fakeLifecycleAPI) StopSession(_ context.Context, in *manmanpb.StopSessionRequest, _ ...grpc.CallOption) (*manmanpb.StopSessionResponse, error) {
	f.stops++
	return &manmanpb.StopSessionResponse{Session: &manmanpb.Session{SessionId: in.SessionId, Status: "stopping"}}, nil
}
func (f *fakeLifecycleAPI) RestartDeployment(context.Context, *manmanpb.RestartDeploymentRequest, ...grpc.CallOption) (*manmanpb.RestartDeploymentResponse, error) {
	f.restarts++
	return &manmanpb.RestartDeploymentResponse{}, nil
}
func (f *fakeLifecycleAPI) ListPendingRestarts(context.Context, *manmanpb.ListPendingRestartsRequest, ...grpc.CallOption) (*manmanpb.ListPendingRestartsResponse, error) {
	return &manmanpb.ListPendingRestartsResponse{States: f.pending}, nil
}

type fakeAllow map[int64]bool

func (a fakeAllow) Allowed(_ context.Context, id int64) (bool, error) { return a[id], nil }

func lcCtx(p Persona) context.Context {
	return ContextWithCaller(context.Background(), &Caller{Issuer: "i", Subject: "s", Persona: p, Token: "t"})
}

func TestStartAlreadyRunningStartsNoSecond(t *testing.T) {
	api := &fakeLifecycleAPI{live: &manmanpb.Session{SessionId: 7, ServerGameConfigId: 1, Status: "running"}}
	l := &lifecycle{api: api, allp: fakeAllow{}}
	_, out, err := l.start(lcCtx(PersonaServerManager), nil, deploymentIn{DeploymentID: 1})
	if err != nil || !out.AlreadyRunning || out.SessionID != 7 || api.starts != 0 {
		t.Fatalf("out=%+v err=%v starts=%d", out, err, api.starts)
	}
}

func TestStartGamerAllowlist(t *testing.T) {
	api := &fakeLifecycleAPI{}
	l := &lifecycle{api: api, allp: fakeAllow{1: true}}
	if _, _, err := l.start(lcCtx(PersonaGamer), nil, deploymentIn{DeploymentID: 2}); !errors.Is(err, ErrNotAllowlisted) || api.starts != 0 {
		t.Fatalf("non-allowlisted: err=%v starts=%d", err, api.starts)
	}
	if _, out, err := l.start(lcCtx(PersonaGamer), nil, deploymentIn{DeploymentID: 1}); err != nil || out.SessionID == 0 || api.starts != 1 {
		t.Fatalf("allowlisted: %+v %v", out, err)
	}
}

func TestStartRevokedGrantRefused(t *testing.T) {
	// A revoked grant (valid_to set) reads as not allowed.
	api := &fakeLifecycleAPI{}
	allow := fakeAllow{1: true}
	l := &lifecycle{api: api, allp: allow}
	delete(allow, 1)
	if _, _, err := l.start(lcCtx(PersonaGamer), nil, deploymentIn{DeploymentID: 1}); !errors.Is(err, ErrNotAllowlisted) || api.starts != 0 {
		t.Fatalf("err=%v starts=%d", err, api.starts)
	}
}

func TestStartManagerBypassesAllowlist(t *testing.T) {
	for _, p := range []Persona{PersonaServerManager, PersonaAdmin} {
		api := &fakeLifecycleAPI{}
		l := &lifecycle{api: api, allp: fakeAllow{}}
		if _, _, err := l.start(lcCtx(p), nil, deploymentIn{DeploymentID: 5}); err != nil || api.starts != 1 {
			t.Fatalf("%v: err=%v starts=%d", p, err, api.starts)
		}
	}
}

func TestStopRestartUnconfirmedVsConfirmed(t *testing.T) {
	for _, name := range []string{"stop", "restart"} {
		api := &fakeLifecycleAPI{live: &manmanpb.Session{SessionId: 9, ServerGameConfigId: 3, Status: "running"}}
		l := &lifecycle{api: api}
		g := &Gate{Store: &MemoryConfirmationStore{}}
		tool := l.stopGated()
		if name == "restart" {
			tool = l.restartGated()
		}
		caller := &Caller{Issuer: "i", Subject: "s", Persona: PersonaServerManager}
		ctx := context.Background()
		out, err := g.Call(ctx, caller, nil, tool, args(`{"deployment_id":3}`))
		if err != nil || out.Applied || out.ConfirmationToken == "" || out.Preview == nil {
			t.Fatalf("%s preview: %+v %v", name, out, err)
		}
		p := out.Preview.(sessionPreview)
		if p.SessionID != 9 {
			t.Fatalf("%s preview should name session 9: %+v", name, p)
		}
		if api.stops+api.restarts != 0 {
			t.Fatalf("%s unconfirmed mutated", name)
		}
		out, err = g.Call(ctx, caller, nil, tool, withToken(`{"deployment_id":3}`, out.ConfirmationToken))
		if err != nil || !out.Applied {
			t.Fatalf("%s confirm: %+v %v", name, out, err)
		}
		if (name == "stop" && api.stops != 1) || (name == "restart" && api.restarts != 1) {
			t.Fatalf("%s not applied once: stops=%d restarts=%d", name, api.stops, api.restarts)
		}
	}
}

func TestStopWithNoSessionErrors(t *testing.T) {
	l := &lifecycle{api: &fakeLifecycleAPI{}}
	g := &Gate{Store: &MemoryConfirmationStore{}}
	caller := &Caller{Issuer: "i", Subject: "s"}
	if _, err := g.Call(context.Background(), caller, nil, l.stopGated(), args(`{"deployment_id":3}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestPendingRestartsFiltersToPending(t *testing.T) {
	api := &fakeLifecycleAPI{pending: []*manmanpb.PendingRestartState{
		{ServerGameConfigId: 1, PendingRestartId: 10, Status: "pending"},
		{ServerGameConfigId: 2, PendingRestartId: 11, Status: "resolved"},
	}}
	l := &lifecycle{api: api}
	_, out, err := l.pending(lcCtx(PersonaServerManager), nil, struct{}{})
	if err != nil || len(out.Deployments) != 1 || out.Deployments[0].DeploymentID != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}
