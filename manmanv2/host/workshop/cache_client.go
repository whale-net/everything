package workshop

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CacheClient fetches Workshop addon content from S3 using short-lived
// presigned URLs obtained from control-api. It holds no S3 credentials.
type CacheClient struct {
	workshopClient pb.WorkshopServiceClient
	serverID       int64
	httpClient     *http.Client
}

// NewCacheClient constructs a CacheClient. If httpClient is nil, http.DefaultClient is used.
func NewCacheClient(workshopClient pb.WorkshopServiceClient, serverID int64, httpClient *http.Client) *CacheClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &CacheClient{
		workshopClient: workshopClient,
		serverID:       serverID,
		httpClient:     httpClient,
	}
}

// cacheObjectFormat documents the wire format a cache hit's object body must be in: an
// uncompressed tar archive of the finished addon content (i.e. exactly what would land
// under destDir after a normal SteamCMD download completes -- including any
// _legacy.bin -> <workshopid>.vpk rename already applied). This matches the ".tar" (not
// ".tar.gz") extension workshop.S3Key already derives (manmanv2/api/workshop/cachekey.go)
// -- the write side (#2184) is responsible for producing objects in this format so this
// read path's extraction logic matches what was uploaded.
const cacheObjectFormat = "tar"

// TryFetch asks control-api for a presigned GET URL for the given (workshopID,
// contentVersion) pair and, on a cache hit, streams the object from S3 into destDir.
//
// hit == false, err == nil means control-api reports no cache entry for this content —
// this is the ordinary path for a first-ever download and callers must fall back to a
// normal SteamCMD download without treating it as an error.
//
// hit == false, err != nil (e.g. an expired presigned URL, RPC failure, or the RPC being
// Unimplemented against an older control-api) must also degrade to the SteamCMD fallback
// path rather than failing the install.
func (c *CacheClient) TryFetch(ctx context.Context, workshopID, contentVersion, destDir string) (hit bool, err error) {
	logger := slog.With("workshop_id", workshopID, "content_version", contentVersion)
	start := time.Now()

	resp, err := c.workshopClient.GetCacheDownloadURL(ctx, &pb.GetCacheDownloadURLRequest{
		ServerId:       c.serverID,
		WorkshopId:     workshopID,
		ContentVersion: contentVersion,
	})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.Unimplemented {
			// Older control-api or an unavailable relay: this is an expected
			// deployment skew case, not a genuine error. Fall back to SteamCMD.
			logger.Info("control-api does not support workshop cache relay, falling back to SteamCMD")
		} else {
			logger.Warn("workshop cache lookup failed, falling back to SteamCMD", "error", err)
		}
		return false, err
	}

	if !resp.CacheHit {
		// Ordinary miss path (e.g. first-ever download of this content version) — not an error.
		return false, nil
	}

	if err := fetchAndExtract(ctx, c.httpClient, resp.PresignedUrl, destDir); err != nil {
		// A stale/expired presigned URL (or any other transfer failure) must degrade to
		// the SteamCMD fallback, never fail the install outright (NFR: see issue #2183).
		logger.Warn("workshop cache download failed, falling back to SteamCMD", "cache_entry_id", resp.CacheEntryId, "error", err)
		return false, err
	}

	duration := time.Since(start)
	logger.Info("workshop cache hit: fetched addon content from S3",
		"cache_entry_id", resp.CacheEntryId, "bytes", resp.SizeBytes, "duration", duration)

	// Report the completed read back to control-api so host presence is recorded (FR10).
	// Presence must only be recorded after the object has actually landed, which is why
	// this call happens after fetchAndExtract succeeds, not before. A failure here does not
	// invalidate the cache hit -- the content is already in place -- so it is logged and
	// swallowed rather than turned into a fallback-triggering error.
	if resp.CacheEntryId != 0 {
		if _, reportErr := c.workshopClient.ReportCacheRead(ctx, &pb.ReportCacheReadRequest{
			ServerId:     c.serverID,
			CacheEntryId: resp.CacheEntryId,
		}); reportErr != nil {
			logger.Warn("failed to report workshop cache read to control-api", "cache_entry_id", resp.CacheEntryId, "error", reportErr)
		}
	}

	return true, nil
}

