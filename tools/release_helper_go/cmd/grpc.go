package cmd

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"google.golang.org/grpc"
)

// RegistryClients holds an active gRPC connection and typed client stubs for
// App Registry services.
type RegistryClients struct {
	conn     *grpcclient.Client
	App      pb.AppRegistryClient
	Artifact pb.ArtifactRegistryClient
}

// Close closes the underlying gRPC connection.
func (c *RegistryClients) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// newServiceAccountDialOptionFromEnv constructs the gRPC auth dial option based on
// environment variables (GRPC_AUTH_MODE, GRPC_AUTH_TOKEN_URL, GRPC_AUTH_CLIENT_ID,
// GRPC_AUTH_CLIENT_SECRET).
func newServiceAccountDialOptionFromEnv() (grpc.DialOption, error) {
	mode := grpcauth.AuthMode(envOrDefault("GRPC_AUTH_MODE", string(grpcauth.AuthModeNone)))
	authOpt, err := grpcauth.NewServiceAccountDialOption(grpcauth.ClientConfig{
		Mode:         mode,
		TokenURL:     defaultEnv("GRPC_AUTH_TOKEN_URL"),
		ClientID:     defaultEnv("GRPC_AUTH_CLIENT_ID"),
		ClientSecret: defaultEnv("GRPC_AUTH_CLIENT_SECRET"),
	})
	if err != nil {
		return nil, fmt.Errorf("app registry auth dial option: %w", err)
	}
	return authOpt, nil
}

// NewRegistryClients connects to the App Registry gRPC server using configuration
// from environment variables (APP_REGISTRY_ADDRESS, GRPC_AUTH_*).
func NewRegistryClients(ctx context.Context) (*RegistryClients, error) {
	address := defaultEnv("APP_REGISTRY_ADDRESS")
	if address == "" {
		return nil, fmt.Errorf("APP_REGISTRY_ADDRESS is not set")
	}

	authOpt, err := newServiceAccountDialOptionFromEnv()
	if err != nil {
		return nil, err
	}

	conn, err := grpcclient.NewClient(ctx, address, authOpt)
	if err != nil {
		return nil, fmt.Errorf("dial app-registry-api at %s: %w", address, err)
	}

	c := conn.GetConnection()
	return &RegistryClients{
		conn:     conn,
		App:      pb.NewAppRegistryClient(c),
		Artifact: pb.NewArtifactRegistryClient(c),
	}, nil
}

// AppRegistryClientFactory creates an AppRegistryClient and a cleanup func.
type AppRegistryClientFactory func(ctx context.Context) (pb.AppRegistryClient, func() error, error)

// ArtifactRegistryClientFactory creates an ArtifactRegistryClient and a cleanup func.
type ArtifactRegistryClientFactory func(ctx context.Context) (pb.ArtifactRegistryClient, func() error, error)

var (
	defaultAppRegistryClientFactory      AppRegistryClientFactory      = realAppRegistryClientFactory
	defaultArtifactRegistryClientFactory ArtifactRegistryClientFactory = realArtifactRegistryClientFactory
)

func realAppRegistryClientFactory(ctx context.Context) (pb.AppRegistryClient, func() error, error) {
	clients, err := NewRegistryClients(ctx)
	if err != nil {
		return nil, nil, err
	}
	return clients.App, clients.Close, nil
}

func realArtifactRegistryClientFactory(ctx context.Context) (pb.ArtifactRegistryClient, func() error, error) {
	clients, err := NewRegistryClients(ctx)
	if err != nil {
		return nil, nil, err
	}
	return clients.Artifact, clients.Close, nil
}

// NewAppRegistryClient constructs an AppRegistryClient using the active factory.
func NewAppRegistryClient(ctx context.Context) (pb.AppRegistryClient, func() error, error) {
	return defaultAppRegistryClientFactory(ctx)
}

// NewArtifactRegistryClient constructs an ArtifactRegistryClient using the active factory.
func NewArtifactRegistryClient(ctx context.Context) (pb.ArtifactRegistryClient, func() error, error) {
	return defaultArtifactRegistryClientFactory(ctx)
}

