package workshop

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/whale-net/everything/libs/go/grpcauth"
	s3lib "github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/manmanv2/api/workshop"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// cachePresigner is the narrow S3 surface the cache handlers need: the two
// single-object presign methods GetCacheDownloadURL and GetCacheUploadURL
// use, plus the single-object Delete EvictCacheEntry uses (FR12) -- nothing
// else (no Upload, Exists, etc). *s3lib.Client (wired in by
// manmanv2/api/main.go) satisfies this. Declaring it here rather than
// depending on *s3lib.Client directly lets cache_test.go substitute a fake
// that records the exact key/ttl/delete calls it received and can be made to
// fail on demand, without dragging in real AWS SDK config/credentials or
// letting a test accidentally reach for one of the concrete client's
// unrelated methods.
//
// Public-endpoint variants only (FR6/FR7/FR9): the internal-endpoint
// PresignGetURL/PresignPutURL are for callers that reach S3 directly from
// inside the cluster network. Host-manager runs on bare metal
// (manmanv2/README-HOST.md) and cannot resolve or route to that internal
// endpoint, so both cache RPCs must presign against Config.PublicEndpoint
// via PresignPublicGetURL/PresignPublicPutURL -- the same public-endpoint
// mechanism App Registry's ResolveBinaryURL already relies on in production
// (tools/app_registry/server/handlers/artifact.go).
type cachePresigner interface {
	PresignPublicGetURL(ctx context.Context, key string, ttl time.Duration) (string, error)
	PresignPublicPutURL(ctx context.Context, key string, ttl time.Duration) (string, error)
	// Delete deletes exactly the single object at key -- never a prefix,
	// never a bulk/multi-object delete (FR12 blast-radius rule).
	Delete(ctx context.Context, key string) error
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
	// bucket-wide, never a static credential (NFR6). Signed against the
	// public endpoint (FR6/FR7): host-manager runs on bare metal and cannot
	// reach control-api's internal S3 endpoint.
	presignedURL, err := h.s3Client.PresignPublicGetURL(ctx, entry.S3Key, cacheURLTTL)
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
	// bucket-wide, never a static credential (NFR6). Signed against the
	// public endpoint (FR9/FR7): host-manager runs on bare metal and cannot
	// reach control-api's internal S3 endpoint.
	presignedURL, err := h.s3Client.PresignPublicPutURL(ctx, entry.S3Key, cacheURLTTL)
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

// ReportCacheRead implements FR10's data source: a host calls this after it
// has actually landed a cached object on disk via GetCacheDownloadURL, so
// that host presence for the entry is recorded. A URL being issued is not
// the same as a copy landing -- this call is what makes the difference, and
// the host-manager caller is responsible for only invoking it post-landing.
func (h *WorkshopServiceHandler) ReportCacheRead(ctx context.Context, req *pb.ReportCacheReadRequest) (*pb.ReportCacheReadResponse, error) {
	if req.ServerId == 0 {
		return nil, status.Error(codes.InvalidArgument, "server_id is required")
	}
	if req.CacheEntryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "cache_entry_id is required")
	}
	if err := requireHostIdentity(ctx, req.ServerId); err != nil {
		return nil, err
	}

	if err := h.cacheRepo.UpsertHostPresence(ctx, req.CacheEntryId, req.ServerId); err != nil {
		slog.Warn("failed to record workshop cache host presence", "cache_entry_id", req.CacheEntryId, "server_id", req.ServerId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to record host presence: %v", err)
	}

	slog.Info("recorded workshop cache host presence", "cache_entry_id", req.CacheEntryId, "server_id", req.ServerId)
	return &pb.ReportCacheReadResponse{}, nil
}