// fetchAndExtract streams presignedURL's body into a temp file and extracts it (as a
// tar archive, see cacheObjectFormat) into a staging directory, then atomically renames
// the staging directory into place at destDir. Both the temp file and staging directory are
// created as siblings of destDir so the final rename is same-filesystem and atomic, and so an
// interrupted transfer or extraction never leaves partial content visible at destDir: until
// the very last step, nothing exists at destDir's path at all.
//
// The presigned URL and its query string are never logged (NFR6) — errors returned by this
// function must not embed it either.
func fetchAndExtract(ctx context.Context, httpClient *http.Client, presignedURL, destDir string) error {
	parentDir := filepath.Dir(destDir)
	if err := os.MkdirAll(parentDir, 0777); err != nil {
		return fmt.Errorf("failed to create parent directory %s: %w", parentDir, err)
	}

	tmpFile, err := os.CreateTemp(parentDir, ".workshop-cache-download-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp download file: %w", err)
	}
	tmpFilePath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpFilePath)
	}()

	stagingDir, err := os.MkdirTemp(parentDir, ".workshop-cache-staging-*")
	if err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, presignedURL, nil)
	if err != nil {
		return fmt.Errorf("failed to build cache download request: %w", err)
	}

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		// http errors can embed the request URL; strip it defensively so the presigned
		// URL and its query string (which carries the signature) never reach logs (NFR6).
		return fmt.Errorf("cache download request failed: %w", scrubURL(err, presignedURL))
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		// Deliberately do not include the response body here: some object stores echo
		// request parameters (including signed query strings) back in error bodies.
		return fmt.Errorf("cache download returned HTTP %d", httpResp.StatusCode)
	}

	// Stream — do not buffer the whole addon in memory; addons are large.
	if _, err := io.Copy(tmpFile, httpResp.Body); err != nil {
		return fmt.Errorf("cache download interrupted: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to finalize downloaded file: %w", err)
	}

	if err := extractTar(tmpFilePath, stagingDir); err != nil {
		return fmt.Errorf("failed to extract cached content: %w", err)
	}

	// Final atomic step: nothing at destDir's path is touched until the extracted content
	// is fully staged, so an interrupted download/extract never leaves half-written content
	// at destDir. If destDir already exists (unexpected for a fresh install), it is removed
	// first — os.Rename onto a pre-existing non-empty directory fails on POSIX.
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("failed to clear destination directory: %w", err)
	}
	if err := os.Rename(stagingDir, destDir); err != nil {
		return fmt.Errorf("failed to move cached content into place: %w", err)
	}

	return nil
}

// extractTar extracts the uncompressed tar archive at tarPath into destDir, which must
// already exist. Path traversal entries (e.g. "../") are rejected defensively even though
// the archive originates from our own trusted cache-write path.
func extractTar(tarPath, destDir string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("corrupt tar stream: %w", err)
		}

		cleanName := filepath.Clean(hdr.Name)
		if cleanName == "." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) || cleanName == ".." {
			continue
		}
		targetPath := filepath.Join(destDir, cleanName)
		if !strings.HasPrefix(targetPath, filepath.Clean(destDir)+string(filepath.Separator)) && targetPath != filepath.Clean(destDir) {
			return fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0777); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0777); err != nil {
				return err
			}
			out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0777|0600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			// Symlinks and other special entry types are not expected from the
			// cache-write path; skip rather than fail the whole extraction.
			continue
		}
	}
}

// scrubURL replaces any occurrence of rawURL within err's message with a redacted
// placeholder, defending against HTTP client errors that embed the request URL verbatim
// (NFR6: never log the presigned URL or its query string).
func scrubURL(err error, rawURL string) error {
	if err == nil || rawURL == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), rawURL, "[redacted-presigned-url]")
	return fmt.Errorf("%s", msg)
}