// withAppRegistryClient overrides the AppRegistryClient factory in tests.
func withAppRegistryClient(client pb.AppRegistryClient, fn func()) {
	old := defaultAppRegistryClientFactory
	defaultAppRegistryClientFactory = func(ctx context.Context) (pb.AppRegistryClient, func() error, error) {
		return client, func() error { return nil }, nil
	}
	defer func() { defaultAppRegistryClientFactory = old }()
	fn()
}

// withArtifactRegistryClient overrides the ArtifactRegistryClient factory in tests.
func withArtifactRegistryClient(client pb.ArtifactRegistryClient, fn func()) {
	old := defaultArtifactRegistryClientFactory
	defaultArtifactRegistryClientFactory = func(ctx context.Context) (pb.ArtifactRegistryClient, func() error, error) {
		return client, func() error { return nil }, nil
	}
	defer func() { defaultArtifactRegistryClientFactory = old }()
	fn()
}

// withRegistryClients overrides both App and Artifact registry client factories in tests.
func withRegistryClients(appClient pb.AppRegistryClient, artifactClient pb.ArtifactRegistryClient, fn func()) {
	withAppRegistryClient(appClient, func() {
		withArtifactRegistryClient(artifactClient, fn)
	})
}

// ── Mock / Fake Client Implementations ───────────────────────────────────────

// FakeAppRegistryClient is an in-memory test implementation of pb.AppRegistryClient.
type FakeAppRegistryClient struct {
	ReconcileAppsFn     func(ctx context.Context, in *pb.ReconcileAppsRequest, opts ...grpc.CallOption) (*pb.ReconcileAppsResponse, error)
	AssertAppsFn        func(ctx context.Context, in *pb.AssertAppsRequest, opts ...grpc.CallOption) (*pb.AssertAppsResponse, error)
	ListAppsFn          func(ctx context.Context, in *pb.ListAppsRequest, opts ...grpc.CallOption) (*pb.ListAppsResponse, error)
	GetAppFn            func(ctx context.Context, in *pb.GetAppRequest, opts ...grpc.CallOption) (*pb.GetAppResponse, error)
	ListChartsFn        func(ctx context.Context, in *pb.ListChartsRequest, opts ...grpc.CallOption) (*pb.ListChartsResponse, error)
	SetAppStatusFn      func(ctx context.Context, in *pb.SetAppStatusRequest, opts ...grpc.CallOption) (*pb.SetAppStatusResponse, error)
	ListReconcileRunsFn func(ctx context.Context, in *pb.ListReconcileRunsRequest, opts ...grpc.CallOption) (*pb.ListReconcileRunsResponse, error)

	ReconcileAppsCalls     []*pb.ReconcileAppsRequest
	AssertAppsCalls        []*pb.AssertAppsRequest
	ListAppsCalls          []*pb.ListAppsRequest
	GetAppCalls            []*pb.GetAppRequest
	ListChartsCalls        []*pb.ListChartsRequest
	SetAppStatusCalls      []*pb.SetAppStatusRequest
	ListReconcileRunsCalls []*pb.ListReconcileRunsRequest
}

// NewFakeAppRegistryClient creates a new FakeAppRegistryClient.
func NewFakeAppRegistryClient() *FakeAppRegistryClient {
	return &FakeAppRegistryClient{}
}

func (f *FakeAppRegistryClient) ReconcileApps(ctx context.Context, in *pb.ReconcileAppsRequest, opts ...grpc.CallOption) (*pb.ReconcileAppsResponse, error) {
	f.ReconcileAppsCalls = append(f.ReconcileAppsCalls, in)
	if f.ReconcileAppsFn != nil {
		return f.ReconcileAppsFn(ctx, in, opts...)
	}
	return &pb.ReconcileAppsResponse{}, nil
}

func (f *FakeAppRegistryClient) AssertApps(ctx context.Context, in *pb.AssertAppsRequest, opts ...grpc.CallOption) (*pb.AssertAppsResponse, error) {
	f.AssertAppsCalls = append(f.AssertAppsCalls, in)
	if f.AssertAppsFn != nil {
		return f.AssertAppsFn(ctx, in, opts...)
	}
	return &pb.AssertAppsResponse{}, nil
}

func (f *FakeAppRegistryClient) ListApps(ctx context.Context, in *pb.ListAppsRequest, opts ...grpc.CallOption) (*pb.ListAppsResponse, error) {
	f.ListAppsCalls = append(f.ListAppsCalls, in)
	if f.ListAppsFn != nil {
		return f.ListAppsFn(ctx, in, opts...)
	}
	return &pb.ListAppsResponse{}, nil
}

