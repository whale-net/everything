package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// BazelRunner runs bazel commands and returns stdout.
type BazelRunner interface {
	Run(args ...string) (string, error)
}

// GitRunner runs git commands and returns stdout.
type GitRunner interface {
	Run(args ...string) (string, error)
}

// DockerRunner runs docker commands and returns stdout.
type DockerRunner interface {
	Run(args ...string) (string, error)
}

// FileSystem provides file system operations. Replaced in tests with a fake.
type FileSystem interface {
	Stat(path string) (os.FileInfo, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
}

// ArtifactRecorder records published artifacts in App Registry. Since the
// AR-5 cutover, RecordArtifact always requires a prior BeginPublish (there
// is no more per-domain direct-create fallback), so this interface covers
// the same BeginPublish -> RecordArtifact/FailPublish sequence releaser.go
// uses for apps/charts.
type ArtifactRecorder interface {
	BeginPublish(ctx context.Context, req *pb.BeginPublishRequest) (*pb.BeginPublishResponse, error)
	RecordArtifact(ctx context.Context, req *pb.RecordArtifactRequest) (*pb.RecordArtifactResponse, error)
	FailPublish(ctx context.Context, req *pb.FailPublishRequest) (*pb.FailPublishResponse, error)
}

// grpcArtifactRecorder dials App Registry fresh per call, matching the
// "one dial per invocation" posture of this package's other App Registry
// call sites (see registry_version.go's dialVersioningClient).
type grpcArtifactRecorder struct{}

func (grpcArtifactRecorder) dial(ctx context.Context) (pb.ArtifactRegistryClient, func() error, error) {
	address := defaultEnv("APP_REGISTRY_ADDRESS")
	if address == "" {
		return nil, nil, fmt.Errorf("APP_REGISTRY_ADDRESS is not set")
	}

	authOpt, err := grpcauth.NewServiceAccountDialOption(grpcauth.ClientConfig{
		Mode:         grpcauth.AuthMode(envOrDefault("GRPC_AUTH_MODE", "none")),
		TokenURL:     defaultEnv("GRPC_AUTH_TOKEN_URL"),
		ClientID:     defaultEnv("GRPC_AUTH_CLIENT_ID"),
		ClientSecret: defaultEnv("GRPC_AUTH_CLIENT_SECRET"),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("app registry auth: %w", err)
	}

	conn, err := grpcclient.NewClient(ctx, address, authOpt)
	if err != nil {
		return nil, nil, fmt.Errorf("dial app-registry-api at %s: %w", address, err)
	}
	return pb.NewArtifactRegistryClient(conn.GetConnection()), conn.Close, nil
}

func (r grpcArtifactRecorder) BeginPublish(ctx context.Context, req *pb.BeginPublishRequest) (*pb.BeginPublishResponse, error) {
	client, closeFn, err := r.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn() //nolint:errcheck
	return client.BeginPublish(ctx, req)
}

func (r grpcArtifactRecorder) RecordArtifact(ctx context.Context, req *pb.RecordArtifactRequest) (*pb.RecordArtifactResponse, error) {
	client, closeFn, err := r.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn() //nolint:errcheck
	return client.RecordArtifact(ctx, req)
}

func (r grpcArtifactRecorder) FailPublish(ctx context.Context, req *pb.FailPublishRequest) (*pb.FailPublishResponse, error) {
	client, closeFn, err := r.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer closeFn() //nolint:errcheck
	return client.FailPublish(ctx, req)
}

// EnvLookup reads environment variables. Replaced in tests with a fake.
type EnvLookup func(key string) string

// Package-level defaults — replaced in tests via with* helpers.
var defaultFS FileSystem = realFS{}
var defaultEnv EnvLookup = os.Getenv
var defaultBazel BazelRunner   // set in init() after realBazelRunner is defined
var defaultGit GitRunner       // set in init() after realGitRunner is defined
var defaultDocker DockerRunner // set in init() after realDockerRunner is defined
var defaultWorkspaceRoot func() (string, error) = findWorkspaceRoot
var defaultArtifactRecorder ArtifactRecorder = grpcArtifactRecorder{}
