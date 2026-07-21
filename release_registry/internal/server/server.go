// Package server implements the gRPC RegistryService with Postgres-backed handlers.
package server

import (
	"context"
	"fmt"
	"strconv"

	pb "github.com/whale-net/everything/release_registry/proto/gen"
)

var _ pb.RegistryServiceServer = (*Server)(nil)

// RegisterApp implements FR7: idempotent app upsert.
func (s *Server) RegisterApp(ctx context.Context, req *pb.RegisterAppRequest) (*pb.RegisterAppResponse, error) {
	m := req.GetMetadata()
	if m == nil || m.AppKey == "" {
		return &pb.RegisterAppResponse{Success: false}, fmt.Errorf("invalid request: missing metadata/app_key")
	}

	query := `INSERT INTO registry_apps (app_key, domain, name, registry, organization) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (app_key) DO UPDATE SET domain = EXCLUDED.domain, name = EXCLUDED.name, registry = EXCLUDED.registry, organization = EXCLUDED.organization`

	if _, err := s.pool.Exec(ctx, query, m.AppKey, m.Domain, m.Name, m.Registry, m.Organization); err != nil {
		return &pb.RegisterAppResponse{Success: false}, fmt.Errorf("register app error: %w", err)
	}
	return &pb.RegisterAppResponse{Success: true}, nil
}

// RegisterCommit implements FR1: lightweight audit trail for commits.
func (s *Server) RegisterCommit(ctx context.Context, req *pb.RegisterCommitRequest) (*pb.RegisterCommitResponse, error) {
	r := req.GetRecord()
	if r == nil || r.Sha == "" {
		return &pb.RegisterCommitResponse{Success: false}, fmt.Errorf("invalid request: missing commit record")
	}

	query := `INSERT INTO registry_commits (repo, sha, ref, timestamp) VALUES ($1, $2, $3, NOW())`
	if _, err := s.pool.Exec(ctx, query, r.Repo, r.Sha, r.Ref); err != nil {
		return &pb.RegisterCommitResponse{Success: false}, fmt.Errorf("register commit error: %w", err)
	}
	return &pb.RegisterCommitResponse{Success: true}, nil
}

// RegisterArtifact implements FR2/FR3: artifact registration with registry-side version computation.
func (s *Server) RegisterArtifact(ctx context.Context, req *pb.RegisterArtifactRequest) (*pb.RegisterArtifactResponse, error) {
	a := req.GetAppKey()
	if a == "" || req.Kind == pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED {
		return &pb.RegisterArtifactResponse{Success: false}, fmt.Errorf("invalid request: missing app_key or kind")
	}

	version, err := s.nextVersion(ctx, a, string(req.Kind))
	if err != nil {
		return nil, fmt.Errorf("next version error: %w", err)
	}

	query := `INSERT INTO registry_artifacts (app_key, kind, version, commit_sha) VALUES ($1, $2, $3, $4)`
	if _, err := s.pool.Exec(ctx, query, a, req.Kind, version, req.CommitSha); err != nil {
		return &pb.RegisterArtifactResponse{Success: false}, fmt.Errorf("register artifact error: %w", err)
	}
	return &pb.RegisterArtifactResponse{Success: true, Version: version}, nil
}

// Promote implements FR4: SCD2 write path for promotions.
func (s *Server) Promote(ctx context.Context, req *pb.PromoteRequest) (*pb.PromoteResponse, error) {
	if req.AppKey == "" || req.Env == "" || req.Kind == pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED || req.Version == "" {
		return &pb.PromoteResponse{Success: false}, fmt.Errorf("invalid request: missing required fields")
	}

	var commitSha string
	if err := s.pool.QueryRow(ctx,
		`SELECT commit_sha FROM registry_artifacts WHERE app_key=$1 AND kind=$2 AND version=$3 ORDER BY created_at DESC LIMIT 1`,
		req.AppKey, req.Kind, req.Version).Scan(&commitSha); err != nil {
		return &pb.PromoteResponse{Success: false}, fmt.Errorf("lookup commit sha error: %w", err)
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE registry_promotions SET valid_to = NOW() WHERE app_key=$1 AND env=$2 AND kind=$3 AND valid_to IS NULL`,
		req.AppKey, req.Env, req.Kind); err != nil {
		return &pb.PromoteResponse{Success: false}, fmt.Errorf("SCD2 close error: %w", err)
	}

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO registry_promotions (app_key, env, kind, version, commit_sha) VALUES ($1, $2, $3, $4, $5)`,
		req.AppKey, req.Env, req.Kind, req.Version, commitSha); err != nil {
		return &pb.PromoteResponse{Success: false}, fmt.Errorf("SCD2 insert error: %w", err)
	}
	return &pb.PromoteResponse{Success: true}, nil
}

// Resolve implements FR5: lookup current promoted version for (app, env, kind).
func (s *Server) Resolve(ctx context.Context, req *pb.ResolveRequest) (*pb.ResolveResponse, error) {
	if req.AppKey == "" || req.Env == "" || req.Kind == pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED {
		return &pb.ResolveResponse{Version: "", CommitSha: ""}, fmt.Errorf("invalid request: missing required fields")
	}

	var version, commitSha string
	if err := s.pool.QueryRow(ctx,
		`SELECT pr.version, pr.commit_sha FROM registry_promotions pr WHERE pr.app_key=$1 AND pr.env=$2 AND pr.kind=$3 AND pr.valid_to IS NULL ORDER BY pr.valid_from DESC LIMIT 1`,
		req.AppKey, req.Env, req.Kind).Scan(&version, &commitSha); err != nil {
		return &pb.ResolveResponse{Version: "", CommitSha: ""}, fmt.Errorf("resolve error: %w", err)
	}
	return &pb.ResolveResponse{Version: version, CommitSha: commitSha}, nil
}

// nextVersion computes the next semver for an app+kind by finding the latest artifact.
func (s *Server) nextVersion(ctx context.Context, appKey string, kind string) (string, error) {
	var lastVer string
	if err := s.pool.QueryRow(ctx,
		`SELECT version FROM registry_artifacts WHERE app_key=$1 AND kind=$2 ORDER BY created_at DESC LIMIT 1`,
		appKey, kind).Scan(&lastVer); err != nil {
		return "v0.0.0", nil // first artifact gets v0.0.0
	}

	if lastVer == "" || len(lastVer) < 3 {
		return "v0.0.1", nil
	}

	major, minor, patch := parseSemver(lastVer[1:])
	return "v" + formatSemver(major, minor, patch+1), nil
}

// parseSemver parses MAJOR.MINOR.PATCH from a semver string (without leading 'v').
func parseSemver(s string) (int, int, int) {
	var major, minor, patch int
	state := 0 // 0=major, 1.minor, 2.patch

	for _, c := range s {
		if c >= '0' && c <= '9' {
			digit := int(c) - '0'
			switch state {
			case 0:
				major = major*10 + digit
			case 1:
				minor = minor*10 + digit
			case 2:
				patch = patch*10 + digit
			}
		} else if c == '.' {
			state++
		}
	}

	return major, minor, patch
}

func formatSemver(m, mi, p int) string {
	return fmt.Sprintf("%d.%d.%d", m, mi, p)
}