// ListAddonCacheEntries implements FR10: fleet-wide Admin visibility into a
// single addon's cache -- every content-addressed version, which hosts hold
// a copy of each, and how stale each entry's last verification is, without
// querying hosts one at a time. Resolves addon_id -> workshop_id, then
// fetches that workshop's cache entries newest-first and their host
// presence in one batched follow-up query (never one query per entry, so an
// addon with a long version history doesn't fan out into N round-trips).
// Read-only: this RPC must not create, touch, or verify any cache entry.
func (h *WorkshopServiceHandler) ListAddonCacheEntries(ctx context.Context, req *pb.ListAddonCacheEntriesRequest) (*pb.ListAddonCacheEntriesResponse, error) {
	if req.AddonId == 0 {
		return nil, status.Error(codes.InvalidArgument, "addon_id is required")
	}

	addon, err := h.addonRepo.Get(ctx, req.AddonId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "addon %d not found: %v", req.AddonId, err)
	}

	entries, err := h.cacheRepo.ListCacheEntriesForWorkshopID(ctx, addon.WorkshopID)
	if err != nil {
		slog.Warn("failed to list workshop cache entries", "addon_id", req.AddonId, "workshop_id", addon.WorkshopID, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to list cache entries: %v", err)
	}
	if len(entries) == 0 {
		return &pb.ListAddonCacheEntriesResponse{Entries: []*pb.WorkshopCacheEntry{}}, nil
	}

	cacheEntryIDs := make([]int64, len(entries))
	for i, e := range entries {
		cacheEntryIDs[i] = e.CacheEntryID
	}

	// One query for every entry's host presence -- see
	// ListHostPresenceForCacheEntryIDs' doc comment: looping ListHostPresence
	// per entry here is exactly the per-host, per-entry fan-out FR10 rules out.
	presenceByEntry, err := h.cacheRepo.ListHostPresenceForCacheEntryIDs(ctx, cacheEntryIDs)
	if err != nil {
		slog.Warn("failed to list workshop cache host presence", "addon_id", req.AddonId, "workshop_id", addon.WorkshopID, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to list cache host presence: %v", err)
	}

	pbEntries := make([]*pb.WorkshopCacheEntry, len(entries))
	for i, e := range entries {
		pbEntries[i] = cacheEntryToProto(e, presenceByEntry[e.CacheEntryID])
	}

	return &pb.ListAddonCacheEntriesResponse{Entries: pbEntries}, nil
}

// EvictCacheEntry implements FR12: Admin manual eviction of exactly one
// content-addressed cache entry. This is a control-plane action, not a host
// operation -- control-api holds the S3 credentials and deletes the object
// directly, with no presigned-URL relay and no command.host.* message
// published (FR12).
//
// Ordering is load-bearing: delete the S3 object first, then the
// workshop_cache_entries row (workshop_cache_host_presence rows for it
// cascade via ON DELETE CASCADE, migration 040). If the object delete fails,
// return an error and leave the row -- an orphaned row pointing at a live
// object is recoverable by retrying the eviction, whereas a deleted row
// pointing at a live object leaves an unreferenced object nobody can ever
// find or clean up (M4 has no GC to catch it). An object already absent
// from S3 (NoSuchKey) is treated as success and the row is still removed --
// eviction should converge, not wedge on an inconsistency.
//
// Deletes by this entry's exact object key only -- never a prefix, never a
// bulk/multi-object delete, never "all versions of this addon" (FR12
// blast-radius rule). Sibling entries for the same workshop_id, their
// presence rows, and every addon/installation/library/batch-job row are
// untouched.
func (h *WorkshopServiceHandler) EvictCacheEntry(ctx context.Context, req *pb.EvictCacheEntryRequest) (*pb.EvictCacheEntryResponse, error) {
	if req.CacheEntryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "cache_entry_id is required")
	}

	entry, err := h.cacheRepo.GetCacheEntry(ctx, req.CacheEntryId)
	if err != nil {
		slog.Warn("failed to look up workshop cache entry for eviction", "cache_entry_id", req.CacheEntryId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to look up cache entry: %v", err)
	}
	if entry == nil {
		return nil, status.Errorf(codes.NotFound, "cache entry %d not found", req.CacheEntryId)
	}

	// Object first: an object delete failure here must leave the row in
	// place so the eviction can be retried (see doc comment above).
	if err := h.s3Client.Delete(ctx, entry.S3Key); err != nil && !s3lib.IsNoSuchKey(err) {
		slog.Warn("failed to evict workshop cache object from S3", "cache_entry_id", entry.CacheEntryID, "s3_key", entry.S3Key, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to delete cache object: %v", err)
	}

	// Row second, only once the object is confirmed gone (or was already
	// gone).
	if err := h.cacheRepo.DeleteCacheEntry(ctx, entry.CacheEntryID); err != nil {
		slog.Warn("failed to delete workshop cache entry row after evicting object", "cache_entry_id", entry.CacheEntryID, "s3_key", entry.S3Key, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to delete cache entry: %v", err)
	}

	slog.Info("evicted workshop cache entry",
		"cache_entry_id", entry.CacheEntryID, "workshop_id", entry.WorkshopID, "content_version", entry.ContentVersion, "s3_key", entry.S3Key)

	return &pb.EvictCacheEntryResponse{Evicted: true, S3Key: entry.S3Key}, nil
}

// cacheEntryToProto converts a cache entry plus its already-fetched host
// presence into the wire type. LastVerifiedAt is 0 when the entry has never
// been verified (WorkshopCacheEntry.LastVerifiedAt == nil) -- the UI page
// renders that as "never verified" rather than a raw zero timestamp.
func cacheEntryToProto(entry *manman.WorkshopCacheEntry, presence []*manman.WorkshopCacheHostPresenceWithServer) *pb.WorkshopCacheEntry {
	pbEntry := &pb.WorkshopCacheEntry{
		CacheEntryId:   entry.CacheEntryID,
		WorkshopId:     entry.WorkshopID,
		ContentVersion: entry.ContentVersion,
		CacheKey:       entry.CacheKey,
		S3Key:          entry.S3Key,
		CreatedAt:      entry.CreatedAt.Unix(),
		Hosts:          make([]*pb.WorkshopCacheHost, len(presence)),
	}
	if entry.SizeBytes != nil {
		pbEntry.SizeBytes = *entry.SizeBytes
	}
	if entry.LastVerifiedAt != nil {
		pbEntry.LastVerifiedAt = entry.LastVerifiedAt.Unix()
	}
	for i, p := range presence {
		pbEntry.Hosts[i] = &pb.WorkshopCacheHost{
			ServerId:    p.ServerID,
			ServerName:  p.ServerName,
			FirstSeenAt: p.FirstSeenAt.Unix(),
			LastSeenAt:  p.LastSeenAt.Unix(),
		}
	}
	return pbEntry
}

// VerifyCacheEntry implements FR11: dispatch an on-demand SteamCMD verify of
// a single cache entry against its Workshop source, independent of any
// install. This RPC only decides *whether and where* to dispatch -- the
// up-to-date/changed outcome is reported asynchronously by the host on the
// existing status.host.*.workshop.cache key (#2184, WorkshopCacheStatusUpdate)
// and is not part of this response (see the proto's rpc doc comment).
func (h *WorkshopServiceHandler) VerifyCacheEntry(ctx context.Context, req *pb.VerifyCacheEntryRequest) (*pb.VerifyCacheEntryResponse, error) {
	if req.CacheEntryId == 0 {
		return nil, status.Error(codes.InvalidArgument, "cache_entry_id is required")
	}

	entry, err := h.cacheRepo.GetCacheEntry(ctx, req.CacheEntryId)
	if err != nil {
		slog.Warn("failed to look up workshop cache entry for verify", "cache_entry_id", req.CacheEntryId, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to look up cache entry: %v", err)
	}
	if entry == nil {
		return nil, status.Errorf(codes.NotFound, "cache entry %d not found", req.CacheEntryId)
	}

	serverID := req.ServerId
	if serverID == 0 {
		// No explicit host: pick one that actually holds a copy, preferring
		// the most recently seen -- the freshest presence row is the best
		// signal of "still has it and is likely online". No host holding a
		// copy is a real, reportable outcome (not an error): the Admin gets
		// "no_host_available" back rather than a dispatch that can never
		// land, and nothing is published.
		presence, presenceErr := h.cacheRepo.ListHostPresence(ctx, req.CacheEntryId)
		if presenceErr != nil {
			slog.Warn("failed to list workshop cache host presence for verify", "cache_entry_id", req.CacheEntryId, "error", presenceErr)
			return nil, status.Errorf(codes.Internal, "failed to list cache host presence: %v", presenceErr)
		}

		var chosen *manman.WorkshopCacheHostPresence
		for _, p := range presence {
			if chosen == nil || p.LastSeenAt.After(chosen.LastSeenAt) {
				chosen = p
			}
		}
		if chosen == nil {
			slog.Info("no host holds a copy of workshop cache entry, cannot dispatch verify", "cache_entry_id", req.CacheEntryId)
			return &pb.VerifyCacheEntryResponse{Dispatched: false, Status: "no_host_available"}, nil
		}
		serverID = chosen.ServerID
	}
	// An explicit server_id is always honored even if that host does not
	// hold a copy -- an Admin may deliberately ask a specific host to fetch
	// and verify from Steam directly. The choice is recorded in the log line
	// below either way.

	// The cache entry's identity is workshop_id + content_version only
	// (NFR1) -- resolve the addon that owns this workshop_id to get the
	// steam_app_id the host needs to actually run SteamCMD.
	addon, err := h.addonRepo.GetByWorkshopIDAnyGame(ctx, entry.WorkshopID)
	if err != nil {
		slog.Warn("failed to resolve addon for workshop cache verify", "cache_entry_id", req.CacheEntryId, "workshop_id", entry.WorkshopID, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to resolve addon for cache entry: %v", err)
	}
	if addon == nil || addon.SteamAppID == nil || *addon.SteamAppID == "" {
		slog.Warn("cannot resolve steam_app_id for workshop cache verify", "cache_entry_id", req.CacheEntryId, "workshop_id", entry.WorkshopID)
		return nil, status.Errorf(codes.FailedPrecondition, "no addon with a steam_app_id owns workshop_id %s", entry.WorkshopID)
	}

	cmd := &hostrmq.VerifyCacheEntryCommand{
		CacheEntryID:   entry.CacheEntryID,
		WorkshopID:     entry.WorkshopID,
		ContentVersion: entry.ContentVersion,
		SteamAppID:     *addon.SteamAppID,
	}
	routingKey := fmt.Sprintf("command.host.%d.workshop.cache_verify", serverID)
	if err := h.rmqPublisher.Publish(ctx, "manman", routingKey, cmd); err != nil {
		slog.Warn("failed to publish workshop cache verify command", "cache_entry_id", req.CacheEntryId, "server_id", serverID, "error", err)
		return nil, status.Errorf(codes.Internal, "failed to dispatch verify command: %v", err)
	}

	slog.Info("dispatched workshop cache verify command",
		"cache_entry_id", req.CacheEntryId, "workshop_id", entry.WorkshopID, "server_id", serverID, "explicit_server_id", req.ServerId != 0)

	return &pb.VerifyCacheEntryResponse{
		Dispatched: true,
		ServerId:   serverID,
		Status:     "dispatched",
	}, nil
}
