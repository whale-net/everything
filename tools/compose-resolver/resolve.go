package main

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// registryConfig is what's needed to ask app-registry what should currently
// be running for one image repository in one environment.
type registryConfig struct {
	Address      string
	Environment  string
	Repository   string
	AuthMode     string
	AuthTokenURL string
	ClientID     string
	ClientSecret string
}

// resolvedArtifact is the artifact app-registry says is currently promoted.
type resolvedArtifact struct {
	Version string
	Digest  string
}

// resolveCurrentVersion dials app-registry, fetches the environment's
// current state, and returns whichever entry's artifact repository matches
// cfg.Repository. Returns (nil, nil), not an error, if nothing is promoted
// for it yet -- that's a valid "not adopted here" state, not a failure.
func resolveCurrentVersion(ctx context.Context, cfg registryConfig) (*resolvedArtifact, error) {
	authOpt, err := grpcauth.NewServiceAccountDialOption(grpcauth.ClientConfig{
		Mode:         grpcauth.AuthMode(cfg.AuthMode),
		TokenURL:     cfg.AuthTokenURL,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("creating auth dial option: %w", err)
	}

	conn, err := grpcclient.NewClient(ctx, cfg.Address, authOpt)
	if err != nil {
		return nil, fmt.Errorf("connecting to app-registry-api at %s: %w", cfg.Address, err)
	}
	defer conn.Close() //nolint:errcheck

	resp, err := pb.NewPromotionRegistryClient(conn.GetConnection()).GetEnvironmentState(ctx, &pb.GetEnvironmentStateRequest{
		EnvironmentKey: cfg.Environment,
	})
	if err != nil {
		return nil, fmt.Errorf("GetEnvironmentState(%s): %w", cfg.Environment, err)
	}

	return findByRepository(resp.GetEntries(), cfg.Repository), nil
}

// findByRepository is the pure part of resolveCurrentVersion, split out so
// the matching logic can be unit tested without a live gRPC server.
func findByRepository(entries []*pb.EnvironmentStateEntry, repo string) *resolvedArtifact {
	for _, e := range entries {
		a := e.GetArtifact()
		if a.GetRepository() == repo {
			return &resolvedArtifact{Version: a.GetVersion(), Digest: a.GetDigest()}
		}
	}
	return nil
}
