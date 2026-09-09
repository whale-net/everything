package workshop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/manmanv2/api/workshop"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
	manman "github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// --- fakes -------------------------------------------------------------

// presignCall records one call into fakePresigner, so tests can assert
// exactly what key/ttl a handler asked to be signed (NFR6).
type presignCall struct {
	key string
	ttl time.Duration
}

// deleteCall records one call into fakePresigner.Delete, so tests can assert
// exactly which key was deleted and that no other key was ever touched --
// the FR12 blast-radius assertion.
type deleteCall struct {
	key string
}

// fakePresigner is the cachePresigner test double the issue's Testing
// section asks for: it never touches real S3, records every call it
// receives, and can be told to fail on demand so the handler's error path
// is exercised without a real presign failure being reproducible. It also
// backs EvictCacheEntry's tests (FR12): deleteCalls records every Delete
// invocation exactly, and deleteErr/deleteNoSuchKey let a test drive the
// object-delete-fails and object-already-absent paths without a real S3
// error being reproducible.
type fakePresigner struct {
	mu sync.Mutex

	getCalls []presignCall
	putCalls []presignCall

	getErr error
	putErr error

	deleteCalls     []deleteCall
	deleteErr       error
	deleteNoSuchKey bool
}

// noSuchKeyErr is a fakePresigner.Delete failure that s3lib.IsNoSuchKey
// recognizes via its string-matching fallback (the same fallback the real
// libs/go/s3.IsNoSuchKey uses for S3-compatible endpoints that don't return
// a typed error), so tests can drive the "object already absent" path
// without depending on the real AWS SDK's typed NoSuchKey error.
var errSimulatedNoSuchKey = errors.New("simulated: NoSuchKey: the specified key does not exist")

func (f *fakePresigner) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls = append(f.deleteCalls, deleteCall{key: key})
	if f.deleteNoSuchKey {
		return errSimulatedNoSuchKey
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return nil
}

// PresignPublicGetURL implements cachePresigner's public-endpoint GET
// method (FR6/FR7/FR9: GetCacheDownloadURL must presign against the public
// endpoint host-manager can actually reach, not the internal-only
// PresignGetURL). Call-recording behavior is unchanged from the
// pre-rename PresignGetURL this replaces.
func (f *fakePresigner) PresignPublicGetURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, presignCall{key: key, ttl: ttl})
	if f.getErr != nil {
		return "", f.getErr
	}
	return fmt.Sprintf("https://cache-bucket.s3.example.com/%s?X-Amz-Signature=deadbeefdeadbeef&X-Amz-Expires=%d", key, int(ttl.Seconds())), nil
}

// PresignPublicPutURL implements cachePresigner's public-endpoint PUT
// method (FR6/FR7/FR9: GetCacheUploadURL must presign against the public
// endpoint host-manager can actually reach, not the internal-only
// PresignPutURL). Call-recording behavior is unchanged from the
// pre-rename PresignPutURL this replaces.
func (f *fakePresigner) PresignPublicPutURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCalls = append(f.putCalls, presignCall{key: key, ttl: ttl})
	if f.putErr != nil {
		return "", f.putErr
	}
	return fmt.Sprintf("https://cache-bucket.s3.example.com/%s?X-Amz-Signature=cafebabecafebabe&X-Amz-Expires=%d", key, int(ttl.Seconds())), nil
}

// fakeCacheRepo is a minimal, mutex-protected in-memory
// repository.WorkshopCacheRepository: enough to exercise
// GetCacheDownloadURL/GetCacheUploadURL, including the concurrent-upsert
// convergence NFR4 requires. Keyed on cache_key, matching the real
// repository's documented uniqueness (repository.go's
// WorkshopCacheRepository doc comment).
type fakeCacheRepo struct {
	mu          sync.Mutex
	entries     map[string]*manman.WorkshopCacheEntry
	nextID      int64
	upsertCalls int
	getErr      error
	upsertErr   error

	// listErr, when set, makes ListCacheEntriesForWorkshopID fail -- used by
	// ListAddonCacheEntries's Internal-on-list-failure path.
	listErr error

	// presenceByEntry/presenceErr/presenceCalls back
	// ListHostPresenceForCacheEntryIDs: presenceCalls lets tests assert the
	// FR10 batching contract (one call regardless of entry count) the way
	// the real repository's ANY($1) query does, rather than fanning out one
	// ListHostPresence call per entry.
	presenceByEntry map[int64][]*manman.WorkshopCacheHostPresenceWithServer
	presenceErr     error
	presenceCalls   int

	// hostPresence/hostPresenceErr back ListHostPresence, the single-entry
	// lookup VerifyCacheEntry (#2186) uses to pick a host when server_id==0.
	// Keyed on cache_entry_id, mirroring the real repository's contract.
	hostPresence    map[int64][]*manman.WorkshopCacheHostPresence
	hostPresenceErr error

	// deleteCacheEntryErr, when set, makes DeleteCacheEntry fail -- backs
	// EvictCacheEntry's "row delete fails after the object is already gone"
	// regression test.
	deleteCacheEntryErr error
}

func newFakeCacheRepo() *fakeCacheRepo {
	return &fakeCacheRepo{
		entries:         map[string]*manman.WorkshopCacheEntry{},
		presenceByEntry: map[int64][]*manman.WorkshopCacheHostPresenceWithServer{},
		hostPresence:    map[int64][]*manman.WorkshopCacheHostPresence{},
	}
}

func (f *fakeCacheRepo) seed(entry *manman.WorkshopCacheEntry) *manman.WorkshopCacheEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	cp := *entry
	cp.CacheEntryID = f.nextID
	f.entries[cp.CacheKey] = &cp
	out := cp
	return &out
}

func (f *fakeCacheRepo) GetCacheEntryByKey(_ context.Context, cacheKey string) (*manman.WorkshopCacheEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	e, ok := f.entries[cacheKey]
	if !ok {
		return nil, nil
	}
	cp := *e
	return &cp, nil
}

// UpsertCacheEntry mirrors the real repository's documented contract
// (repository.go): idempotent insert-or-return-existing keyed on cache_key,
// so two racing callers converge on the same cache_entry_id/s3_key (NFR4).
func (f *fakeCacheRepo) UpsertCacheEntry(_ context.Context, entry *manman.WorkshopCacheEntry) (*manman.WorkshopCacheEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertCalls++
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	if existing, ok := f.entries[entry.CacheKey]; ok {
		cp := *existing
		return &cp, nil
	}
	f.nextID++
	cp := *entry
	cp.CacheEntryID = f.nextID
	f.entries[cp.CacheKey] = &cp
	out := cp
	return &out, nil
}

// ListCacheEntriesForWorkshopID mirrors the real repository's documented
// contract (workshop_cache.go): every entry for workshopID, newest-first by
// CreatedAt (cache_entry_id descending as the tie-break), matching
// ListCacheEntriesForWorkshopID's ORDER BY created_at DESC, cache_entry_id
// DESC exactly.
func (f *fakeCacheRepo) ListCacheEntriesForWorkshopID(_ context.Context, workshopID string) ([]*manman.WorkshopCacheEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []*manman.WorkshopCacheEntry
	for _, e := range f.entries {
		if e.WorkshopID == workshopID {
			cp := *e
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].CacheEntryID > out[j].CacheEntryID
	})
	return out, nil
}