func (f *FakeAppRegistryClient) GetApp(ctx context.Context, in *pb.GetAppRequest, opts ...grpc.CallOption) (*pb.GetAppResponse, error) {
	f.GetAppCalls = append(f.GetAppCalls, in)
	if f.GetAppFn != nil {
		return f.GetAppFn(ctx, in, opts...)
	}
	return &pb.GetAppResponse{}, nil
}

func (f *FakeAppRegistryClient) ListCharts(ctx context.Context, in *pb.ListChartsRequest, opts ...grpc.CallOption) (*pb.ListChartsResponse, error) {
	f.ListChartsCalls = append(f.ListChartsCalls, in)
	if f.ListChartsFn != nil {
		return f.ListChartsFn(ctx, in, opts...)
	}
	return &pb.ListChartsResponse{}, nil
}

func (f *FakeAppRegistryClient) SetAppStatus(ctx context.Context, in *pb.SetAppStatusRequest, opts ...grpc.CallOption) (*pb.SetAppStatusResponse, error) {
	f.SetAppStatusCalls = append(f.SetAppStatusCalls, in)
	if f.SetAppStatusFn != nil {
		return f.SetAppStatusFn(ctx, in, opts...)
	}
	return &pb.SetAppStatusResponse{}, nil
}

func (f *FakeAppRegistryClient) ListReconcileRuns(ctx context.Context, in *pb.ListReconcileRunsRequest, opts ...grpc.CallOption) (*pb.ListReconcileRunsResponse, error) {
	f.ListReconcileRunsCalls = append(f.ListReconcileRunsCalls, in)
	if f.ListReconcileRunsFn != nil {
		return f.ListReconcileRunsFn(ctx, in, opts...)
	}
	return &pb.ListReconcileRunsResponse{}, nil
}

// FakeArtifactRegistryClient is an in-memory test implementation of pb.ArtifactRegistryClient.
type FakeArtifactRegistryClient struct {
	RecordBuildFn           func(ctx context.Context, in *pb.RecordBuildRequest, opts ...grpc.CallOption) (*pb.RecordBuildResponse, error)
	RecordArtifactFn        func(ctx context.Context, in *pb.RecordArtifactRequest, opts ...grpc.CallOption) (*pb.RecordArtifactResponse, error)
	BeginPublishFn          func(ctx context.Context, in *pb.BeginPublishRequest, opts ...grpc.CallOption) (*pb.BeginPublishResponse, error)
	FailPublishFn           func(ctx context.Context, in *pb.FailPublishRequest, opts ...grpc.CallOption) (*pb.FailPublishResponse, error)
	BeginPublishBatchFn     func(ctx context.Context, in *pb.BeginPublishBatchRequest, opts ...grpc.CallOption) (*pb.BeginPublishBatchResponse, error)
	ListArtifactsFn         func(ctx context.Context, in *pb.ListArtifactsRequest, opts ...grpc.CallOption) (*pb.ListArtifactsResponse, error)
	GetArtifactFn           func(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error)
	GetReleaseRunFn         func(ctx context.Context, in *pb.GetReleaseRunRequest, opts ...grpc.CallOption) (*pb.GetReleaseRunResponse, error)
	ResolveArtifactFn       func(ctx context.Context, in *pb.ResolveArtifactRequest, opts ...grpc.CallOption) (*pb.ResolveArtifactResponse, error)
	ListBuildsFn            func(ctx context.Context, in *pb.ListBuildsRequest, opts ...grpc.CallOption) (*pb.ListBuildsResponse, error)
	ListArtifactPinsFn      func(ctx context.Context, in *pb.ListArtifactPinsRequest, opts ...grpc.CallOption) (*pb.ListArtifactPinsResponse, error)
	AllocateVersionFn       func(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error)
	AdoptArtifactFn         func(ctx context.Context, in *pb.AdoptArtifactRequest, opts ...grpc.CallOption) (*pb.AdoptArtifactResponse, error)
	CheckChartHermeticityFn func(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error)

	RecordBuildCalls           []*pb.RecordBuildRequest
	RecordArtifactCalls        []*pb.RecordArtifactRequest
	BeginPublishCalls          []*pb.BeginPublishRequest
	FailPublishCalls           []*pb.FailPublishRequest
	BeginPublishBatchCalls     []*pb.BeginPublishBatchRequest
	ListArtifactsCalls         []*pb.ListArtifactsRequest
	GetArtifactCalls           []*pb.GetArtifactRequest
	GetReleaseRunCalls         []*pb.GetReleaseRunRequest
	ResolveArtifactCalls       []*pb.ResolveArtifactRequest
	ListBuildsCalls            []*pb.ListBuildsRequest
	ListArtifactPinsCalls      []*pb.ListArtifactPinsRequest
	AllocateVersionCalls       []*pb.AllocateVersionRequest
	AdoptArtifactCalls         []*pb.AdoptArtifactRequest
	CheckChartHermeticityCalls []*pb.CheckChartHermeticityRequest
}

