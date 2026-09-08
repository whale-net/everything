package workshop

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/manmanv2/api/workshop"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// cachePresigner is the narrow S3 presign surface GetCacheDownloadURL and
// GetCacheUploadURL need -- exactly the two single-object presign methods,
// nothing else (no Upload, Delete, Exists, etc). *s3lib.Client (wired in by
// manmanv2/api/main.go) satisfies this. Declaring it here rather than
// depending on *s3lib.Client directly lets cache_test.go substitute a fake
// that records the exact key/ttl it was called with and can be made to fail
// on demand, without dragging in real AWS SDK config/credentials or letting
// a test accidentally reach for one of the concrete client's unrelated
// methods.
type cachePresigner interface {
	PresignGetURL(ctx context.Context, key string, ttl time.Duration) (string, error)
	PresignPutURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// cacheURLTTL is NFR6's load-bearing constant: every presigned URL this
// handler issues -- GET (GetCacheDownloadURL) or PUT (GetCacheUploadURL) --
// expires in minutes, not hours. The existing backup flow's 1-hour TTL
// (manmanv2/api/handlers/backup_config.go's PresignPutURL call) is
// deliberately not the precedent to copy here: a leaked or logged Workshop
// cache URL must have a narrow blast radius. Do not raise this without a
// plan amendment (plan #2175 NFR6).
const cacheURLTTL = 5 * time.Minute

// requireHostIdentity enforces the host's existing gRPC authentication
// (same interceptor path every other host-facing RPC uses, libs/go/grpcauth)
// and defends against one host minting a cache URL while impersonating
// another server_id.
//
// Claims.Subject only carries a claim scoped to a single server once
// host-manager is provisioned per-host credentials -- not yet true for the
// shared service-account credential every host-manager dials control-api
// with today (manmanv2/host/main.go's GRPC_AUTH_CLIENT_ID/SECRET are one
// pair for the whole fleet), nor for AuthModeNone's fixed "dev-user" dev
// claims. So: a Subject that parses as an int64 is treated as a claimed
// server_id and MUST match serverID (PermissionDenied on mismatch); any
// other Subject shape is an identity not scoped to a specific server and is
// authorized for any server_id, matching today's actual deployment. This
// keeps the check meaningful the moment per-host credentials exist, without
// breaking every request under the fleet's current shared credential.
func requireHostIdentity(ctx context.Context, serverID int64) error {
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "request carries no credentials")
	}
	if subjectServerID, err := strconv.ParseInt(claims.Subject, 10, 64); err == nil {
		if subjectServerID != serverID {
			return status.Errorf(codes.PermissionDenied, "server_id %d does not match authenticated host identity", serverID)
		}
	}
	return nil
}