func (f *fakeCacheRepo) GetCacheEntry(_ context.Context, cacheEntryID int64) (*manman.WorkshopCacheEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.entries {
		if e.CacheEntryID == cacheEntryID {
			cp := *e
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeCacheRepo) TouchCacheEntryVerified(_ context.Context, _ int64, _ time.Time) error {
	return nil
}

// DeleteCacheEntry mirrors the real repository's row delete plus the
// ON DELETE CASCADE on workshop_cache_host_presence (migration 040): removing
// an entry also removes its presence rows here, so
// TestEvictCacheEntry_RemovesOnlyItsOwnPresenceRows can assert sibling
// entries' presence survives untouched.
func (f *fakeCacheRepo) DeleteCacheEntry(_ context.Context, cacheEntryID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteCacheEntryErr != nil {
		return f.deleteCacheEntryErr
	}
	for k, e := range f.entries {
		if e.CacheEntryID == cacheEntryID {
			delete(f.entries, k)
			delete(f.presenceByEntry, cacheEntryID)
			return nil
		}
	}
	return nil
}

func (f *fakeCacheRepo) UpsertHostPresence(_ context.Context, _, _ int64) error {
	return nil
}

// ListHostPresence backs VerifyCacheEntry's (#2186) server_id==0 host
// selection: every server currently known to hold a copy of cacheEntryID.
func (f *fakeCacheRepo) ListHostPresence(_ context.Context, cacheEntryID int64) ([]*manman.WorkshopCacheHostPresence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hostPresenceErr != nil {
		return nil, f.hostPresenceErr
	}
	return f.hostPresence[cacheEntryID], nil
}

// ListHostPresenceForCacheEntryIDs is the fake's counterpart to the real
// repository's batched FR10 lookup: it records exactly how many times it
// was invoked (presenceCalls) so TestListAddonCacheEntries_PresenceLookupIsBatched
// can assert the handler makes one call regardless of how many entries it's
// listing -- looping ListHostPresence per entry here would be exactly the
// per-host, per-entry fan-out FR10 rules out.
func (f *fakeCacheRepo) ListHostPresenceForCacheEntryIDs(_ context.Context, cacheEntryIDs []int64) (map[int64][]*manman.WorkshopCacheHostPresenceWithServer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presenceCalls++
	if f.presenceErr != nil {
		return nil, f.presenceErr
	}
	result := make(map[int64][]*manman.WorkshopCacheHostPresenceWithServer)
	for _, id := range cacheEntryIDs {
		if presence, ok := f.presenceByEntry[id]; ok {
			result[id] = presence
		}
	}
	return result, nil
}

func (f *fakeCacheRepo) entryCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

// --- test helpers --------------------------------------------------------

// hostCtx builds a context carrying claims exactly as the real gRPC
// interceptor would (grpcauth.ContextWithClaims), with Subject set to the
// numeric server_id the request claims to be -- see requireHostIdentity's
// doc comment on why a numeric Subject is treated as a claimed server_id.
func hostCtx(serverID int64) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{
		Subject: strconv.FormatInt(serverID, 10),
	})
}

// captureLogs redirects the package-level slog default logger to an
// in-memory buffer for the duration of fn, then restores the prior default.
// cache.go logs through slog.Info/slog.Warn directly (not an injected
// logger), so this is the only way to observe what actually reached a log
// sink -- exactly what the NFR6 "never logs the URL" tests need to assert
// against.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

const testServerID = int64(42)

func newTestHandler(repo *fakeCacheRepo, presigner *fakePresigner) *WorkshopServiceHandler {
	return &WorkshopServiceHandler{
		cacheRepo: repo,
		s3Client:  presigner,
	}
}

// --- GetCacheDownloadURL --------------------------------------------------

func TestGetCacheDownloadURL_CacheHit(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	cacheKey := workshop.CacheKey("123456", "789")
	s3Key := workshop.S3Key(cacheKey)
	sizeBytes := int64(1024)
	seeded := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID:     "123456",
		ContentVersion: "789",
		CacheKey:       cacheKey,
		S3Key:          s3Key,
		SizeBytes:      &sizeBytes,
	})

	h := newTestHandler(repo, presigner)
	before := time.Now()
	resp, err := h.GetCacheDownloadURL(hostCtx(testServerID), &pb.GetCacheDownloadURLRequest{
		ServerId:       testServerID,
		WorkshopId:     "123456",
		ContentVersion: "789",
	})
	if err != nil {
		t.Fatalf("GetCacheDownloadURL: unexpected error: %v", err)
	}
	if !resp.CacheHit {
		t.Fatal("CacheHit = false, want true")
	}
	if resp.CacheEntryId != seeded.CacheEntryID {
		t.Errorf("CacheEntryId = %d, want %d", resp.CacheEntryId, seeded.CacheEntryID)
	}
	if resp.PresignedUrl == "" {
		t.Error("PresignedUrl is empty, want a URL on a cache hit")
	}
	if resp.SizeBytes != sizeBytes {
		t.Errorf("SizeBytes = %d, want %d", resp.SizeBytes, sizeBytes)
	}

	expiresAt := time.Unix(resp.ExpiresAt, 0)
	if expiresAt.Before(before) || expiresAt.After(before.Add(cacheURLTTL+time.Second)) {
		t.Errorf("ExpiresAt = %v, want within %v of %v", expiresAt, cacheURLTTL, before)
	}
}

// TestGetCacheDownloadURL_UsesPublicEndpointPresign pins the FR6/FR7/FR9
// regression this issue exists to fix: GetCacheDownloadURL must invoke the
// presigner's public-endpoint GET method (PresignPublicGetURL), the one
// signed against Config.PublicEndpoint that a bare-metal host-manager can
// actually reach, and not the internal-only PresignGetURL that
// fakePresigner no longer even implements. Asserting getCalls was
// incremented exactly once is only meaningful because fakePresigner has no
// PresignGetURL method left to satisfy the old interface -- if the handler
// called anything other than PresignPublicGetURL, this package would fail
// to compile rather than this test merely failing, which is the point: the
// fix is pinned by name, not just incidentally covered by the fake's
// rename.
func TestGetCacheDownloadURL_UsesPublicEndpointPresign(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	cacheKey := workshop.CacheKey("246810", "1")
	s3Key := workshop.S3Key(cacheKey)
	repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "246810", ContentVersion: "1", CacheKey: cacheKey, S3Key: s3Key,
	})
	h := newTestHandler(repo, presigner)

	resp, err := h.GetCacheDownloadURL(hostCtx(testServerID), &pb.GetCacheDownloadURLRequest{
		ServerId: testServerID, WorkshopId: "246810", ContentVersion: "1",
	})
	if err != nil {
		t.Fatalf("GetCacheDownloadURL: unexpected error: %v", err)
	}
	if resp.PresignedUrl == "" {
		t.Fatal("PresignedUrl is empty, want a URL on a cache hit")
	}
	if len(presigner.getCalls) != 1 {
		t.Fatalf("PresignPublicGetURL was called %d times, want exactly 1", len(presigner.getCalls))
	}
	if got := presigner.getCalls[0].key; got != s3Key {
		t.Errorf("PresignPublicGetURL key = %q, want %q", got, s3Key)
	}
	if len(presigner.putCalls) != 0 {
		t.Errorf("PresignPublicPutURL was called %d times on a download, want 0", len(presigner.putCalls))
	}
}

func TestGetCacheDownloadURL_CacheMiss(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	h := newTestHandler(repo, presigner)

	resp, err := h.GetCacheDownloadURL(hostCtx(testServerID), &pb.GetCacheDownloadURLRequest{
		ServerId:       testServerID,
		WorkshopId:     "does-not-exist",
		ContentVersion: "1",
	})
	if err != nil {
		t.Fatalf("GetCacheDownloadURL: unexpected error on a miss: %v", err)
	}
	if resp.CacheHit {
		t.Error("CacheHit = true, want false on a miss")
	}
	if resp.PresignedUrl != "" {
		t.Errorf("PresignedUrl = %q, want empty on a miss", resp.PresignedUrl)
	}
	if len(presigner.getCalls) != 0 {
		t.Errorf("presigner was called %d times on a miss, want 0", len(presigner.getCalls))
	}
}

// --- GetCacheUploadURL -----------------------------------------------------

func TestGetCacheUploadURL_NewKey(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	h := newTestHandler(repo, presigner)

	resp, err := h.GetCacheUploadURL(hostCtx(testServerID), &pb.GetCacheUploadURLRequest{
		ServerId:       testServerID,
		WorkshopId:     "555",
		ContentVersion: "3",
	})
	if err != nil {
		t.Fatalf("GetCacheUploadURL: unexpected error: %v", err)
	}
	if resp.PresignedUrl == "" {
		t.Error("PresignedUrl is empty, want a PUT URL")
	}
	if resp.CacheEntryId == 0 {
		t.Error("CacheEntryId is 0, want a created entry id")
	}
	wantCacheKey := workshop.CacheKey("555", "3")
	wantS3Key := workshop.S3Key(wantCacheKey)
	if resp.S3Key != wantS3Key {
		t.Errorf("S3Key = %q, want %q", resp.S3Key, wantS3Key)
	}
	if repo.entryCount() != 1 {
		t.Errorf("entryCount = %d, want 1 (entry should have been created)", repo.entryCount())
	}
}

