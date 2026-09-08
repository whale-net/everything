package workshop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/manmanv2/api/workshop"
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

// fakePresigner is the cachePresigner test double the issue's Testing
// section asks for: it never touches real S3, records every call it
// receives, and can be told to fail on demand so the handler's error path
// is exercised without a real presign failure being reproducible.
type fakePresigner struct {
	mu sync.Mutex

	getCalls []presignCall
	putCalls []presignCall

	getErr error
	putErr error
}

func (f *fakePresigner) PresignGetURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls = append(f.getCalls, presignCall{key: key, ttl: ttl})
	if f.getErr != nil {
		return "", f.getErr
	}
	return fmt.Sprintf("https://cache-bucket.s3.example.com/%s?X-Amz-Signature=deadbeefdeadbeef&X-Amz-Expires=%d", key, int(ttl.Seconds())), nil
}

func (f *fakePresigner) PresignPutURL(_ context.Context, key string, ttl time.Duration) (string, error) {
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
}

func newFakeCacheRepo() *fakeCacheRepo {
	return &fakeCacheRepo{entries: map[string]*manman.WorkshopCacheEntry{}}
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

func (f *fakeCacheRepo) ListCacheEntriesForWorkshopID(_ context.Context, _ string) ([]*manman.WorkshopCacheEntry, error) {
	return nil, nil
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

func (f *fakeCacheRepo) DeleteCacheEntry(_ context.Context, cacheEntryID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, e := range f.entries {
		if e.CacheEntryID == cacheEntryID {
			delete(f.entries, k)
			return nil
		}
	}
	return nil
}

func (f *fakeCacheRepo) UpsertHostPresence(_ context.Context, _, _ int64) error {
	return nil
}

func (f *fakeCacheRepo) ListHostPresence(_ context.Context, _ int64) ([]*manman.WorkshopCacheHostPresence, error) {
	return nil, nil
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