// GetCacheDownloadURL implements FR6: if a cache entry already exists for
// (workshop_id, content_version)'s content-addressed key, return a
// short-lived single-object presigned GET URL for it. A miss is a normal
// answer (cache_hit = false, no error, no URL) -- the host falls back to a
// normal SteamCMD download. Recording host presence happens on the host's
// side once it reports a completed read (a URL issued is not a copy
// landed), not here.
func (h *WorkshopServiceHandler) GetCacheDownloadURL(ctx context.Context, req *pb.GetCacheDownloadURLRequest) (*pb.GetCacheDownloadURLResponse, error) {
	if req.ServerId == 0 {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	if req.WorkshopId == "" {
		return nil, status.Error(codes.InvalidArgument, "workshop_id is required")
	}
	if req.ContentVersion == "" {
		return nil, status.Error(codes.InvalidArgument, "content_version is required")
	}
	if err := requireHostIdentity(ctx, req.ServerId); err != nil {
		return nil, err
	}

	cacheKey := workshop.CacheKey(req.WorkshopId, req.ContentVersion)
	entry, err := h.cacheRepo.GetCacheEntryByKey(ctx, cacheKey)
	if err != nil {
		slog.Warn("failed to look up workshop cache entry", "cache_key", cacheKey, "server_id", req.ServerId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to look up cache entry: %v", err)
	}
	if entry == nil {
		return &pb.GetCacheDownloadURLResponse{CacheHit: false}, nil
	}

	// Scoped to exactly this one object key -- never a prefix, never
	// bucket-wide, never a static credential (NFR6).
	presignedURL, err := h.s3Client.PresignGetURL(ctx, entry.S3Key, cacheURLTTL)
	if err != nil {
		slog.Warn("failed to presign workshop cache download URL", "cache_entry_id", entry.CacheEntryID, "cache_key", cacheKey, "server_id", req.ServerId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to generate presigned URL: %v", err)
	}
	expiresAt := time.Now().Add(cacheURLTTL)

	// NFR6: log the entry identity and expiry, never the URL or its query string.
	slog.Info("issued workshop cache download URL",
		"cache_entry_id", entry.CacheEntryID, "cache_key", cacheKey, "server_id", req.ServerId, "expires_at", expiresAt.Unix())

	resp := &pb.GetCacheDownloadURLResponse{
		CacheHit:     true,
		CacheEntryId: entry.CacheEntryID,
		PresignedUrl: presignedURL,
		ExpiresAt:    expiresAt.Unix(),
	}
	if entry.SizeBytes != nil {
		resp.SizeBytes = *entry.SizeBytes
	}
	return resp, nil
}

// GetCacheUploadURL implements FR9: derive the cache entry for
// (workshop_id, content_version), idempotently ensuring the row exists so
// two hosts racing to cache the same version converge on the same
// cache_entry_id and s3_key (NFR4, no distributed lock per LB8), then
// return a short-lived single-object presigned PUT URL for it.
func (h *WorkshopServiceHandler) GetCacheUploadURL(ctx context.Context, req *pb.GetCacheUploadURLRequest) (*pb.GetCacheUploadURLResponse, error) {
	if req.ServerId == 0 {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	if req.WorkshopId == "" {
		return nil, status.Error(codes.InvalidArgument, "workshop_id is required")
	}
	if req.ContentVersion == "" {
		return nil, status.Error(codes.InvalidArgument, "content_version is required")
	}
	if err := requireHostIdentity(ctx, req.ServerId); err != nil {
		return nil, err
	}

	cacheKey := workshop.CacheKey(req.WorkshopId, req.ContentVersion)
	s3Key := workshop.S3Key(cacheKey)

	entry, err := h.cacheRepo.UpsertCacheEntry(ctx, &manman.WorkshopCacheEntry{
		WorkshopID:     req.WorkshopId,
		ContentVersion: req.ContentVersion,
		CacheKey:       cacheKey,
		S3Key:          s3Key,
	})
	if err != nil {
		slog.Warn("failed to upsert workshop cache entry", "cache_key", cacheKey, "server_id", req.ServerId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to create cache entry: %v", err)
	}

	// Scoped to exactly this one object key -- never a prefix, never
	// bucket-wide, never a static credential (NFR6).
	presignedURL, err := h.s3Client.PresignPutURL(ctx, entry.S3Key, cacheURLTTL)
	if err != nil {
		slog.Warn("failed to presign workshop cache upload URL", "cache_entry_id", entry.CacheEntryID, "cache_key", cacheKey, "server_id", req.ServerId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to generate presigned URL: %v", err)
	}
	expiresAt := time.Now().Add(cacheURLTTL)

	// NFR6: log the entry identity and expiry, never the URL or its query string.
	slog.Info("issued workshop cache upload URL",
		"cache_entry_id", entry.CacheEntryID, "cache_key", cacheKey, "server_id", req.ServerId, "expires_at", expiresAt.Unix())

	return &pb.GetCacheUploadURLResponse{
		CacheEntryId: entry.CacheEntryID,
		PresignedUrl: presignedURL,
		ExpiresAt:    expiresAt.Unix(),
		S3Key:        entry.S3Key,
	}, nil
}