// TestGetCacheUploadURL_UsesPublicEndpointPresign pins the FR6/FR7/FR9
// regression this issue exists to fix: GetCacheUploadURL must invoke the
// presigner's public-endpoint PUT method (PresignPublicPutURL) -- the one
// signed against Config.PublicEndpoint a bare-metal host-manager can
// actually reach -- and not the internal-only PresignPutURL that
// fakePresigner no longer implements. See
// TestGetCacheDownloadURL_UsesPublicEndpointPresign's doc comment for why
// this is pinned by name rather than left to the rename alone.
func TestGetCacheUploadURL_UsesPublicEndpointPresign(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	h := newTestHandler(repo, presigner)

	resp, err := h.GetCacheUploadURL(hostCtx(testServerID), &pb.GetCacheUploadURLRequest{
		ServerId: testServerID, WorkshopId: "13579", ContentVersion: "2",
	})
	if err != nil {
		t.Fatalf("GetCacheUploadURL: unexpected error: %v", err)
	}
	if resp.PresignedUrl == "" {
		t.Fatal("PresignedUrl is empty, want a PUT URL")
	}
	if len(presigner.putCalls) != 1 {
		t.Fatalf("PresignPublicPutURL was called %d times, want exactly 1", len(presigner.putCalls))
	}
	if got := presigner.putCalls[0].key; got != resp.S3Key {
		t.Errorf("PresignPublicPutURL key = %q, want %q", got, resp.S3Key)
	}
	if len(presigner.getCalls) != 0 {
		t.Errorf("PresignPublicGetURL was called %d times on an upload, want 0", len(presigner.getCalls))
	}
}

// TestGetCacheUploadURL_ConcurrentSameKey is the NFR4 regression: two hosts
// racing to cache the same (workshop_id, content_version) must converge on
// one cache_entry_id/s3_key rather than erroring or diverging, since both
// write identical content to the same object (no distributed lock, LB8).
func TestGetCacheUploadURL_ConcurrentSameKey(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	h := newTestHandler(repo, presigner)

	req := &pb.GetCacheUploadURLRequest{
		ServerId:       testServerID,
		WorkshopId:     "999",
		ContentVersion: "7",
	}

	const n = 8
	respCh := make(chan *pb.GetCacheUploadURLResponse, n)
	errCh := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := h.GetCacheUploadURL(hostCtx(testServerID), req)
			respCh <- resp
			errCh <- err
		}()
	}
	wg.Wait()
	close(respCh)
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("GetCacheUploadURL: unexpected error under concurrency: %v", err)
		}
	}

	var wantID int64
	var wantS3Key string
	first := true
	for resp := range respCh {
		if first {
			wantID = resp.CacheEntryId
			wantS3Key = resp.S3Key
			first = false
			continue
		}
		if resp.CacheEntryId != wantID {
			t.Errorf("CacheEntryId = %d, want %d (all concurrent callers must converge)", resp.CacheEntryId, wantID)
		}
		if resp.S3Key != wantS3Key {
			t.Errorf("S3Key = %q, want %q (all concurrent callers must converge)", resp.S3Key, wantS3Key)
		}
	}

	if repo.entryCount() != 1 {
		t.Errorf("entryCount = %d, want 1 (concurrent upserts of the same key must not create duplicates)", repo.entryCount())
	}
}

// --- NFR6 regression tests -------------------------------------------------

// TestNFR6_TTLNeverExceedsFiveMinutes pins cacheURLTTL itself and asserts
// both RPCs actually hand that TTL (not some other value) to the presigner
// -- NFR6 requires minutes-scale expiry, explicitly not the backup flow's
// 1-hour precedent.
func TestNFR6_TTLNeverExceedsFiveMinutes(t *testing.T) {
	if cacheURLTTL > 5*time.Minute {
		t.Fatalf("cacheURLTTL = %v, must be <= 5 minutes (NFR6)", cacheURLTTL)
	}

	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	cacheKey := workshop.CacheKey("111", "1")
	repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "111", ContentVersion: "1", CacheKey: cacheKey, S3Key: workshop.S3Key(cacheKey),
	})
	h := newTestHandler(repo, presigner)

	if _, err := h.GetCacheDownloadURL(hostCtx(testServerID), &pb.GetCacheDownloadURLRequest{
		ServerId: testServerID, WorkshopId: "111", ContentVersion: "1",
	}); err != nil {
		t.Fatalf("GetCacheDownloadURL: %v", err)
	}
	if _, err := h.GetCacheUploadURL(hostCtx(testServerID), &pb.GetCacheUploadURLRequest{
		ServerId: testServerID, WorkshopId: "222", ContentVersion: "1",
	}); err != nil {
		t.Fatalf("GetCacheUploadURL: %v", err)
	}

	if len(presigner.getCalls) != 1 {
		t.Fatalf("expected exactly 1 GET presign call, got %d", len(presigner.getCalls))
	}
	if got := presigner.getCalls[0].ttl; got > 5*time.Minute {
		t.Errorf("GET TTL = %v, want <= 5 minutes", got)
	}
	if len(presigner.putCalls) != 1 {
		t.Fatalf("expected exactly 1 PUT presign call, got %d", len(presigner.putCalls))
	}
	if got := presigner.putCalls[0].ttl; got > 5*time.Minute {
		t.Errorf("PUT TTL = %v, want <= 5 minutes", got)
	}
}

// TestNFR6_PresignedExactSingleObjectKey asserts the presigner is invoked
// with the exact object key for the entry -- never a prefix, glob, or
// bucket-wide wildcard (NFR6: "Never presign a prefix, never issue a
// bucket-wide or list-capable URL").
func TestNFR6_PresignedExactSingleObjectKey(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	cacheKey := workshop.CacheKey("321", "9")
	s3Key := workshop.S3Key(cacheKey)
	entry := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "321", ContentVersion: "9", CacheKey: cacheKey, S3Key: s3Key,
	})
	h := newTestHandler(repo, presigner)

	if _, err := h.GetCacheDownloadURL(hostCtx(testServerID), &pb.GetCacheDownloadURLRequest{
		ServerId: testServerID, WorkshopId: entry.WorkshopID, ContentVersion: entry.ContentVersion,
	}); err != nil {
		t.Fatalf("GetCacheDownloadURL: %v", err)
	}
	if len(presigner.getCalls) != 1 {
		t.Fatalf("expected exactly 1 GET presign call, got %d", len(presigner.getCalls))
	}
	if got := presigner.getCalls[0].key; got != s3Key {
		t.Errorf("GET presign key = %q, want exact key %q", got, s3Key)
	}
	if strings.HasSuffix(presigner.getCalls[0].key, "/") || strings.Contains(presigner.getCalls[0].key, "*") {
		t.Errorf("GET presign key %q looks like a prefix/wildcard, want a single object key", presigner.getCalls[0].key)
	}

	uploadResp, err := h.GetCacheUploadURL(hostCtx(testServerID), &pb.GetCacheUploadURLRequest{
		ServerId: testServerID, WorkshopId: "654", ContentVersion: "2",
	})
	if err != nil {
		t.Fatalf("GetCacheUploadURL: %v", err)
	}
	if len(presigner.putCalls) != 1 {
		t.Fatalf("expected exactly 1 PUT presign call, got %d", len(presigner.putCalls))
	}
	if got := presigner.putCalls[0].key; got != uploadResp.S3Key {
		t.Errorf("PUT presign key = %q, want exact key %q", got, uploadResp.S3Key)
	}
	if strings.HasSuffix(presigner.putCalls[0].key, "/") || strings.Contains(presigner.putCalls[0].key, "*") {
		t.Errorf("PUT presign key %q looks like a prefix/wildcard, want a single object key", presigner.putCalls[0].key)
	}
}

