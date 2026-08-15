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

// FileSystem provides file system operations. Replaced in tests with a fake.
type FileSystem interface {
	Stat(path string) (os.FileInfo, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
}

// ArtifactRecorder records published artifacts in App Registry.
type ArtifactRecorder interface {
	RecordArtifact(ctx context.Context, req *pb.RecordArtifactRequest) (*pb.RecordArtifactResponse, error)
}

// grpcArtifactRecorder dials App Registry and calls RecordArtifact.
type grpcArtifactRecorder struct{}

func (grpcArtifactRecorder) RecordArtifact(ctx context.Context, req *pb.RecordArtifactRequest) (*pb.RecordArtifactResponse, error) {
	address := defaultEnv("APP_REGISTRY_ADDRESS")
	if address == "" {
		return nil, fmt.Errorf("APP_REGISTRY_ADDRESS is not set")
	}

	authOpt, err := grpcauth.NewServiceAccountDialOption(grpcauth.ClientConfig{
		Mode:         grpcauth.AuthMode(envOrDefault("GRPC_AUTH_MODE", "none")),
		TokenURL:     defaultEnv("GRPC_AUTH_TOKEN_URL"),
		ClientID:     defaultEnv("GRPC_AUTH_CLIENT_ID"),
		ClientSecret: defaultEnv("GRPC_AUTH_CLIENT_SECRET"),
	})
	if err != nil {
		return nil, fmt.Errorf("app registry auth: %w", err)
	}

	conn, err := grpcclient.NewClient(ctx, address, authOpt)
	if err != nil {
		return nil, fmt.Errorf("dial app-registry-api at %s: %w", address, err)
	}
	defer conn.Close() //nolint:errcheck

	client := pb.NewArtifactRegistryClient(conn.GetConnection())
	return client.RecordArtifact(ctx, req)
}

// EnvLookup reads environment variables. Replaced in tests with a fake.
type EnvLookup func(key string) string

// Package-level defaults — replaced in tests via with* helpers.
var defaultFS FileSystem = realFS{}
var defaultEnv EnvLookup = os.Getenv
var defaultBazel BazelRunner   // set in init() after realBazelRunner is defined
var defaultGit GitRunner       // set in init() after realGitRunner is defined
var defaultWorkspaceRoot func() (string, error) = findWorkspaceRoot
var defaultArtifactRecorder ArtifactRecorder = grpcArtifactRecorder{}
