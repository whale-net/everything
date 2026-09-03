package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/grpcauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// This file is the permanent regression test built directly from #1668's
// root-cause investigation (see the doc comment above
// isDeploymentActionTimeout in handlers_deployment_actions.go for the full
// writeup). fakeDeploymentAPIClient in handlers_deployment_actions_test.go
// stubs the RPC entirely in memory -- it can prove boundDeploymentRPC's
// own select/time.After race works, but it cannot prove anything about
// whether a *real* gRPC call over a *real* socket, dialed through the
// *exact* production dial chain (grpcclient.NewClient +
// grpcauth.NewUserTokenDialOption as a grpc.WithPerRPCCredentials
// DialOption, exactly matching manmanv2/ui/main.go's NewControlClient
// call), actually gets cancelled the way the code assumes. #1664 already
// had unit tests passing against the fake client and still didn't survive
// #1667's live Tilt re-validation -- this test exists so that gap can't
// repeat silently under `bazel test` again.
//
// realTransportHungAPI implements manmanpb.ManManAPIServer with
// StopSession/StartSession handlers that ignore their own ctx entirely and
// block far longer than any bound under test -- simulating a downstream
// (host manager) that never answers and an API handler that doesn't even
// check its own context, which is the actual production shape #1667
// reported (the API's own gRPC interceptor logged the full unbounded
// downstream duration, not an early ctx-cancellation-triggered return).
type realTransportHungAPI struct {
	manmanpb.UnimplementedManManAPIServer
}

func (h *realTransportHungAPI) StopSession(ctx context.Context, req *manmanpb.StopSessionRequest) (*manmanpb.StopSessionResponse, error) {
	time.Sleep(5 * time.Second)
	return &manmanpb.StopSessionResponse{Session: &manmanpb.Session{SessionId: req.SessionId}}, nil
}

func (h *realTransportHungAPI) StartSession(ctx context.Context, req *manmanpb.StartSessionRequest) (*manmanpb.StartSessionResponse, error) {
	time.Sleep(5 * time.Second)
	return &manmanpb.StartSessionResponse{Session: &manmanpb.Session{SessionId: 1, ServerGameConfigId: req.ServerGameConfigId}}, nil
}

// newRealTransportTestApp starts a real gRPC server on a loopback TCP
// socket serving realTransportHungAPI, dials it through the exact
// production ControlClient/grpcclient/grpcauth wiring, and returns an App
// wired to the resulting real *grpc.ClientConn plus a cleanup func. Callers
// must defer the cleanup to stop the server and close the connection.
func newRealTransportTestApp(t *testing.T) *App {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	manmanpb.RegisterManManAPIServer(grpcServer, &realTransportHungAPI{})
	go func() {
		_ = grpcServer.Serve(lis)
	}()
	t.Cleanup(grpcServer.Stop)

	// Mirrors main.go's NewApp -> NewControlClient(ctx, config.ControlAPIURL,
	// userAuthOpt) exactly: the same PerRPCCredentials DialOption feeding
	// the same grpcclient.NewClient constructor.
	userAuthOpt := grpcauth.NewUserTokenDialOption(grpcauth.AuthModeNone)
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	controlClient, err := NewControlClient(dialCtx, lis.Addr().String(), userAuthOpt)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = controlClient.Close() })

	return &App{grpc: controlClient, deploymentActionTimeout: 500 * time.Millisecond}
}

// TestRealTransport_Stop_HungBackend_ReturnsWithinBound is #1667's actual
// repro condition reproduced over a real socket: a genuinely unreachable
// (here: deliberately slow and ctx-ignoring) backend must not be allowed
// to hold the handler open for anywhere near its own 5s sleep -- the
// bounded action timeout (500ms in this test) must still govern how long
// doDeploymentAction takes to return.
//
// This directly exercises the mechanism the #1668 investigation doc
// comment describes: even though a standalone reproduction proved
// context.WithTimeout alone already bounds this exact real-transport call
// correctly (see that comment), boundDeploymentRPC's independent
// time.After race means this test does not depend on that assumption
// continuing to hold in every future environment.
func TestRealTransport_Stop_HungBackend_ReturnsWithinBound(t *testing.T) {
	// Calls boundDeploymentRPC + ControlClient.StopSession directly with a
	// hardcoded session id, exactly as stopDeployment does with a resolved
	// live session -- ListSessions isn't stubbed by realTransportHungAPI
	// (it falls through to UnimplementedManManAPIServer, which errors), so
	// this routes around getLiveSession to isolate the bound itself.
	app := newRealTransportTestApp(t)

	start := time.Now()
	_, err := boundDeploymentRPC(context.Background(), app.deploymentActionBound(), func(c context.Context) (*manmanpb.Session, error) {
		return app.grpc.StopSession(c, 777)
	})
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("StopSession call returned after %s, want well under the backend's 5s hang (bound is %s)", elapsed, app.deploymentActionBound())
	}
	if err == nil {
		t.Fatalf("expected a bound-firing error, got nil (elapsed %s)", elapsed)
	}
	if !isDeploymentActionTimeout(err) {
		t.Errorf("expected isDeploymentActionTimeout(err) to be true, got err=%v", err)
	}
}

// TestRealTransport_Start_HungBackend_ReturnsWithinBound is
// TestRealTransport_Stop_HungBackend_ReturnsWithinBound's Start
// counterpart, covering #1668's extension of the bound to Start.
func TestRealTransport_Start_HungBackend_ReturnsWithinBound(t *testing.T) {
	app := newRealTransportTestApp(t)

	start := time.Now()
	_, err := boundDeploymentRPC(context.Background(), app.deploymentActionBound(), func(c context.Context) (*manmanpb.Session, error) {
		return app.grpc.StartSession(c, 42, false)
	})
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("StartSession call returned after %s, want well under the backend's 5s hang (bound is %s)", elapsed, app.deploymentActionBound())
	}
	if err == nil {
		t.Fatalf("expected a bound-firing error, got nil (elapsed %s)", elapsed)
	}
	if !isDeploymentActionTimeout(err) {
		t.Errorf("expected isDeploymentActionTimeout(err) to be true, got err=%v", err)
	}
}

// TestRealTransport_DeadlineExceeded_MatchesGRPCWrappedError guards the
// isDeploymentActionTimeout bug this investigation found directly: a real
// gRPC client-side-cancelled call surfaces as a *status.Error, which plain
// errors.Is(err, context.DeadlineExceeded) cannot see through.
func TestRealTransport_DeadlineExceeded_MatchesGRPCWrappedError(t *testing.T) {
	app := newRealTransportTestApp(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := app.grpc.StopSession(ctx, 777)

	if err == nil {
		t.Fatalf("expected an error from a call whose context already expired, got nil")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Skip("this Go/grpc-go version's errors.Is now sees through gRPC-wrapped deadline errors -- isDeploymentActionTimeout's extra status.Code check is now redundant defense-in-depth rather than a fix for an active bug, which is fine")
	}
	if !isDeploymentActionTimeout(err) {
		t.Errorf("isDeploymentActionTimeout(err) = false for a real gRPC-wrapped context deadline error (err=%v); errors.Is alone was proven insufficient by this investigation -- status.Code(err) == codes.DeadlineExceeded must also be checked", err)
	}
}