// TestNFR6_URLNeverReachesLogs is the security-load-bearing regression the
// issue calls out by name: a log capture of both RPCs must not contain any
// substring of the presigned URL they return, at any log level. A leaked
// query string alone (the signature) would be enough to grant object
// access for the URL's lifetime, so this checks the full URL string, not
// just its host/path.
func TestNFR6_URLNeverReachesLogs(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	cacheKey := workshop.CacheKey("777", "1")
	repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "777", ContentVersion: "1", CacheKey: cacheKey, S3Key: workshop.S3Key(cacheKey),
	})
	h := newTestHandler(repo, presigner)

	var downloadURL, uploadURL string
	logs := captureLogs(t, func() {
		resp, err := h.GetCacheDownloadURL(hostCtx(testServerID), &pb.GetCacheDownloadURLRequest{
			ServerId: testServerID, WorkshopId: "777", ContentVersion: "1",
		})
		if err != nil {
			t.Fatalf("GetCacheDownloadURL: %v", err)
		}
		downloadURL = resp.PresignedUrl

		uploadResp, err := h.GetCacheUploadURL(hostCtx(testServerID), &pb.GetCacheUploadURLRequest{
			ServerId: testServerID, WorkshopId: "888", ContentVersion: "1",
		})
		if err != nil {
			t.Fatalf("GetCacheUploadURL: %v", err)
		}
		uploadURL = uploadResp.PresignedUrl
	})

	if downloadURL == "" || uploadURL == "" {
		t.Fatal("test setup produced an empty presigned URL, cannot validate the log assertion")
	}
	if strings.Contains(logs, downloadURL) {
		t.Errorf("log output contains the full download presigned URL:\n%s", logs)
	}
	if strings.Contains(logs, uploadURL) {
		t.Errorf("log output contains the full upload presigned URL:\n%s", logs)
	}
	// The signature/query-string alone is the actually dangerous leak --
	// check for it explicitly in addition to the full URL, since a
	// half-truncated log line could contain the query without the whole URL
	// string matching.
	if i := strings.Index(downloadURL, "?"); i != -1 && strings.Contains(logs, downloadURL[i:]) {
		t.Errorf("log output contains the download URL's query string:\n%s", logs)
	}
	if i := strings.Index(uploadURL, "?"); i != -1 && strings.Contains(logs, uploadURL[i:]) {
		t.Errorf("log output contains the upload URL's query string:\n%s", logs)
	}
}

// --- authentication / authorization ---------------------------------------

func TestGetCacheDownloadURL_Unauthenticated(t *testing.T) {
	repo := newFakeCacheRepo()
	h := newTestHandler(repo, &fakePresigner{})

	_, err := h.GetCacheDownloadURL(context.Background(), &pb.GetCacheDownloadURLRequest{
		ServerId: testServerID, WorkshopId: "1", ContentVersion: "1",
	})
	assertCode(t, err, codes.Unauthenticated)
}

func TestGetCacheDownloadURL_ServerIDMismatch(t *testing.T) {
	repo := newFakeCacheRepo()
	h := newTestHandler(repo, &fakePresigner{})

	_, err := h.GetCacheDownloadURL(hostCtx(testServerID), &pb.GetCacheDownloadURLRequest{
		ServerId: testServerID + 1, WorkshopId: "1", ContentVersion: "1",
	})
	assertCode(t, err, codes.PermissionDenied)
}

func TestGetCacheUploadURL_Unauthenticated(t *testing.T) {
	repo := newFakeCacheRepo()
	h := newTestHandler(repo, &fakePresigner{})

	_, err := h.GetCacheUploadURL(context.Background(), &pb.GetCacheUploadURLRequest{
		ServerId: testServerID, WorkshopId: "1", ContentVersion: "1",
	})
	assertCode(t, err, codes.Unauthenticated)
}

func TestGetCacheUploadURL_ServerIDMismatch(t *testing.T) {
	repo := newFakeCacheRepo()
	h := newTestHandler(repo, &fakePresigner{})

	_, err := h.GetCacheUploadURL(hostCtx(testServerID), &pb.GetCacheUploadURLRequest{
		ServerId: testServerID + 1, WorkshopId: "1", ContentVersion: "1",
	})
	assertCode(t, err, codes.PermissionDenied)
}

// --- presigner failure -----------------------------------------------------

// TestGetCacheDownloadURL_PresignerFailure covers the download path's
// presigner-failure contract: codes.Internal, and -- because the download
// path only ever reads -- no cache-entry row is created as a side effect of
// the failed attempt (there is nothing here that could leave a "partial"
// row behind, and this test pins that).
func TestGetCacheDownloadURL_PresignerFailure(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{getErr: errors.New("simulated presign failure")}
	cacheKey := workshop.CacheKey("321", "1")
	repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "321", ContentVersion: "1", CacheKey: cacheKey, S3Key: workshop.S3Key(cacheKey),
	})
	h := newTestHandler(repo, presigner)

	before := repo.entryCount()
	_, err := h.GetCacheDownloadURL(hostCtx(testServerID), &pb.GetCacheDownloadURLRequest{
		ServerId: testServerID, WorkshopId: "321", ContentVersion: "1",
	})
	assertCode(t, err, codes.Internal)
	if repo.upsertCalls != 0 {
		t.Errorf("UpsertCacheEntry was called %d times on the read-only download path, want 0", repo.upsertCalls)
	}
	if after := repo.entryCount(); after != before {
		t.Errorf("entryCount changed from %d to %d after a presigner failure, want no partial row left behind", before, after)
	}
}

func TestGetCacheUploadURL_PresignerFailure(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{putErr: errors.New("simulated presign failure")}
	h := newTestHandler(repo, presigner)

	_, err := h.GetCacheUploadURL(hostCtx(testServerID), &pb.GetCacheUploadURLRequest{
		ServerId: testServerID, WorkshopId: "1", ContentVersion: "1",
	})
	assertCode(t, err, codes.Internal)
}

// --- ListAddonCacheEntries (FR10) -------------------------------------------

// newCacheListHandler builds a handler wired with the given cacheRepo and
// addonRepo -- the two collaborators ListAddonCacheEntries's addon_id ->
// workshop_id resolution plus batched entries/presence lookup depends on.
func newCacheListHandler(repo *fakeCacheRepo, addonRepo *MockWorkshopAddonRepository) *WorkshopServiceHandler {
	return &WorkshopServiceHandler{
		cacheRepo: repo,
		addonRepo: addonRepo,
	}
}

// TestListAddonCacheEntries_MultipleVersionsNewestFirstWithHostSets is the
// issue's core FR10 assertion: an addon with more than one cached version
// gets every version back, newest-first, each carrying only its own host
// set -- not the union across entries and not another entry's hosts.
func TestListAddonCacheEntries_MultipleVersionsNewestFirstWithHostSets(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	addonRepo.On("Get", mock.Anything, int64(42)).Return(&manman.WorkshopAddon{
		AddonID:    42,
		WorkshopID: "123456",
	}, nil)

	older := time.Now().Add(-48 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	sizeV1 := int64(1024)

	v1 := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "123456", ContentVersion: "v1",
		CacheKey: "ws/123456/v1", S3Key: "workshop-cache/123456/v1.tar",
		SizeBytes: &sizeV1, CreatedAt: older,
	})
	v2 := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "123456", ContentVersion: "v2",
		CacheKey: "ws/123456/v2", S3Key: "workshop-cache/123456/v2.tar",
		CreatedAt: newer,
	})

	repo.presenceByEntry[v1.CacheEntryID] = []*manman.WorkshopCacheHostPresenceWithServer{
		{CacheEntryID: v1.CacheEntryID, ServerID: 1, ServerName: "host-a"},
	}
	repo.presenceByEntry[v2.CacheEntryID] = []*manman.WorkshopCacheHostPresenceWithServer{
		{CacheEntryID: v2.CacheEntryID, ServerID: 2, ServerName: "host-b"},
		{CacheEntryID: v2.CacheEntryID, ServerID: 3, ServerName: "host-c"},
	}

	h := newCacheListHandler(repo, addonRepo)
	resp, err := h.ListAddonCacheEntries(context.Background(), &pb.ListAddonCacheEntriesRequest{AddonId: 42})
	if err != nil {
		t.Fatalf("ListAddonCacheEntries: unexpected error: %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(resp.Entries))
	}

	if resp.Entries[0].ContentVersion != "v2" {
		t.Errorf("Entries[0].ContentVersion = %q, want %q (newest first)", resp.Entries[0].ContentVersion, "v2")
	}
	if resp.Entries[1].ContentVersion != "v1" {
		t.Errorf("Entries[1].ContentVersion = %q, want %q", resp.Entries[1].ContentVersion, "v1")
	}
	if resp.Entries[1].SizeBytes != sizeV1 {
		t.Errorf("Entries[1].SizeBytes = %d, want %d", resp.Entries[1].SizeBytes, sizeV1)
	}

	if len(resp.Entries[0].Hosts) != 2 {
		t.Fatalf("expected v2 to carry exactly its own 2 hosts, got %d", len(resp.Entries[0].Hosts))
	}
	for _, host := range resp.Entries[0].Hosts {
		if host.ServerName == "host-a" {
			t.Errorf("v2's hosts must not include v1's host-a, got hosts: %+v", resp.Entries[0].Hosts)
		}
	}
	if len(resp.Entries[1].Hosts) != 1 || resp.Entries[1].Hosts[0].ServerName != "host-a" {
		t.Errorf("expected v1's hosts to be exactly [host-a], got %+v", resp.Entries[1].Hosts)
	}

	addonRepo.AssertExpectations(t)
}