// NewFakeArtifactRegistryClient creates a new FakeArtifactRegistryClient.
func NewFakeArtifactRegistryClient() *FakeArtifactRegistryClient {
	return &FakeArtifactRegistryClient{}
}

func (f *FakeArtifactRegistryClient) RecordBuild(ctx context.Context, in *pb.RecordBuildRequest, opts ...grpc.CallOption) (*pb.RecordBuildResponse, error) {
	f.RecordBuildCalls = append(f.RecordBuildCalls, in)
	if f.RecordBuildFn != nil {
		return f.RecordBuildFn(ctx, in, opts...)
	}
	return &pb.RecordBuildResponse{Build: &pb.Build{BuildId: "test-build-id", GitSha: in.GitSha}}, nil
}

func (f *FakeArtifactRegistryClient) RecordArtifact(ctx context.Context, in *pb.RecordArtifactRequest, opts ...grpc.CallOption) (*pb.RecordArtifactResponse, error) {
	f.RecordArtifactCalls = append(f.RecordArtifactCalls, in)
	if f.RecordArtifactFn != nil {
		return f.RecordArtifactFn(ctx, in, opts...)
	}
	return &pb.RecordArtifactResponse{Artifact: &pb.Artifact{ArtifactId: "test-artifact-id", State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHED, Digest: in.Digest}}, nil
}

func (f *FakeArtifactRegistryClient) BeginPublish(ctx context.Context, in *pb.BeginPublishRequest, opts ...grpc.CallOption) (*pb.BeginPublishResponse, error) {
	f.BeginPublishCalls = append(f.BeginPublishCalls, in)
	if f.BeginPublishFn != nil {
		return f.BeginPublishFn(ctx, in, opts...)
	}
	return &pb.BeginPublishResponse{Artifact: &pb.Artifact{ArtifactId: "test-artifact-id", State: pb.ArtifactState_ARTIFACT_STATE_PUBLISHING}}, nil
}

func (f *FakeArtifactRegistryClient) FailPublish(ctx context.Context, in *pb.FailPublishRequest, opts ...grpc.CallOption) (*pb.FailPublishResponse, error) {
	f.FailPublishCalls = append(f.FailPublishCalls, in)
	if f.FailPublishFn != nil {
		return f.FailPublishFn(ctx, in, opts...)
	}
	return &pb.FailPublishResponse{Artifact: &pb.Artifact{ArtifactId: "test-artifact-id", State: pb.ArtifactState_ARTIFACT_STATE_FAILED, FailReason: in.Reason}}, nil
}

func (f *FakeArtifactRegistryClient) BeginPublishBatch(ctx context.Context, in *pb.BeginPublishBatchRequest, opts ...grpc.CallOption) (*pb.BeginPublishBatchResponse, error) {
	f.BeginPublishBatchCalls = append(f.BeginPublishBatchCalls, in)
	if f.BeginPublishBatchFn != nil {
		return f.BeginPublishBatchFn(ctx, in, opts...)
	}
	return &pb.BeginPublishBatchResponse{}, nil
}

func (f *FakeArtifactRegistryClient) ListArtifacts(ctx context.Context, in *pb.ListArtifactsRequest, opts ...grpc.CallOption) (*pb.ListArtifactsResponse, error) {
	f.ListArtifactsCalls = append(f.ListArtifactsCalls, in)
	if f.ListArtifactsFn != nil {
		return f.ListArtifactsFn(ctx, in, opts...)
	}
	return &pb.ListArtifactsResponse{}, nil
}

