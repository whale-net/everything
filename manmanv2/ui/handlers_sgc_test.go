package main

import (
	"context"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// fakeManManAPIClient/fakeWorkshopServiceClient/newTestApp are shared test
// scaffolding used across this package's handler tests (see e.g.
// handlers_workshop_batch_status_test.go, handlers_workshop_cache_test.go).
//
// This file used to also hold handleSGCDetail's own page-rendering tests
// (the "Status & Connect" / session-history regression guards from #1530-
// #1532). handleSGCDetail retired with sgc_detail.templ (task #2279,
// FR16) -- its dispatch fallback now redirects instead of rendering
// (handlers_deployment_redirects.go, guarded by
// handlers_redirects_test.go) -- so those page-specific tests retired with
// it. The fake gRPC clients below outlive that page: other handlers still
// depend on them.
//
// fakeManManAPIClient/fakeWorkshopServiceClient embed their respective nil
// gRPC client interfaces and override only the RPCs a given test's call
// graph actually reaches (see grpc_client.go's thin ControlClient wrappers
// for which underlying client -- api vs workshop -- each helper method
// uses). Any call to an un-overridden method panics on the nil embedded
// interface, which is deliberate: it fails a test loudly rather than
// silently returning a zero value if a scenario's call graph ever grows.

type fakeManManAPIClient struct {
	manmanpb.ManManAPIClient

	sgc          *manmanpb.ServerGameConfig
	server       *manmanpb.Server
	getServerErr error
	sessions     []*manmanpb.Session
}

func (f *fakeManManAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{}, nil
}

func (f *fakeManManAPIClient) GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	return &manmanpb.GetServerGameConfigResponse{Config: f.sgc}, nil
}

func (f *fakeManManAPIClient) GetServer(ctx context.Context, in *manmanpb.GetServerRequest, opts ...grpc.CallOption) (*manmanpb.GetServerResponse, error) {
	if f.getServerErr != nil {
		return nil, f.getServerErr
	}
	return &manmanpb.GetServerResponse{Server: f.server}, nil
}

// Task #2098 wires the (now-retired) SGC detail page to ListAllocatedPorts
// and ListServerGameConfigs (FR13/FR14 guidance); these additive overrides
// keep other tests sharing this fake compiling against the nil-embedded
// interface without changing what they assert.
func (f *fakeManManAPIClient) ListAllocatedPorts(ctx context.Context, in *manmanpb.ListAllocatedPortsRequest, opts ...grpc.CallOption) (*manmanpb.ListAllocatedPortsResponse, error) {
	return &manmanpb.ListAllocatedPortsResponse{}, nil
}

func (f *fakeManManAPIClient) ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	return &manmanpb.ListServerGameConfigsResponse{}, nil
}

func (f *fakeManManAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return &manmanpb.GetGameConfigResponse{}, nil
}

func (f *fakeManManAPIClient) ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	return &manmanpb.ListSessionsResponse{Sessions: f.sessions}, nil
}

func (f *fakeManManAPIClient) ListGameConfigVolumes(ctx context.Context, in *manmanpb.ListGameConfigVolumesRequest, opts ...grpc.CallOption) (*manmanpb.ListGameConfigVolumesResponse, error) {
	return &manmanpb.ListGameConfigVolumesResponse{}, nil
}

func (f *fakeManManAPIClient) ListBackups(ctx context.Context, in *manmanpb.ListBackupsRequest, opts ...grpc.CallOption) (*manmanpb.ListBackupsResponse, error) {
	return &manmanpb.ListBackupsResponse{}, nil
}

type fakeWorkshopServiceClient struct {
	manmanpb.WorkshopServiceClient
}

func (f *fakeWorkshopServiceClient) ListInstallations(ctx context.Context, in *manmanpb.ListInstallationsRequest, opts ...grpc.CallOption) (*manmanpb.ListInstallationsResponse, error) {
	return &manmanpb.ListInstallationsResponse{}, nil
}

func (f *fakeWorkshopServiceClient) ListGameConfigLibraries(ctx context.Context, in *manmanpb.ListGameConfigLibrariesRequest, opts ...grpc.CallOption) (*manmanpb.ListGameConfigLibrariesResponse, error) {
	return &manmanpb.ListGameConfigLibrariesResponse{}, nil
}

// newTestApp builds an App wired to fake gRPC clients. Since this test
// lives in package main, it can set ControlClient's unexported api/
// workshop fields directly -- no interface refactor of ControlClient is
// needed for this white-box test.
func newTestApp(api *fakeManManAPIClient, workshop *fakeWorkshopServiceClient) *App {
	return &App{
		grpc: &ControlClient{api: api, workshop: workshop},
	}
}