// TestListAddonCacheEntries_NoCacheEntries_EmptyListNoError proves an addon
// that resolves fine but has never been cached returns an empty list, not
// an error -- the issue's explicit non-error-on-uncached-addon contract.
func TestListAddonCacheEntries_NoCacheEntries_EmptyListNoError(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	addonRepo.On("Get", mock.Anything, int64(7)).Return(&manman.WorkshopAddon{
		AddonID:    7,
		WorkshopID: "no-cache-entries",
	}, nil)

	h := newCacheListHandler(repo, addonRepo)
	resp, err := h.ListAddonCacheEntries(context.Background(), &pb.ListAddonCacheEntriesRequest{AddonId: 7})
	if err != nil {
		t.Fatalf("ListAddonCacheEntries: unexpected error for an uncached addon: %v", err)
	}
	if resp.Entries == nil {
		t.Error("Entries is nil, want a non-nil empty slice")
	}
	if len(resp.Entries) != 0 {
		t.Errorf("Entries has %d items, want 0", len(resp.Entries))
	}
	// The empty-list path must not even attempt the presence lookup -- there
	// are no cache_entry_ids to look up.
	if repo.presenceCalls != 0 {
		t.Errorf("ListHostPresenceForCacheEntryIDs was called %d times for an uncached addon, want 0", repo.presenceCalls)
	}

	addonRepo.AssertExpectations(t)
}

// TestListAddonCacheEntries_UnknownAddonID_NotFound proves an addon_id that
// doesn't resolve surfaces as codes.NotFound, and never reaches the cache
// repository at all.
func TestListAddonCacheEntries_UnknownAddonID_NotFound(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	addonRepo.On("Get", mock.Anything, int64(999)).Return(nil, assert.AnError)

	h := newCacheListHandler(repo, addonRepo)
	_, err := h.ListAddonCacheEntries(context.Background(), &pb.ListAddonCacheEntriesRequest{AddonId: 999})
	assertCode(t, err, codes.NotFound)

	if repo.presenceCalls != 0 {
		t.Errorf("ListHostPresenceForCacheEntryIDs was called %d times for an unresolvable addon, want 0", repo.presenceCalls)
	}
	addonRepo.AssertExpectations(t)
}

// TestListAddonCacheEntries_MissingAddonID_InvalidArgument guards the
// request-validation half of the RPC, same convention as every other
// handler's `_id is required` check in this package.
func TestListAddonCacheEntries_MissingAddonID_InvalidArgument(t *testing.T) {
	h := newCacheListHandler(newFakeCacheRepo(), new(MockWorkshopAddonRepository))
	_, err := h.ListAddonCacheEntries(context.Background(), &pb.ListAddonCacheEntriesRequest{})
	assertCode(t, err, codes.InvalidArgument)
}

// TestListAddonCacheEntries_PresenceLookupIsBatched is the fan-out
// regression the issue names explicitly: the presence lookup's call count
// must not scale with the number of cache entries being listed -- one
// batched call for N entries, never N calls.
func TestListAddonCacheEntries_PresenceLookupIsBatched(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	addonRepo.On("Get", mock.Anything, int64(1)).Return(&manman.WorkshopAddon{
		AddonID:    1,
		WorkshopID: "long-history",
	}, nil)

	const numEntries = 10
	for i := 0; i < numEntries; i++ {
		repo.seed(&manman.WorkshopCacheEntry{
			WorkshopID:     "long-history",
			ContentVersion: fmt.Sprintf("v%d", i),
			CacheKey:       fmt.Sprintf("ws/long-history/v%d", i),
			S3Key:          fmt.Sprintf("workshop-cache/long-history/v%d.tar", i),
			CreatedAt:      time.Now().Add(time.Duration(-i) * time.Hour),
		})
	}

	h := newCacheListHandler(repo, addonRepo)
	resp, err := h.ListAddonCacheEntries(context.Background(), &pb.ListAddonCacheEntriesRequest{AddonId: 1})
	if err != nil {
		t.Fatalf("ListAddonCacheEntries: unexpected error: %v", err)
	}
	if len(resp.Entries) != numEntries {
		t.Fatalf("expected %d entries, got %d", numEntries, len(resp.Entries))
	}
	if repo.presenceCalls != 1 {
		t.Errorf("ListHostPresenceForCacheEntryIDs was called %d times for %d entries, want exactly 1 (batched, not per-entry)", repo.presenceCalls, numEntries)
	}
}

// TestListAddonCacheEntries_ListFailure_Internal and
// TestListAddonCacheEntries_PresenceFailure_Internal cover the two
// repository-error paths: a failure listing entries, and a failure listing
// their presence, both surface as codes.Internal rather than a partial or
// panicking response.
func TestListAddonCacheEntries_ListFailure_Internal(t *testing.T) {
	repo := newFakeCacheRepo()
	repo.listErr = errors.New("simulated list failure")
	addonRepo := new(MockWorkshopAddonRepository)
	addonRepo.On("Get", mock.Anything, int64(1)).Return(&manman.WorkshopAddon{AddonID: 1, WorkshopID: "123"}, nil)

	h := newCacheListHandler(repo, addonRepo)
	_, err := h.ListAddonCacheEntries(context.Background(), &pb.ListAddonCacheEntriesRequest{AddonId: 1})
	assertCode(t, err, codes.Internal)
}

func TestListAddonCacheEntries_PresenceFailure_Internal(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	addonRepo.On("Get", mock.Anything, int64(1)).Return(&manman.WorkshopAddon{AddonID: 1, WorkshopID: "123"}, nil)
	repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "123", ContentVersion: "1", CacheKey: "ws/123/1", S3Key: "workshop-cache/123/1.tar",
	})
	repo.presenceErr = errors.New("simulated presence failure")

	h := newCacheListHandler(repo, addonRepo)
	_, err := h.ListAddonCacheEntries(context.Background(), &pb.ListAddonCacheEntriesRequest{AddonId: 1})
	assertCode(t, err, codes.Internal)
}

// --- VerifyCacheEntry (FR11) -------------------------------------------------

// publishedCommand records one call into fakeRMQPublisher, so tests can
// assert the exact exchange/routing key/body VerifyCacheEntry dispatched --
// the issue's "publishes on the exact new routing key" and "nothing
// published" assertions both depend on being able to observe this precisely.
type publishedCommand struct {
	exchange   string
	routingKey string
	body       interface{}
}

// fakeRMQPublisher is a minimal workshop.RMQPublisher test double: it never
// touches a real broker, records every Publish call it receives, and can be
// told to fail on demand so VerifyCacheEntry's dispatch-failure path is
// exercised without a real broker outage being reproducible.
type fakeRMQPublisher struct {
	mu         sync.Mutex
	calls      []publishedCommand
	publishErr error
}