func (f *FakeArtifactRegistryClient) GetArtifact(ctx context.Context, in *pb.GetArtifactRequest, opts ...grpc.CallOption) (*pb.GetArtifactResponse, error) {
	f.GetArtifactCalls = append(f.GetArtifactCalls, in)
	if f.GetArtifactFn != nil {
		return f.GetArtifactFn(ctx, in, opts...)
	}
	if in.BeforeVersion != "" {
		return &pb.GetArtifactResponse{}, nil
	}
	ver := in.Version
	if ver == "" {
		ver = "v1.0.0"
	}
	return &pb.GetArtifactResponse{
		Artifact: &pb.Artifact{
			ArtifactId: "test-artifact-id",
			Version:    ver,
			Digest:     "sha256:testartifactdigest",
			State:      pb.ArtifactState_ARTIFACT_STATE_PUBLISHED,
		},
	}, nil
}

func (f *FakeArtifactRegistryClient) GetReleaseRun(ctx context.Context, in *pb.GetReleaseRunRequest, opts ...grpc.CallOption) (*pb.GetReleaseRunResponse, error) {
	f.GetReleaseRunCalls = append(f.GetReleaseRunCalls, in)
	if f.GetReleaseRunFn != nil {
		return f.GetReleaseRunFn(ctx, in, opts...)
	}
	return &pb.GetReleaseRunResponse{}, nil
}

func (f *FakeArtifactRegistryClient) ResolveArtifact(ctx context.Context, in *pb.ResolveArtifactRequest, opts ...grpc.CallOption) (*pb.ResolveArtifactResponse, error) {
	f.ResolveArtifactCalls = append(f.ResolveArtifactCalls, in)
	if f.ResolveArtifactFn != nil {
		return f.ResolveArtifactFn(ctx, in, opts...)
	}
	return &pb.ResolveArtifactResponse{}, nil
}

func (f *FakeArtifactRegistryClient) ListBuilds(ctx context.Context, in *pb.ListBuildsRequest, opts ...grpc.CallOption) (*pb.ListBuildsResponse, error) {
	f.ListBuildsCalls = append(f.ListBuildsCalls, in)
	if f.ListBuildsFn != nil {
		return f.ListBuildsFn(ctx, in, opts...)
	}
	return &pb.ListBuildsResponse{}, nil
}

func (f *FakeArtifactRegistryClient) ListArtifactPins(ctx context.Context, in *pb.ListArtifactPinsRequest, opts ...grpc.CallOption) (*pb.ListArtifactPinsResponse, error) {
	f.ListArtifactPinsCalls = append(f.ListArtifactPinsCalls, in)
	if f.ListArtifactPinsFn != nil {
		return f.ListArtifactPinsFn(ctx, in, opts...)
	}
	return &pb.ListArtifactPinsResponse{}, nil
}

func (f *FakeArtifactRegistryClient) AllocateVersion(ctx context.Context, in *pb.AllocateVersionRequest, opts ...grpc.CallOption) (*pb.AllocateVersionResponse, error) {
	f.AllocateVersionCalls = append(f.AllocateVersionCalls, in)
	if f.AllocateVersionFn != nil {
		return f.AllocateVersionFn(ctx, in, opts...)
	}
	return &pb.AllocateVersionResponse{}, nil
}

func (f *FakeArtifactRegistryClient) AdoptArtifact(ctx context.Context, in *pb.AdoptArtifactRequest, opts ...grpc.CallOption) (*pb.AdoptArtifactResponse, error) {
	f.AdoptArtifactCalls = append(f.AdoptArtifactCalls, in)
	if f.AdoptArtifactFn != nil {
		return f.AdoptArtifactFn(ctx, in, opts...)
	}
	return &pb.AdoptArtifactResponse{}, nil
}

func (f *FakeArtifactRegistryClient) CheckChartHermeticity(ctx context.Context, in *pb.CheckChartHermeticityRequest, opts ...grpc.CallOption) (*pb.CheckChartHermeticityResponse, error) {
	f.CheckChartHermeticityCalls = append(f.CheckChartHermeticityCalls, in)
	if f.CheckChartHermeticityFn != nil {
		return f.CheckChartHermeticityFn(ctx, in, opts...)
	}
	return &pb.CheckChartHermeticityResponse{Enforced: false}, nil
}