func (f *fakeRMQPublisher) Publish(_ context.Context, exchange, routingKey string, body interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishErr != nil {
		return f.publishErr
	}
	f.calls = append(f.calls, publishedCommand{exchange: exchange, routingKey: routingKey, body: body})
	return nil
}

func (f *fakeRMQPublisher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newVerifyHandler builds a handler wired with the collaborators
// VerifyCacheEntry depends on: cacheRepo (entry lookup + host presence),
// addonRepo (workshop_id -> steam_app_id resolution), and rmqPublisher (the
// actual dispatch).
func newVerifyHandler(repo *fakeCacheRepo, addonRepo *MockWorkshopAddonRepository, publisher *fakeRMQPublisher) *WorkshopServiceHandler {
	return &WorkshopServiceHandler{
		cacheRepo:    repo,
		addonRepo:    addonRepo,
		rmqPublisher: publisher,
	}
}

const testSteamAppID = "440900"

// seedVerifyAddon wires addonRepo to resolve workshopID to an addon carrying
// testSteamAppID, the steam_app_id VerifyCacheEntry needs to actually run
// SteamCMD on the host.
func seedVerifyAddon(addonRepo *MockWorkshopAddonRepository, workshopID string) {
	steamAppID := testSteamAppID
	addonRepo.On("GetByWorkshopIDAnyGame", mock.Anything, workshopID).Return(&manman.WorkshopAddonWithGame{
		WorkshopAddon: manman.WorkshopAddon{WorkshopID: workshopID},
		SteamAppID:    &steamAppID,
	}, nil)
}

// TestVerifyCacheEntry_ServerIDZero_PicksMostRecentlySeenHost is the issue's
// core host-selection assertion: with no explicit server_id, the handler
// dispatches to whichever host currently holding a copy was seen most
// recently, publishing the command on that host's exact routing key with the
// entry's workshop_id/content_version carried through.
func TestVerifyCacheEntry_ServerIDZero_PicksMostRecentlySeenHost(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	publisher := &fakeRMQPublisher{}

	entry := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "123456", ContentVersion: "v3",
		CacheKey: "ws/123456/v3", S3Key: "workshop-cache/123456/v3.tar",
	})
	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-1 * time.Minute)
	repo.hostPresence[entry.CacheEntryID] = []*manman.WorkshopCacheHostPresence{
		{CacheEntryID: entry.CacheEntryID, ServerID: 1, LastSeenAt: older},
		{CacheEntryID: entry.CacheEntryID, ServerID: 2, LastSeenAt: newer},
	}
	seedVerifyAddon(addonRepo, entry.WorkshopID)

	h := newVerifyHandler(repo, addonRepo, publisher)
	resp, err := h.VerifyCacheEntry(context.Background(), &pb.VerifyCacheEntryRequest{CacheEntryId: entry.CacheEntryID})
	if err != nil {
		t.Fatalf("VerifyCacheEntry: unexpected error: %v", err)
	}
	if !resp.Dispatched {
		t.Error("Dispatched = false, want true")
	}
	if resp.Status != "dispatched" {
		t.Errorf("Status = %q, want %q", resp.Status, "dispatched")
	}
	if resp.ServerId != 2 {
		t.Errorf("ServerId = %d, want 2 (most recently seen host)", resp.ServerId)
	}

	if publisher.callCount() != 1 {
		t.Fatalf("expected exactly 1 publish call, got %d", publisher.callCount())
	}
	call := publisher.calls[0]
	wantRoutingKey := "command.host.2.workshop.cache_verify"
	if call.routingKey != wantRoutingKey {
		t.Errorf("routingKey = %q, want %q", call.routingKey, wantRoutingKey)
	}
	if call.exchange != "manman" {
		t.Errorf("exchange = %q, want %q", call.exchange, "manman")
	}
	cmd, ok := call.body.(*hostrmq.VerifyCacheEntryCommand)
	if !ok {
		t.Fatalf("published body is %T, want *hostrmq.VerifyCacheEntryCommand", call.body)
	}
	if cmd.CacheEntryID != entry.CacheEntryID {
		t.Errorf("cmd.CacheEntryID = %d, want %d", cmd.CacheEntryID, entry.CacheEntryID)
	}
	if cmd.WorkshopID != entry.WorkshopID {
		t.Errorf("cmd.WorkshopID = %q, want %q", cmd.WorkshopID, entry.WorkshopID)
	}
	if cmd.ContentVersion != entry.ContentVersion {
		t.Errorf("cmd.ContentVersion = %q, want %q", cmd.ContentVersion, entry.ContentVersion)
	}
	if cmd.SteamAppID != testSteamAppID {
		t.Errorf("cmd.SteamAppID = %q, want %q", cmd.SteamAppID, testSteamAppID)
	}
}

// TestVerifyCacheEntry_ServerIDZero_NoHostAvailable is FR11's explicit
// "no_host_available is a real, reportable state, not an error" contract:
// when no host holds a copy, the handler must return a normal response
// (Dispatched=false) rather than an error, and must not publish anything.
func TestVerifyCacheEntry_ServerIDZero_NoHostAvailable(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	publisher := &fakeRMQPublisher{}

	entry := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "654321", ContentVersion: "v1",
		CacheKey: "ws/654321/v1", S3Key: "workshop-cache/654321/v1.tar",
	})
	// No entry in repo.hostPresence for this cache_entry_id -- nobody holds a copy.

	h := newVerifyHandler(repo, addonRepo, publisher)
	resp, err := h.VerifyCacheEntry(context.Background(), &pb.VerifyCacheEntryRequest{CacheEntryId: entry.CacheEntryID})
	if err != nil {
		t.Fatalf("VerifyCacheEntry: unexpected error when no host is available: %v", err)
	}
	if resp.Dispatched {
		t.Error("Dispatched = true, want false when no host holds a copy")
	}
	if resp.Status != "no_host_available" {
		t.Errorf("Status = %q, want %q", resp.Status, "no_host_available")
	}
	if publisher.callCount() != 0 {
		t.Errorf("publish was called %d times, want 0 when no host is available", publisher.callCount())
	}
	// The addon/steam_app_id resolution is unnecessary work when nothing
	// will be dispatched -- assert it never even ran.
	addonRepo.AssertNotCalled(t, "GetByWorkshopIDAnyGame", mock.Anything, mock.Anything)
}

// TestVerifyCacheEntry_ExplicitServerID_HonoredEvenWithoutCopy proves an
// Admin can deliberately target a specific host that does not currently hold
// a copy -- the explicit choice is always honored (dispatched=true), unlike
// the server_id==0 path which requires a copy to exist somewhere.
func TestVerifyCacheEntry_ExplicitServerID_HonoredEvenWithoutCopy(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	publisher := &fakeRMQPublisher{}

	entry := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "111222", ContentVersion: "v9",
		CacheKey: "ws/111222/v9", S3Key: "workshop-cache/111222/v9.tar",
	})
	// Presence records a different host than the one explicitly requested --
	// requestedServerID below holds no known copy.
	repo.hostPresence[entry.CacheEntryID] = []*manman.WorkshopCacheHostPresence{
		{CacheEntryID: entry.CacheEntryID, ServerID: 99, LastSeenAt: time.Now()},
	}
	seedVerifyAddon(addonRepo, entry.WorkshopID)

	const requestedServerID = int64(7)
	h := newVerifyHandler(repo, addonRepo, publisher)
	resp, err := h.VerifyCacheEntry(context.Background(), &pb.VerifyCacheEntryRequest{
		CacheEntryId: entry.CacheEntryID,
		ServerId:     requestedServerID,
	})
	if err != nil {
		t.Fatalf("VerifyCacheEntry: unexpected error: %v", err)
	}
	if !resp.Dispatched {
		t.Error("Dispatched = false, want true -- an explicit server_id must always be honored")
	}
	if resp.ServerId != requestedServerID {
		t.Errorf("ServerId = %d, want %d (the explicitly requested host)", resp.ServerId, requestedServerID)
	}

	if publisher.callCount() != 1 {
		t.Fatalf("expected exactly 1 publish call, got %d", publisher.callCount())
	}
	wantRoutingKey := fmt.Sprintf("command.host.%d.workshop.cache_verify", requestedServerID)
	if got := publisher.calls[0].routingKey; got != wantRoutingKey {
		t.Errorf("routingKey = %q, want %q", got, wantRoutingKey)
	}
}

// TestVerifyCacheEntry_UnknownCacheEntryID_NotFound proves an unresolvable
// cache_entry_id surfaces as codes.NotFound and never reaches the presence
// lookup, addon resolution, or publish step.
func TestVerifyCacheEntry_UnknownCacheEntryID_NotFound(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	publisher := &fakeRMQPublisher{}

	h := newVerifyHandler(repo, addonRepo, publisher)
	_, err := h.VerifyCacheEntry(context.Background(), &pb.VerifyCacheEntryRequest{CacheEntryId: 999999})
	assertCode(t, err, codes.NotFound)

	if publisher.callCount() != 0 {
		t.Errorf("publish was called %d times for an unknown cache_entry_id, want 0", publisher.callCount())
	}
	addonRepo.AssertNotCalled(t, "GetByWorkshopIDAnyGame", mock.Anything, mock.Anything)
}

// TestVerifyCacheEntry_MissingCacheEntryID_InvalidArgument guards the
// request-validation half of the RPC, same convention as every other
// handler's `_id is required` check in this package.
func TestVerifyCacheEntry_MissingCacheEntryID_InvalidArgument(t *testing.T) {
	h := newVerifyHandler(newFakeCacheRepo(), new(MockWorkshopAddonRepository), &fakeRMQPublisher{})
	_, err := h.VerifyCacheEntry(context.Background(), &pb.VerifyCacheEntryRequest{})
	assertCode(t, err, codes.InvalidArgument)
}

// TestVerifyCacheEntry_NoSteamAppID_FailedPrecondition covers the case where
// the cache entry's workshop_id cannot be resolved to an addon with a
// steam_app_id -- the host has no way to actually run SteamCMD, so nothing
// is published.
func TestVerifyCacheEntry_NoSteamAppID_FailedPrecondition(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	publisher := &fakeRMQPublisher{}

	entry := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "333444", ContentVersion: "v1",
		CacheKey: "ws/333444/v1", S3Key: "workshop-cache/333444/v1.tar",
	})
	repo.hostPresence[entry.CacheEntryID] = []*manman.WorkshopCacheHostPresence{
		{CacheEntryID: entry.CacheEntryID, ServerID: 5, LastSeenAt: time.Now()},
	}
	addonRepo.On("GetByWorkshopIDAnyGame", mock.Anything, entry.WorkshopID).Return(&manman.WorkshopAddonWithGame{
		WorkshopAddon: manman.WorkshopAddon{WorkshopID: entry.WorkshopID},
		SteamAppID:    nil,
	}, nil)

	h := newVerifyHandler(repo, addonRepo, publisher)
	_, err := h.VerifyCacheEntry(context.Background(), &pb.VerifyCacheEntryRequest{CacheEntryId: entry.CacheEntryID})
	assertCode(t, err, codes.FailedPrecondition)
	if publisher.callCount() != 0 {
		t.Errorf("publish was called %d times when no steam_app_id resolves, want 0", publisher.callCount())
	}
}

// TestVerifyCacheEntry_PublishFailure_Internal covers the dispatch-failure
// path: a broker error surfaces as codes.Internal.
func TestVerifyCacheEntry_PublishFailure_Internal(t *testing.T) {
	repo := newFakeCacheRepo()
	addonRepo := new(MockWorkshopAddonRepository)
	publisher := &fakeRMQPublisher{publishErr: errors.New("simulated broker failure")}

	entry := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "555666", ContentVersion: "v1",
		CacheKey: "ws/555666/v1", S3Key: "workshop-cache/555666/v1.tar",
	})
	repo.hostPresence[entry.CacheEntryID] = []*manman.WorkshopCacheHostPresence{
		{CacheEntryID: entry.CacheEntryID, ServerID: 5, LastSeenAt: time.Now()},
	}
	seedVerifyAddon(addonRepo, entry.WorkshopID)

	h := newVerifyHandler(repo, addonRepo, publisher)
	_, err := h.VerifyCacheEntry(context.Background(), &pb.VerifyCacheEntryRequest{CacheEntryId: entry.CacheEntryID})
	assertCode(t, err, codes.Internal)
}


// --- EvictCacheEntry (FR12) --------------------------------------------------

// seedThreeVersions seeds three cache entries for the same workshop_id --
// three distinct content versions of one addon -- plus a presence row for
// each, so eviction tests can assert the blast radius is exactly the one
// targeted entry and nothing else: not sibling rows, not sibling presence,
// not another workshop_id's row (FR12's core assertion).
func seedThreeVersions(repo *fakeCacheRepo) (v1, v2, v3 *manman.WorkshopCacheEntry) {
	v1 = repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "123456", ContentVersion: "v1", CacheKey: "ws/123456/v1", S3Key: "workshop-cache/123456/v1.tar",
	})
	v2 = repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "123456", ContentVersion: "v2", CacheKey: "ws/123456/v2", S3Key: "workshop-cache/123456/v2.tar",
	})
	v3 = repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "123456", ContentVersion: "v3", CacheKey: "ws/123456/v3", S3Key: "workshop-cache/123456/v3.tar",
	})
	repo.presenceByEntry[v1.CacheEntryID] = []*manman.WorkshopCacheHostPresenceWithServer{{CacheEntryID: v1.CacheEntryID, ServerID: 1, ServerName: "host-a"}}
	repo.presenceByEntry[v2.CacheEntryID] = []*manman.WorkshopCacheHostPresenceWithServer{{CacheEntryID: v2.CacheEntryID, ServerID: 2, ServerName: "host-b"}}
	repo.presenceByEntry[v3.CacheEntryID] = []*manman.WorkshopCacheHostPresenceWithServer{{CacheEntryID: v3.CacheEntryID, ServerID: 3, ServerName: "host-c"}}
	return v1, v2, v3
}

// TestEvictCacheEntry_DeletesExactlyOneObjectAndRow is the issue's core FR12
// assertion: evicting one of three versions of the same addon issues exactly
// one Delete call, for exactly that entry's exact s3_key, and leaves the
// other two rows (and, implicitly, their objects -- never touched by any
// Delete call) fully intact.
func TestEvictCacheEntry_DeletesExactlyOneObjectAndRow(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	v1, v2, v3 := seedThreeVersions(repo)
	h := newTestHandler(repo, presigner)

	resp, err := h.EvictCacheEntry(context.Background(), &pb.EvictCacheEntryRequest{CacheEntryId: v2.CacheEntryID})
	if err != nil {
		t.Fatalf("EvictCacheEntry: unexpected error: %v", err)
	}
	if !resp.Evicted {
		t.Error("Evicted = false, want true")
	}
	if resp.S3Key != v2.S3Key {
		t.Errorf("S3Key = %q, want %q", resp.S3Key, v2.S3Key)
	}

	if len(presigner.deleteCalls) != 1 {
		t.Fatalf("expected exactly 1 Delete call, got %d: %+v", len(presigner.deleteCalls), presigner.deleteCalls)
	}
	if got := presigner.deleteCalls[0].key; got != v2.S3Key {
		t.Errorf("Delete key = %q, want exact key %q", got, v2.S3Key)
	}

	if e, _ := repo.GetCacheEntry(context.Background(), v2.CacheEntryID); e != nil {
		t.Errorf("evicted entry %d still present after eviction", v2.CacheEntryID)
	}

	// The other two versions' rows are untouched.
	if e, _ := repo.GetCacheEntry(context.Background(), v1.CacheEntryID); e == nil {
		t.Errorf("sibling entry %d was removed, want it untouched", v1.CacheEntryID)
	}
	if e, _ := repo.GetCacheEntry(context.Background(), v3.CacheEntryID); e == nil {
		t.Errorf("sibling entry %d was removed, want it untouched", v3.CacheEntryID)
	}
	if repo.entryCount() != 2 {
		t.Errorf("entryCount = %d, want 2 (only the targeted entry removed)", repo.entryCount())
	}
}

// TestEvictCacheEntry_RemovesOnlyItsOwnPresenceRows guards the presence-row
// half of FR12's blast radius: the evicted entry's presence rows are gone
// (cascade), sibling entries' presence rows remain.
func TestEvictCacheEntry_RemovesOnlyItsOwnPresenceRows(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	v1, v2, v3 := seedThreeVersions(repo)
	h := newTestHandler(repo, presigner)

	if _, err := h.EvictCacheEntry(context.Background(), &pb.EvictCacheEntryRequest{CacheEntryId: v2.CacheEntryID}); err != nil {
		t.Fatalf("EvictCacheEntry: unexpected error: %v", err)
	}

	if _, ok := repo.presenceByEntry[v2.CacheEntryID]; ok {
		t.Errorf("presence rows for evicted entry %d still present", v2.CacheEntryID)
	}
	if _, ok := repo.presenceByEntry[v1.CacheEntryID]; !ok {
		t.Errorf("presence rows for sibling entry %d were removed, want them untouched", v1.CacheEntryID)
	}
	if _, ok := repo.presenceByEntry[v3.CacheEntryID]; !ok {
		t.Errorf("presence rows for sibling entry %d were removed, want them untouched", v3.CacheEntryID)
	}
}

// TestEvictCacheEntry_NoSuchKey_TreatedAsSuccess covers the issue's explicit
// convergence rule: an object already absent from S3 is a success, not an
// error, and the row is still removed.
func TestEvictCacheEntry_NoSuchKey_TreatedAsSuccess(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{deleteNoSuchKey: true}
	entry := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "1", ContentVersion: "1", CacheKey: "ws/1/1", S3Key: "workshop-cache/1/1.tar",
	})
	h := newTestHandler(repo, presigner)

	resp, err := h.EvictCacheEntry(context.Background(), &pb.EvictCacheEntryRequest{CacheEntryId: entry.CacheEntryID})
	if err != nil {
		t.Fatalf("EvictCacheEntry: unexpected error on NoSuchKey: %v", err)
	}
	if !resp.Evicted {
		t.Error("Evicted = false, want true on a NoSuchKey convergence")
	}
	if e, _ := repo.GetCacheEntry(context.Background(), entry.CacheEntryID); e != nil {
		t.Error("row still present after a NoSuchKey eviction, want it removed")
	}
}

// TestEvictCacheEntry_S3DeleteError_RowLeftInPlace_RetrySucceeds is the
// ordering regression the issue calls out by name: if the object delete
// fails, the row must be left in place (recoverable by retrying), not
// removed alongside a still-live object nothing could ever find again. A
// second attempt, once S3 cooperates, must then converge successfully.
func TestEvictCacheEntry_S3DeleteError_RowLeftInPlace_RetrySucceeds(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{deleteErr: errors.New("simulated S3 delete failure")}
	entry := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "1", ContentVersion: "1", CacheKey: "ws/1/1", S3Key: "workshop-cache/1/1.tar",
	})
	h := newTestHandler(repo, presigner)

	_, err := h.EvictCacheEntry(context.Background(), &pb.EvictCacheEntryRequest{CacheEntryId: entry.CacheEntryID})
	assertCode(t, err, codes.Internal)
	if e, _ := repo.GetCacheEntry(context.Background(), entry.CacheEntryID); e == nil {
		t.Fatal("row was removed after a failed S3 delete, want it left in place for retry")
	}

	// Retry, now that S3 cooperates.
	presigner.deleteErr = nil
	resp, err := h.EvictCacheEntry(context.Background(), &pb.EvictCacheEntryRequest{CacheEntryId: entry.CacheEntryID})
	if err != nil {
		t.Fatalf("EvictCacheEntry retry: unexpected error: %v", err)
	}
	if !resp.Evicted {
		t.Error("Evicted = false on retry, want true")
	}
	if e, _ := repo.GetCacheEntry(context.Background(), entry.CacheEntryID); e != nil {
		t.Error("row still present after a successful retry, want it removed")
	}
	if len(presigner.deleteCalls) != 2 {
		t.Errorf("expected 2 Delete calls (failed attempt + retry), got %d", len(presigner.deleteCalls))
	}
}

// TestEvictCacheEntry_RowDeleteFails_ObjectAlreadyGone covers the other
// error half: once the object is confirmed gone, a row-delete failure must
// still surface as an error (not a silent partial success) even though the
// object side of the operation already succeeded.
func TestEvictCacheEntry_RowDeleteFails_ObjectAlreadyGone(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	entry := repo.seed(&manman.WorkshopCacheEntry{
		WorkshopID: "1", ContentVersion: "1", CacheKey: "ws/1/1", S3Key: "workshop-cache/1/1.tar",
	})
	repo.deleteCacheEntryErr = errors.New("simulated row delete failure")
	h := newTestHandler(repo, presigner)

	_, err := h.EvictCacheEntry(context.Background(), &pb.EvictCacheEntryRequest{CacheEntryId: entry.CacheEntryID})
	assertCode(t, err, codes.Internal)
	if len(presigner.deleteCalls) != 1 {
		t.Errorf("expected exactly 1 Delete call, got %d", len(presigner.deleteCalls))
	}
}

// TestEvictCacheEntry_UnknownID_NotFound proves an unknown cache_entry_id
// surfaces as codes.NotFound and never reaches S3 at all -- no Delete call
// for an id that doesn't resolve to a real entry.
func TestEvictCacheEntry_UnknownID_NotFound(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	h := newTestHandler(repo, presigner)

	_, err := h.EvictCacheEntry(context.Background(), &pb.EvictCacheEntryRequest{CacheEntryId: 999})
	assertCode(t, err, codes.NotFound)
	if len(presigner.deleteCalls) != 0 {
		t.Errorf("Delete was called %d times for an unknown cache_entry_id, want 0", len(presigner.deleteCalls))
	}
}

// TestEvictCacheEntry_MissingID_InvalidArgument guards the request
// validation, same convention as every other handler's `_id is required`
// check in this package.
func TestEvictCacheEntry_MissingID_InvalidArgument(t *testing.T) {
	h := newTestHandler(newFakeCacheRepo(), &fakePresigner{})
	_, err := h.EvictCacheEntry(context.Background(), &pb.EvictCacheEntryRequest{})
	assertCode(t, err, codes.InvalidArgument)
}

// TestEvictCacheEntry_NeverIssuesPrefixOrBulkDelete is the FR12
// blast-radius regression pinned by name: fakePresigner.Delete only ever
// receives a single, fully-qualified object key (never a prefix, glob, or a
// call carrying more than one key) across every eviction path exercised
// above. cachePresigner's Delete signature itself (one key, one call) rules
// out a bulk/multi-object delete API from ever being reachable here; this
// asserts the handler never even constructs something prefix-shaped.
func TestEvictCacheEntry_NeverIssuesPrefixOrBulkDelete(t *testing.T) {
	repo := newFakeCacheRepo()
	presigner := &fakePresigner{}
	v1, v2, v3 := seedThreeVersions(repo)
	h := newTestHandler(repo, presigner)

	for _, id := range []int64{v1.CacheEntryID, v2.CacheEntryID, v3.CacheEntryID} {
		if _, err := h.EvictCacheEntry(context.Background(), &pb.EvictCacheEntryRequest{CacheEntryId: id}); err != nil {
			t.Fatalf("EvictCacheEntry(%d): unexpected error: %v", id, err)
		}
	}

	if len(presigner.deleteCalls) != 3 {
		t.Fatalf("expected exactly 3 Delete calls (one per entry), got %d", len(presigner.deleteCalls))
	}
	seen := map[string]bool{}
	for _, call := range presigner.deleteCalls {
		if call.key == "" {
			t.Error("Delete called with an empty key")
		}
		if strings.HasSuffix(call.key, "/") || strings.Contains(call.key, "*") {
			t.Errorf("Delete key %q looks like a prefix/wildcard, want a single object key", call.key)
		}
		if seen[call.key] {
			t.Errorf("Delete called twice with the same key %q", call.key)
		}
		seen[call.key] = true
	}
}

// --- shared assertion helper ------------------------------------------------

func assertCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with code %s, got nil", want)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected a gRPC status error, got %v", err)
	}
	if st.Code() != want {
		t.Fatalf("code = %s, want %s (message: %s)", st.Code(), want, st.Message())
	}
}
